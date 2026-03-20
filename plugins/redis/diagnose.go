package redis

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/plugins"
)

var _ plugins.Diagnosable = (*RedisPlugin)(nil)

// RegisterDiagnoseTools implements plugins.Diagnosable for RedisPlugin.
// It registers read-only diagnostic tools and the accessor factory.
func (p *RedisPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("redis", "redis", "Redis diagnostic tools (INFO, SLOWLOG, CLIENT LIST, CONFIG GET, DBSIZE, LATENCY, MEMORY DOCTOR/STATS)", diagnose.ToolScopeRemote)

	registry.Register("redis", diagnose.DiagnoseTool{
		Name:        "redis_info",
		Description: "Execute Redis INFO command for a specific section (server, clients, memory, stats, replication, cpu, keyspace, persistence, all)",
		Parameters: []diagnose.ToolParam{
			{Name: "section", Type: "string", Description: "INFO section name, e.g. memory, replication, all", Required: false},
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
		Name:        "redis_dbsize",
		Description: "Get the number of keys in the current Redis database (DBSIZE)",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			result, dbErr := acc.DBSize()
			if dbErr != nil {
				return "", fmt.Errorf("redis DBSIZE: %w", dbErr)
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
		Name:        "redis_memory_doctor",
		Description: "Run Redis built-in memory health check (MEMORY DOCTOR). Returns diagnostic advice about memory issues: fragmentation, peak usage, RSS overhead, etc.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			result, memErr := acc.Command("MEMORY", "DOCTOR")
			if memErr != nil {
				return "", fmt.Errorf("redis MEMORY DOCTOR: %w", memErr)
			}
			return result, nil
		},
	})

	registry.Register("redis", diagnose.DiagnoseTool{
		Name:        "redis_memory_stats",
		Description: "Show detailed memory breakdown (MEMORY STATS): dataset size, overhead, fragmentation ratio, allocator stats, replication/AOF buffers. More granular than INFO memory.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			result, memErr := acc.Command("MEMORY", "STATS")
			if memErr != nil {
				return "", fmt.Errorf("redis MEMORY STATS: %w", memErr)
			}
			return result, nil
		},
	})

	registry.RegisterAccessorFactory("redis", func(ctx context.Context, instanceRef any) (any, error) {
		ins, ok := instanceRef.(*Instance)
		if !ok {
			return nil, fmt.Errorf("redis accessor factory: expected *Instance, got %T", instanceRef)
		}
		return NewRedisAccessor(RedisAccessorConfig{
			Target:      ins.Targets[0],
			Username:    ins.Username,
			Password:    ins.Password,
			DB:          ins.DB,
			Timeout:     time.Duration(ins.Timeout),
			ReadTimeout: time.Duration(ins.ReadTimeout),
			TLSConfig:   ins.tlsConfig,
			DialFunc:    ins.dialFunc,
		})
	})
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
