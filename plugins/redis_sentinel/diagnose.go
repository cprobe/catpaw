package redis_sentinel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/plugins"
)

var _ plugins.Diagnosable = (*RedisSentinelPlugin)(nil)

func (p *RedisSentinelPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("redis_sentinel", "redis_sentinel", "Redis Sentinel diagnostic tools (overview, master health, replicas, peers, info)", diagnose.ToolScopeRemote)

	registry.Register("redis_sentinel", diagnose.DiagnoseTool{
		Name:        "sentinel_overview",
		Description: "Show Sentinel target overview. Returns ROLE and a compact summary of SENTINEL MASTERS. Use first when the alert does not yet identify a specific master.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			role, err := acc.Role()
			if err != nil {
				return "", fmt.Errorf("ROLE: %w", err)
			}
			masters, err := acc.SentinelMasters()
			if err != nil {
				return "", fmt.Errorf("SENTINEL MASTERS: %w", err)
			}
			return formatOverview(role, masters), nil
		},
	})

	registry.Register("redis_sentinel", diagnose.DiagnoseTool{
		Name:        "sentinel_master_health",
		Description: "Show one master's Sentinel health in one call. Combines SENTINEL MASTER, CKQUORUM, GET-MASTER-ADDR-BY-NAME, and compact replica/peer counts. Use first for quorum, down-state, and address-resolution alerts.",
		Parameters: []diagnose.ToolParam{
			{Name: "master", Type: "string", Description: "Sentinel master name", Required: true},
		},
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			masterName := strings.TrimSpace(args["master"])
			if masterName == "" {
				return "", fmt.Errorf("master is required")
			}
			master, masterErr := acc.SentinelMaster(masterName)
			ckquorum, ckErr := acc.SentinelCKQuorum(masterName)
			addr, addrErr := acc.SentinelGetMasterAddrByName(masterName)
			replicas, replicasErr := acc.SentinelReplicas(masterName)
			sentinels, sentinelsErr := acc.SentinelSentinels(masterName)

			var b strings.Builder
			fmt.Fprintf(&b, "[MASTER HEALTH]\n")
			if masterErr != nil {
				fmt.Fprintf(&b, "master: error: %v\n", masterErr)
			} else {
				fmt.Fprintf(&b, "name: %s\n", masterName)
				for _, key := range []string{"status", "flags", "ip", "port", "num-slaves", "num-other-sentinels", "quorum"} {
					if v := strings.TrimSpace(master[key]); v != "" {
						fmt.Fprintf(&b, "%s: %s\n", key, v)
					}
				}
			}

			fmt.Fprintf(&b, "\n[CKQUORUM]\n")
			if ckErr != nil {
				fmt.Fprintf(&b, "error: %v\n", ckErr)
			} else {
				fmt.Fprintf(&b, "%s\n", ckquorum)
			}

			fmt.Fprintf(&b, "\n[MASTER ADDR]\n")
			if addrErr != nil {
				fmt.Fprintf(&b, "error: %v\n", addrErr)
			} else {
				fmt.Fprintf(&b, "%s\n", addr)
			}

			fmt.Fprintf(&b, "\n[TOPOLOGY COUNTS]\n")
			if replicasErr != nil {
				fmt.Fprintf(&b, "replicas_error: %v\n", replicasErr)
			} else {
				fmt.Fprintf(&b, "replicas: %d\n", len(replicas))
			}
			if sentinelsErr != nil {
				fmt.Fprintf(&b, "sentinels_error: %v\n", sentinelsErr)
			} else {
				fmt.Fprintf(&b, "sentinels: %d\n", len(sentinels))
			}

			if masterErr != nil && ckErr != nil && addrErr != nil {
				return "", fmt.Errorf("failed to query master health for %s", masterName)
			}
			return b.String(), nil
		},
	})

	registry.Register("redis_sentinel", diagnose.DiagnoseTool{
		Name:        "sentinel_replicas",
		Description: "List detailed replica records for one Sentinel master using SENTINEL REPLICAS. Use when replica visibility or replica topology needs drill-down.",
		Parameters: []diagnose.ToolParam{
			{Name: "master", Type: "string", Description: "Sentinel master name", Required: true},
		},
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			masterName := strings.TrimSpace(args["master"])
			if masterName == "" {
				return "", fmt.Errorf("master is required")
			}
			replicas, err := acc.SentinelReplicas(masterName)
			if err != nil {
				return "", fmt.Errorf("SENTINEL REPLICAS %s: %w", masterName, err)
			}
			return formatList("REPLICAS", replicas), nil
		},
	})

	registry.Register("redis_sentinel", diagnose.DiagnoseTool{
		Name:        "sentinel_sentinels",
		Description: "List detailed peer Sentinel records for one master using SENTINEL SENTINELS. Use for quorum issues or view disagreement drill-down.",
		Parameters: []diagnose.ToolParam{
			{Name: "master", Type: "string", Description: "Sentinel master name", Required: true},
		},
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			masterName := strings.TrimSpace(args["master"])
			if masterName == "" {
				return "", fmt.Errorf("master is required")
			}
			peers, err := acc.SentinelSentinels(masterName)
			if err != nil {
				return "", fmt.Errorf("SENTINEL SENTINELS %s: %w", masterName, err)
			}
			return formatList("SENTINELS", peers), nil
		},
	})

	registry.Register("redis_sentinel", diagnose.DiagnoseTool{
		Name:        "sentinel_info",
		Description: "Fetch Sentinel INFO output. Use as an expert fallback when overview or master health is insufficient. Optional section parameter; default is all.",
		Parameters: []diagnose.ToolParam{
			{Name: "section", Type: "string", Description: "INFO section, default all", Required: false},
		},
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getAccessor(session)
			if err != nil {
				return "", err
			}
			section := strings.TrimSpace(args["section"])
			raw, err := acc.InfoRaw(section)
			if err != nil {
				if section == "" {
					return "", fmt.Errorf("INFO: %w", err)
				}
				return "", fmt.Errorf("INFO %s: %w", section, err)
			}
			return raw, nil
		},
	})

	registry.RegisterAccessorFactory("redis_sentinel", func(ctx context.Context, instanceRef any, target string) (any, error) {
		ins, ok := instanceRef.(*Instance)
		if !ok {
			return nil, fmt.Errorf("redis_sentinel accessor factory: expected *Instance, got %T", instanceRef)
		}
		if target == "" && len(ins.Targets) > 0 {
			target = ins.Targets[0]
		}
		return NewSentinelAccessor(SentinelAccessorConfig{
			Target:      target,
			Username:    ins.Username,
			Password:    ins.Password,
			Timeout:     time.Duration(ins.Timeout),
			ReadTimeout: time.Duration(ins.ReadTimeout),
			TLSConfig:   ins.tlsConfig,
			DialFunc:    ins.dialFunc,
		})
	})

	registry.RegisterPreCollector("redis_sentinel", func(ctx context.Context, accessor any) string {
		acc, ok := accessor.(*SentinelAccessor)
		if !ok {
			return ""
		}
		role, err := acc.Role()
		if err != nil {
			return ""
		}
		masters, err := acc.SentinelMasters()
		if err != nil {
			return fmt.Sprintf("[ROLE]\n%s\n", role)
		}
		return formatOverview(role, masters)
	})

	registry.SetDiagnoseHints("redis_sentinel", `
- 通用 Sentinel 告警 -> 先调 sentinel_overview
- quorum 告警 -> 先调 sentinel_master_health；若仍需看 peer 视图，再调 sentinel_sentinels
- master down 告警 -> 先调 sentinel_master_health
- replica 可见性问题 -> 先调 sentinel_master_health；若仍需看 replica 明细，再调 sentinel_replicas
- 视图不一致 / topology disagreement -> 先调 sentinel_overview，再按 master 调 sentinel_sentinels
- 首轮优先一个 overview 工具或一个 master_health 工具，避免把多个命令型调用拆散`)
}

