package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/plugins"
)

var _ plugins.Diagnosable = (*RedisPlugin)(nil)

// RegisterDiagnoseTools implements plugins.Diagnosable for RedisPlugin.
// It registers read-only diagnostic tools and the accessor factory.
func (p *RedisPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("redis", "redis", "Redis diagnostic tools (INFO, SLOWLOG, CLIENT LIST, CONFIG GET, DBSIZE)", diagnose.ToolScopeRemote)

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
		Description: "List all connected Redis clients (CLIENT LIST)",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
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
