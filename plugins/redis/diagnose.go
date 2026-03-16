package redis

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/pkg/conv"
	"github.com/cprobe/catpaw/plugins"
)

var _ plugins.Diagnosable = (*RedisPlugin)(nil)

// RegisterDiagnoseTools implements plugins.Diagnosable for RedisPlugin.
// It registers read-only diagnostic tools and the accessor factory.
func (p *RedisPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("redis", "redis", "Redis diagnostic tools (INFO, CLUSTER, SLOWLOG, CLIENT LIST, CONFIG GET, LATENCY, MEMORY ANALYSIS, BIGKEYS)", diagnose.ToolScopeRemote)

	registry.Register("redis", diagnose.DiagnoseTool{
		Name: "redis_info",
		Description: "Execute Redis INFO command. Sections: server (version/uptime/mode), clients (connections/blocked), " +
			"memory (used/peak/fragmentation), stats (ops/hits/misses/evictions), replication (role/lag/slaves), " +
			"cpu, keyspace (per-db keys/expires/avg_ttl), persistence (rdb/aof status), all (everything). " +
			"Default: all. NOTE: INFO all is pre-collected in context — use this only for refreshing specific sections.",
		Parameters: []diagnose.ToolParam{
			{Name: "section", Type: "string", Description: "INFO section: server, clients, memory, stats, replication, cpu, keyspace, persistence, all", Required: false},
		},
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			section := args["section"]
			if section == "" {
				section = "all"
			}
			raw, infoErr := acc.InfoRaw(section)
			if infoErr != nil {
				return "", fmt.Errorf("redis INFO %s: %w", section, infoErr)
			}
			return raw, nil
		},
	})

	registry.Register("redis", diagnose.DiagnoseTool{
		Name: "redis_cluster_info",
		Description: "Show Redis Cluster health, topology, and peer connectivity. " +
			"Returns CLUSTER INFO, CLUSTER NODES, and automatically probes all peer nodes " +
			"to report reachability and key metrics (memory, clients, ops/s, replication lag). " +
			"Use for cluster_state, cluster_topology, or any cluster-related alerts. " +
			"Gracefully reports when the target is not running in cluster mode.",
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			ins, _ := session.InstanceRef().(*Instance)

			serverInfo, infoErr := acc.Info("server")
			if infoErr != nil {
				return "", fmt.Errorf("redis INFO server: %w", infoErr)
			}
			mode := strings.ToLower(strings.TrimSpace(serverInfo["redis_mode"]))
			if mode != redisModeCluster {
				if mode == "" {
					mode = redisModeStandalone
				}
				return fmt.Sprintf("Redis target is running in %s mode, not cluster mode.", mode), nil
			}

			clusterInfo, clusterInfoErr := acc.ClusterInfo()
			if clusterInfoErr != nil {
				return "", fmt.Errorf("redis CLUSTER INFO: %w", clusterInfoErr)
			}
			clusterNodes, clusterNodesErr := acc.ClusterNodes()
			if clusterNodesErr != nil {
				return "", fmt.Errorf("redis CLUSTER NODES: %w", clusterNodesErr)
			}

			failNodes, pfailNodes := summarizeClusterNodeFlags(clusterNodes)
			parsedInfo := parseInfoToMap(clusterInfo)

			var b strings.Builder
			fmt.Fprintf(&b, "[CLUSTER SUMMARY]\n")
			fmt.Fprintf(&b, "cluster_state: %s\n", parsedInfo["cluster_state"])
			if v := parsedInfo["cluster_slots_assigned"]; v != "" {
				fmt.Fprintf(&b, "cluster_slots_assigned: %s\n", v)
			}
			if v := parsedInfo["cluster_slots_fail"]; v != "" {
				fmt.Fprintf(&b, "cluster_slots_fail: %s\n", v)
			}
			if v := parsedInfo["cluster_known_nodes"]; v != "" {
				fmt.Fprintf(&b, "cluster_known_nodes: %s\n", v)
			}
			fmt.Fprintf(&b, "fail_nodes: %d\n", failNodes)
			fmt.Fprintf(&b, "pfail_nodes: %d\n\n", pfailNodes)
			fmt.Fprintf(&b, "[CLUSTER INFO]\n%s\n\n", clusterInfo)
			fmt.Fprintf(&b, "[CLUSTER NODES]\n%s\n", clusterNodes)

			if ins != nil {
				nodes := parseClusterNodes(clusterNodes)
				if len(nodes) > 1 {
					probeable, unprobeable := selectProbeCandidates(nodes, acc.Target())
					if len(probeable) > 0 || len(unprobeable) > 0 {
						var results []peerProbeResult
						if len(probeable) > 0 {
							results = probePeers(ctx, probeable, ins)
						}
						fmt.Fprintf(&b, "\n%s", formatPeerConnectivity(results, unprobeable, len(nodes), len(probeable)))
					}
				}
			}

			return b.String(), nil
		},
	})

	registry.Register("redis", diagnose.DiagnoseTool{
		Name:        "redis_slowlog",
		Description: "Get recent slow queries from Redis SLOWLOG (default: last 10 entries)",
		Parameters: []diagnose.ToolParam{
			{Name: "count", Type: "int", Description: "Number of slow log entries to retrieve (default 10)", Required: false},
		},
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			count := 10
			if v, ok := args["count"]; ok && v != "" {
				fmt.Sscanf(v, "%d", &count)
			}
			result, slowErr := acc.SlowlogGet(count)
			if slowErr != nil {
				return "", fmt.Errorf("redis SLOWLOG GET: %w", slowErr)
			}
			return result, nil
		},
	})

	registry.Register("redis", diagnose.DiagnoseTool{
		Name:        "redis_client_list",
		Description: "List connected Redis clients (CLIENT LIST). When client count exceeds 5000, returns a summary instead of full list to avoid blocking Redis.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}

			const safeThreshold = 5000
			info, infoErr := acc.Info("clients")
			if infoErr == nil {
				if countStr, ok := info["connected_clients"]; ok {
					if count, parseErr := strconv.Atoi(countStr); parseErr == nil && count > safeThreshold {
						return clientSummaryFromInfo(acc, count)
					}
				}
			}

			result, clErr := acc.ClientList()
			if clErr != nil {
				return "", fmt.Errorf("redis CLIENT LIST: %w", clErr)
			}
			return result, nil
		},
	})

	registry.Register("redis", diagnose.DiagnoseTool{
		Name:        "redis_config_get",
		Description: "Get Redis configuration parameters (CONFIG GET). Sensitive values are redacted.",
		Parameters: []diagnose.ToolParam{
			{Name: "pattern", Type: "string", Description: "Config pattern, e.g. maxmemory*, save, * (default: *)", Required: false},
		},
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			pattern := args["pattern"]
			result, cfgErr := acc.ConfigGet(pattern)
			if cfgErr != nil {
				return "", fmt.Errorf("redis CONFIG GET %s: %w", pattern, cfgErr)
			}
			return result, nil
		},
	})

	registry.Register("redis", diagnose.DiagnoseTool{
		Name:        "redis_latency",
		Description: "Show latest latency events by event type (LATENCY LATEST). Requires latency-monitor-threshold to be set (e.g. CONFIG SET latency-monitor-threshold 100). Returns event name, timestamp, latest latency ms, max latency ms.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			result, latErr := acc.Command("LATENCY", "LATEST")
			if latErr != nil {
				return "", fmt.Errorf("redis LATENCY LATEST: %w", latErr)
			}
			if result == "" || result == "(nil)" {
				return "No latency events recorded. Note: latency-monitor-threshold may be 0 (disabled). Use redis_config_get with pattern 'latency-monitor-threshold' to check.", nil
			}
			return result, nil
		},
	})

	registry.Register("redis", diagnose.DiagnoseTool{
		Name:        "redis_memory_analysis",
		Description: "Combined memory diagnosis: runs both MEMORY DOCTOR (health advice) and MEMORY STATS (detailed breakdown including dataset size, overhead, fragmentation ratio, allocator stats, replication/AOF buffers). More granular than INFO memory.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			var b strings.Builder

			doctor, doctorErr := acc.Command("MEMORY", "DOCTOR")
			if doctorErr != nil {
				fmt.Fprintf(&b, "[MEMORY DOCTOR] error: %v\n\n", doctorErr)
			} else {
				fmt.Fprintf(&b, "[MEMORY DOCTOR]\n%s\n\n", doctor)
			}

			stats, statsErr := acc.Command("MEMORY", "STATS")
			if statsErr != nil {
				fmt.Fprintf(&b, "[MEMORY STATS] error: %v\n", statsErr)
			} else {
				fmt.Fprintf(&b, "[MEMORY STATS]\n%s\n", stats)
			}

			if doctorErr != nil && statsErr != nil {
				return "", fmt.Errorf("redis MEMORY DOCTOR: %w; MEMORY STATS: %w", doctorErr, statsErr)
			}
			return b.String(), nil
		},
	})

	registry.Register("redis", diagnose.DiagnoseTool{
		Name:        "redis_bigkeys_scan",
		Description: "Sample keys using SCAN and estimate their sizes with MEMORY USAGE to find big keys on the current Redis node. Diagnosis-only heavy tool: bounded by sample_keys (default 1000, max 5000) and topn (default 20, max 100). Optional match pattern narrows the scan.",
		Parameters: []diagnose.ToolParam{
			{Name: "sample_keys", Type: "int", Description: "Maximum number of keys to sample, default 1000, max 5000", Required: false},
			{Name: "topn", Type: "int", Description: "How many largest keys to return, default 20, max 100", Required: false},
			{Name: "match", Type: "string", Description: "Optional SCAN MATCH pattern, e.g. user:* or cart:*", Required: false},
		},
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			sampleKeys := clampInt(parseIntArg(args["sample_keys"], 1000), 1, 5000)
			topN := clampInt(parseIntArg(args["topn"], 20), 1, 100)
			match := strings.TrimSpace(args["match"])

			cursor := "0"
			sampled := 0
			countByType := map[string]int{}
			candidates := make([]bigkeyCandidate, 0, topN)

			for {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				default:
				}

				remaining := sampleKeys - sampled
				if remaining <= 0 {
					break
				}
				scanCount := clampInt(remaining, 1, 200)
				scanArgs := []string{"SCAN", cursor}
				if match != "" {
					scanArgs = append(scanArgs, "MATCH", match)
				}
				scanArgs = append(scanArgs, "COUNT", strconv.Itoa(scanCount))

				reply, scanErr := acc.RawCommand(scanArgs...)
				if scanErr != nil {
					return "", fmt.Errorf("redis SCAN: %w", scanErr)
				}
				nextCursor, keys, err := parseScanReply(reply)
				if err != nil {
					return "", err
				}
				cursor = nextCursor
				for _, key := range keys {
					if sampled >= sampleKeys {
						break
					}
					typ, typeErr := acc.Command("TYPE", key)
					if typeErr != nil {
						continue
					}
					typ = strings.TrimSpace(typ)
					if typ == "" || typ == "(nil)" {
						continue
					}
					memReply, memErr := acc.RawCommand("MEMORY", "USAGE", key)
					if memErr != nil {
						continue
					}
					size, ok, err := parseIntegerReply(memReply)
					if err != nil || !ok || size < 0 {
						continue
					}

					sampled++
					countByType[typ]++
					candidates = append(candidates, bigkeyCandidate{
						Key:  key,
						Type: typ,
						Size: size,
					})
				}
				if cursor == "0" || len(keys) == 0 {
					break
				}
			}

			sort.Slice(candidates, func(i, j int) bool {
				if candidates[i].Size == candidates[j].Size {
					return candidates[i].Key < candidates[j].Key
				}
				return candidates[i].Size > candidates[j].Size
			})
			if len(candidates) > topN {
				candidates = candidates[:topN]
			}

			typeNames := make([]string, 0, len(countByType))
			for typ := range countByType {
				typeNames = append(typeNames, typ)
			}
			sort.Strings(typeNames)

			var b strings.Builder
			fmt.Fprintf(&b, "[BIGKEYS SUMMARY]\n")
			fmt.Fprintf(&b, "sampled_keys: %d\n", sampled)
			if match != "" {
				fmt.Fprintf(&b, "match: %s\n", match)
			}
			if len(typeNames) == 0 {
				fmt.Fprintf(&b, "types: none\n\n")
			} else {
				fmt.Fprintf(&b, "types:\n")
				for _, typ := range typeNames {
					fmt.Fprintf(&b, "  %s: %d\n", typ, countByType[typ])
				}
				fmt.Fprintln(&b)
			}
			if len(candidates) == 0 {
				fmt.Fprintf(&b, "No keys sampled with MEMORY USAGE. Keys may have expired during scan or the match pattern returned nothing.\n")
				return b.String(), nil
			}
			fmt.Fprintf(&b, "[TOP %d BIGGEST KEYS]\n", len(candidates))
			for i, cand := range candidates {
				fmt.Fprintf(&b, "%d. %s  type=%s  size=%s (%d bytes)\n",
					i+1, cand.Key, cand.Type, conv.HumanBytes(uint64(cand.Size)), cand.Size)
			}
			return b.String(), nil
		},
	})

	registry.Register("redis", diagnose.DiagnoseTool{
		Name: "redis_query_peer",
		Description: "Connect to a specific peer Redis node and run a read-only diagnostic command. " +
			"Use after redis_cluster_info reveals issues on a specific peer that need deeper investigation. " +
			"Peer address should come from the CLUSTER NODES output. " +
			"Supported commands: info [section], slowlog [count], client_list, cluster_info, " +
			"memory_doctor, memory_stats, latency, config_get [pattern].",
		Parameters: []diagnose.ToolParam{
			{Name: "peer", Type: "string", Description: "Peer address host:port from CLUSTER NODES output", Required: true},
			{Name: "command", Type: "string", Description: "Command: info, slowlog, client_list, cluster_info, memory_doctor, memory_stats, latency, config_get", Required: true},
			{Name: "args", Type: "string", Description: "Optional args: section for info, count for slowlog, pattern for config_get", Required: false},
		},
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			ins, ok := session.InstanceRef().(*Instance)
			if !ok || ins == nil {
				return "", fmt.Errorf("redis_query_peer requires instance reference for authentication")
			}
			sessionAcc, err := getAccessor(session)
			if err != nil {
				return "", fmt.Errorf("redis_query_peer requires session accessor for topology validation: %w", err)
			}
			peer := strings.TrimSpace(args["peer"])
			if peer == "" {
				return "", fmt.Errorf("peer address is required")
			}
			command := strings.ToLower(strings.TrimSpace(args["command"]))
			if command == "" {
				return "", fmt.Errorf("command is required")
			}
			cmdArgs := strings.TrimSpace(args["args"])
			return executePeerCommand(ctx, ins, sessionAcc, peer, command, cmdArgs)
		},
	})

	registry.RegisterAccessorFactory("redis", func(ctx context.Context, instanceRef any, target string) (any, error) {
		ins, ok := instanceRef.(*Instance)
		if !ok {
			return nil, fmt.Errorf("redis accessor factory: expected *Instance, got %T", instanceRef)
		}
		if target == "" && len(ins.Targets) > 0 {
			target = ins.Targets[0]
		}
		return NewRedisAccessor(RedisAccessorConfig{
			Target:      target,
			Username:    ins.Username,
			Password:    ins.Password,
			DB:          ins.DB,
			Timeout:     time.Duration(ins.Timeout),
			ReadTimeout: time.Duration(ins.ReadTimeout),
			TLSConfig:   ins.tlsConfig,
			DialFunc:    ins.dialFunc,
		})
	})

	registry.RegisterPreCollector("redis", func(ctx context.Context, accessor any) string {
		acc, ok := accessor.(*RedisAccessor)
		if !ok {
			return ""
		}
		raw, err := acc.InfoRaw("all")
		if err != nil {
			return ""
		}
		serverInfo, infoErr := acc.Info("server")
		if infoErr != nil {
			return raw
		}
		if strings.ToLower(strings.TrimSpace(serverInfo["redis_mode"])) != redisModeCluster {
			return raw
		}
		clusterInfo, clusterErr := acc.ClusterInfo()
		if clusterErr != nil {
			return raw
		}
		return raw + "\n\n[CLUSTER INFO]\n" + clusterInfo
	})

	registry.SetDiagnoseHints("redis", `
- 内存告警 → 先分析预采集数据中的 memory 部分，再调 redis_memory_analysis + redis_config_get pattern=maxmemory*；怀疑大 key 时再调 redis_bigkeys_scan
- 延迟/慢查询 → redis_slowlog + redis_latency（可并行调用）
- 连接数告警 → redis_client_list + redis_config_get pattern=maxclients
- 复制告警 → 预采集数据中的 replication 部分已包含完整复制状态，通常无需额外工具
- Cluster 告警 → 调 redis_cluster_info 获取集群快照（含 PEER CONNECTIVITY 全节点连通性和关键指标），根据异常节点决定是否用 redis_query_peer 深查（如 slowlog、memory_doctor）
- 持久化告警 → 预采集数据中的 persistence 部分 + redis_config_get pattern=save*
- 通用排查 → 预采集数据已含 INFO all，直接分析后按需深入
- 首轮建议并行调用 2-3 个最相关的工具，避免逐个调用浪费轮次
- redis_query_peer 仅在 redis_cluster_info 的 PEER CONNECTIVITY 不足以定位问题时使用，大多数集群问题可直接从快照判断`)
}

