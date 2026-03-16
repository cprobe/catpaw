package redis

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/plugins"
)

var _ plugins.Diagnosable = (*RedisPlugin)(nil)

// RegisterDiagnoseTools implements plugins.Diagnosable for RedisPlugin.
// It registers read-only diagnostic tools and the accessor factory.
func (p *RedisPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("redis", "redis", "Redis diagnostic tools (INFO, SLOWLOG, CLIENT LIST, CONFIG GET, LATENCY, MEMORY ANALYSIS)", diagnose.ToolScopeRemote)

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
		return raw
	})

	registry.SetDiagnoseHints("redis", `
- 内存告警 → 先分析预采集数据中的 memory 部分，再调 redis_memory_analysis + redis_config_get pattern=maxmemory*
- 延迟/慢查询 → redis_slowlog + redis_latency（可并行调用）
- 连接数告警 → redis_client_list + redis_config_get pattern=maxclients
- 复制告警 → 预采集数据中的 replication 部分已包含完整复制状态，通常无需额外工具
- 持久化告警 → 预采集数据中的 persistence 部分 + redis_config_get pattern=save*
- 通用排查 → 预采集数据已含 INFO all，直接分析后按需深入
- 首轮建议并行调用 2-3 个最相关的工具，避免逐个调用浪费轮次`)
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