func getAccessor(session *diagnose.DiagnoseSession) (*SentinelAccessor, error) {
	if session.Accessor == nil {
		return nil, fmt.Errorf("no redis_sentinel accessor in session (remote connection not established)")
	}
	acc, ok := session.Accessor.(*SentinelAccessor)
	if !ok {
		return nil, fmt.Errorf("session accessor is %T, expected *SentinelAccessor", session.Accessor)
	}
	return acc, nil
}

func formatOverview(role string, masters []SentinelMasterInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[ROLE]\n%s\n\n", role)
	fmt.Fprintf(&b, "[MASTERS SUMMARY]\ncount: %d\n", len(masters))
	for _, master := range masters {
		name := master["name"]
		flags := master["flags"]
		addr := joinHostPort(master["ip"], master["port"])
		quorum := master["quorum"]
		fmt.Fprintf(&b, "- %s", name)
		if addr != "" {
			fmt.Fprintf(&b, " addr=%s", addr)
		}
		if flags != "" {
			fmt.Fprintf(&b, " flags=%s", flags)
		}
		if quorum != "" {
			fmt.Fprintf(&b, " quorum=%s", quorum)
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}

func formatList(title string, items []SentinelMasterInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\ncount: %d\n", title, len(items))
	for i, item := range items {
		fmt.Fprintf(&b, "%d)\n", i+1)
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", key, item[key])
		}
	}
	return b.String()
}

func joinHostPort(host, port string) string {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" || port == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", host, port)
}