func getAccessor(session *diagnose.DiagnoseSession) (*RedisAccessor, error) {
	if session.Accessor == nil {
		return nil, fmt.Errorf("no redis accessor in session (remote connection not established)")
	}
	acc, ok := session.Accessor.(*RedisAccessor)
	if !ok {
		return nil, fmt.Errorf("session accessor is %T, expected *RedisAccessor", session.Accessor)
	}
	return acc, nil
}

func clientSummaryFromInfo(acc *RedisAccessor, clientCount int) (string, error) {
	info, err := acc.Info("clients")
	if err != nil {
		return "", fmt.Errorf("INFO clients: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[!] CLIENT LIST skipped: %d connected clients exceeds safe threshold (5000).\n", clientCount)
	fmt.Fprintf(&b, "Running CLIENT LIST with this many clients would block Redis for ~%dms.\n\n", clientCount/500)
	fmt.Fprintf(&b, "Client summary from INFO clients:\n")

	keys := []string{
		"connected_clients", "cluster_connections", "maxclients",
		"client_recent_max_input_buffer", "client_recent_max_output_buffer",
		"blocked_clients", "tracking_clients", "clients_in_timeout_table",
		"total_blocking_clients",
	}
	for _, k := range keys {
		if v, ok := info[k]; ok {
			fmt.Fprintf(&b, "  %-40s %s\n", k, v)
		}
	}

	fmt.Fprintf(&b, "\nTip: Use redis_info with section=clients for full client statistics.\n")
	return b.String(), nil
}

type bigkeyCandidate struct {
	Key  string
	Type string
	Size int64
}

func parseIntArg(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func clampInt(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func parseScanReply(reply any) (cursor string, keys []string, err error) {
	arr, ok := reply.([]any)
	if !ok || len(arr) != 2 {
		return "", nil, fmt.Errorf("redis SCAN returned unexpected reply type %T", reply)
	}
	cursor, ok = arr[0].(string)
	if !ok {
		return "", nil, fmt.Errorf("redis SCAN cursor has unexpected type %T", arr[0])
	}
	keys, err = parseStringArray(arr[1])
	if err != nil {
		return "", nil, err
	}
	return cursor, keys, nil
}

func parseStringArray(reply any) ([]string, error) {
	arr, ok := reply.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected array reply type %T", reply)
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected array item type %T", item)
		}
		out = append(out, s)
	}
	return out, nil
}

func parseIntegerReply(reply any) (int64, bool, error) {
	switch v := reply.(type) {
	case nil:
		return 0, false, nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, true, err
		}
		return n, true, nil
	default:
		return 0, false, fmt.Errorf("unexpected integer reply type %T", reply)
	}
}
