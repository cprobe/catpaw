package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/cprobe/digcore/diagnose"
)

const (
	envMaxRead   = 128 * 1024 // /proc/<pid>/environ can be large for some processes
	maxEnvOutput = 200        // max environment variables to display
)

// Substrings in env var names that indicate sensitive values.
var sensitiveKeys = []string{
	"password", "passwd", "secret", "token", "key", "credential",
	"auth", "private", "api_key", "apikey", "access_key", "accesskey",
}

func registerEnvInspect(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_proc", "sysdiag:proc",
		"Process diagnostic tools (environment, open files). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_proc", diagnose.DiagnoseTool{
		Name:        "env_inspect",
		Description: "Show environment variables of a process. Sensitive values (password, token, key, etc) are masked. Requires root or same UID.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "pid", Type: "string", Description: "Process ID (required)", Required: true},
			{Name: "filter", Type: "string", Description: "Only show variables containing this substring (case-insensitive)"},
		},
		Execute: execEnvInspect,
	})
}

func execEnvInspect(_ context.Context, args map[string]string) (string, error) {
	pidStr := strings.TrimSpace(args["pid"])
	if pidStr == "" {
		return "", fmt.Errorf("pid parameter is required")
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return "", fmt.Errorf("invalid pid %q: must be a positive integer", pidStr)
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("env_inspect requires linux (current: %s)", runtime.GOOS)
	}

	filter := strings.ToLower(strings.TrimSpace(args["filter"]))

	envVars, err := readProcEnviron(pid)
	if err != nil {
		return "", err
	}

	if filter != "" {
		envVars = filterEnvVars(envVars, filter)
	}

	return formatEnvVars(pid, envVars, filter), nil
}

func readProcEnviron(pid int) ([]string, error) {
	path := fmt.Sprintf("/proc/%d/environ", pid)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("process %d not found", pid)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("permission denied reading environment of process %d (requires root or same UID)", pid)
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, envMaxRead))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	// /proc/<pid>/environ is NUL-separated
	raw := strings.Split(string(data), "\x00")
	vars := make([]string, 0, len(raw))
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v != "" {
			vars = append(vars, v)
		}
	}
	sort.Strings(vars)
	return vars, nil
}

func filterEnvVars(vars []string, filter string) []string {
	result := make([]string, 0, len(vars)/4)
	for _, v := range vars {
		if strings.Contains(strings.ToLower(v), filter) {
			result = append(result, v)
		}
	}
	return result
}

func formatEnvVars(pid int, vars []string, filter string) string {
	if len(vars) == 0 {
		if filter != "" {
			return fmt.Sprintf("No environment variables matching %q for process %d.", filter, pid)
		}
		return fmt.Sprintf("No environment variables found for process %d (or empty environ).", pid)
	}

	var b strings.Builder
	total := len(vars)
	if total > maxEnvOutput {
		vars = vars[:maxEnvOutput]
	}

	fmt.Fprintf(&b, "Environment for PID %d: %d variables", pid, total)
	if filter != "" {
		fmt.Fprintf(&b, " (filter: %q)", filter)
	}
	if total > maxEnvOutput {
		fmt.Fprintf(&b, " (showing first %d)", maxEnvOutput)
	}
	b.WriteString("\n\n")

	for _, v := range vars {
		masked := maskSensitive(v)
		b.WriteString(masked)
		b.WriteByte('\n')
	}
	return b.String()
}

// maskSensitive replaces the value of env vars whose name contains sensitive keywords.
func maskSensitive(envLine string) string {
	idx := strings.IndexByte(envLine, '=')
	if idx < 0 {
		return envLine
	}
	name := envLine[:idx]
	nameLower := strings.ToLower(name)
	for _, kw := range sensitiveKeys {
		if strings.Contains(nameLower, kw) {
			return name + "=***"
		}
	}
	return envLine
}
