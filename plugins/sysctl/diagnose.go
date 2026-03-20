package sysctl

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/plugins"
)

var _ plugins.Diagnosable = (*SysctlPlugin)(nil)

var defaultKeys = []string{
	"vm.swappiness",
	"vm.dirty_ratio",
	"vm.dirty_background_ratio",
	"vm.overcommit_memory",
	"vm.overcommit_ratio",
	"vm.panic_on_oom",
	"vm.max_map_count",
	"fs.file-max",
	"fs.nr_open",
	"net.core.somaxconn",
	"net.core.netdev_max_backlog",
	"net.core.rmem_max",
	"net.core.wmem_max",
	"net.ipv4.tcp_max_syn_backlog",
	"net.ipv4.tcp_syncookies",
	"net.ipv4.tcp_tw_reuse",
	"net.ipv4.tcp_fin_timeout",
	"net.ipv4.tcp_keepalive_time",
	"net.ipv4.tcp_keepalive_intvl",
	"net.ipv4.tcp_keepalive_probes",
	"net.ipv4.ip_local_port_range",
	"net.ipv4.tcp_rmem",
	"net.ipv4.tcp_wmem",
	"kernel.pid_max",
	"kernel.threads-max",
}

func (p *SysctlPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysctl", "sysctl",
		"Sysctl diagnostic tools (kernel parameter snapshot). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysctl", diagnose.DiagnoseTool{
		Name:        "sysctl_snapshot",
		Description: "Show key kernel parameters. Without arguments, shows a predefined set of important parameters.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "keys", Type: "string", Description: "Comma-separated sysctl keys to read (e.g. 'vm.swappiness,fs.file-max'). If empty, shows default set."},
		},
		Execute: execSysctlSnapshot,
	})

	registry.Register("sysctl", diagnose.DiagnoseTool{
		Name:        "sysctl_get",
		Description: "Read a single sysctl parameter value",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "key", Type: "string", Description: "Sysctl key (e.g. 'net.ipv4.tcp_syncookies')", Required: true},
		},
		Execute: execSysctlGet,
	})
}

func execSysctlSnapshot(_ context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("sysctl_snapshot requires linux (current: %s)", runtime.GOOS)
	}

	keys := defaultKeys
	if raw := args["keys"]; raw != "" {
		keys = nil
		for _, k := range strings.Split(raw, ",") {
			k = strings.TrimSpace(k)
			if k != "" {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			return "", fmt.Errorf("keys parameter is empty after parsing")
		}
		const maxKeys = 100
		if len(keys) > maxKeys {
			return "", fmt.Errorf("too many keys (%d, max %d)", len(keys), maxKeys)
		}
	}

	type kv struct {
		key, val string
	}
	var results []kv
	maxKeyLen := 0

	for _, key := range keys {
		if err := validateKey(key); err != nil {
			results = append(results, kv{key, fmt.Sprintf("[error] %v", err)})
			continue
		}
		val := readSysctl(key)
		results = append(results, kv{key, val})
		if len(key) > maxKeyLen {
			maxKeyLen = len(key)
		}
	}

	var b strings.Builder
	fmtStr := fmt.Sprintf("%%-%ds = %%s\n", maxKeyLen)
	for _, r := range results {
		fmt.Fprintf(&b, fmtStr, r.key, r.val)
	}
	return b.String(), nil
}

func execSysctlGet(_ context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("sysctl_get requires linux (current: %s)", runtime.GOOS)
	}

	key := strings.TrimSpace(args["key"])
	if key == "" {
		return "", fmt.Errorf("key parameter is required")
	}
	if err := validateKey(key); err != nil {
		return "", err
	}

	path := keyToPath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("%s: parameter not found", key), nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return fmt.Sprintf("%s = %s", key, normalizeWhitespace(strings.TrimSpace(string(data)))), nil
}

func readSysctl(key string) string {
	data, err := os.ReadFile(keyToPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return "[not found]"
		}
		return fmt.Sprintf("[error] %v", err)
	}
	return normalizeWhitespace(strings.TrimSpace(string(data)))
}

