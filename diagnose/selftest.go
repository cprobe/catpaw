package diagnose

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// SelfTestResult holds the outcome of testing one tool.
type SelfTestResult struct {
	Category string
	Tool     string
	Status   string // PASS, FAIL, SKIP, WARN
	Duration time.Duration
	OutSize  int
	Message  string
	Args     map[string]string
}

// safeDefaults maps common parameter names to safe values for selftest.
var safeDefaults = map[string]string{
	"pid":       "1",
	"host":      "localhost",
	"path":      "/",
	"since":     "1h",
	"delay":     "1",
	"lines":     "5",
	"pattern":   "error",
	"table":     "filter",
	"interface": "",
	"port":      "",
	"max":       "10",
	"top":       "5",
}

// RunSelfTest executes all local tools in the registry and reports results.
// Returns a non-nil error if any tool FAILs, so the caller can decide the exit code.
func RunSelfTest(registry *ToolRegistry, filter string, verbose bool) error {
	categories := registry.CategoriesWithTools()

	hostname, _ := os.Hostname()
	kernel := ""
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/version"); err == nil {
			fields := strings.Fields(string(data))
			if len(fields) >= 3 {
				kernel = fields[2]
			}
		}
	}

	totalTools := registry.ToolCount()
	fmt.Printf("catpaw selftest — %d tools registered, %s/%s", totalTools, runtime.GOOS, runtime.GOARCH)
	if kernel != "" {
		fmt.Printf(", kernel %s", kernel)
	}
	if hostname != "" {
		fmt.Printf(", host %s", hostname)
	}
	fmt.Println()
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println()

	var results []SelfTestResult
	passCount, failCount, skipCount, warnCount := 0, 0, 0, 0

	timeout := 30 * time.Second

	for _, cat := range categories {
		if filter != "" && !strings.Contains(cat.Name, filter) && !strings.Contains(cat.Plugin, filter) {
			continue
		}

		catHeader := false

		for _, tool := range cat.Tools {
			if tool.Scope == ToolScopeRemote {
				r := SelfTestResult{
					Category: cat.Name,
					Tool:     tool.Name,
					Status:   "SKIP",
					Message:  "remote tool (requires connection)",
				}
				results = append(results, r)
				skipCount++
				if verbose {
					printCatHeader(&catHeader, cat)
					printResult(r)
				}
				continue
			}

			if tool.Execute == nil {
				r := SelfTestResult{
					Category: cat.Name,
					Tool:     tool.Name,
					Status:   "SKIP",
					Message:  "no Execute function",
				}
				results = append(results, r)
				skipCount++
				if verbose {
					printCatHeader(&catHeader, cat)
					printResult(r)
				}
				continue
			}

			args, skipped := buildSafeArgs(tool)
			if skipped != "" {
				r := SelfTestResult{
					Category: cat.Name,
					Tool:     tool.Name,
					Status:   "SKIP",
					Message:  skipped,
				}
				results = append(results, r)
				skipCount++
				if verbose {
					printCatHeader(&catHeader, cat)
					printResult(r)
				}
				continue
			}

			r := runOneTool(cat.Name, tool, args, timeout)
			results = append(results, r)

			switch r.Status {
			case "PASS":
				passCount++
			case "FAIL":
				failCount++
			case "WARN":
				warnCount++
			case "SKIP":
				skipCount++
			}

			printCatHeader(&catHeader, cat)
			printResult(r)
		}

		if catHeader {
			fmt.Println()
		}
	}

	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("Summary: %d PASS, %d SKIP, %d WARN, %d FAIL (total %d)\n",
		passCount, skipCount, warnCount, failCount, len(results))

	if failCount > 0 {
		fmt.Println()
		fmt.Println("FAIL details:")
		for _, r := range results {
			if r.Status == "FAIL" {
				fmt.Printf("  [FAIL] %-28s %s\n", r.Tool, r.Message)
			}
		}
	}

	if warnCount > 0 {
		fmt.Println()
		fmt.Println("WARN details:")
		for _, r := range results {
			if r.Status == "WARN" {
				hint := installHint(r.Message)
				if hint != "" {
					fmt.Printf("  [WARN] %-28s %s\n         %s\n", r.Tool, r.Message, hint)
				} else {
					fmt.Printf("  [WARN] %-28s %s\n", r.Tool, r.Message)
				}
			}
		}
	}

	if failCount > 0 {
		return fmt.Errorf("%d tool(s) failed", failCount)
	}
	return nil
}

func printCatHeader(printed *bool, cat ToolCategory) {
	if *printed {
		return
	}
	*printed = true
	desc := cat.Description
	if desc == "" {
		desc = cat.Name
	}
	fmt.Printf("%s (%d tools)\n", cat.Name, len(cat.Tools))
}

func printResult(r SelfTestResult) {
	tag := colorStatus(r.Status)
	switch r.Status {
	case "PASS":
		argsStr := ""
		if len(r.Args) > 0 {
			argsStr = " " + formatTestArgs(r.Args)
		}
		fmt.Printf("  %s %-28s %8s %6s%s\n",
			tag, r.Tool, formatDuration(r.Duration), formatBytes(r.OutSize), argsStr)
	case "SKIP":
		fmt.Printf("  %s %-28s %s\n", tag, r.Tool, r.Message)
	default:
		fmt.Printf("  %s %-28s %8s %s\n", tag, r.Tool, formatDuration(r.Duration), r.Message)
	}
}

func colorStatus(status string) string {
	switch status {
	case "PASS":
		return "\033[32m[PASS]\033[0m"
	case "FAIL":
		return "\033[31m[FAIL]\033[0m"
	case "WARN":
		return "\033[33m[WARN]\033[0m"
	case "SKIP":
		return "\033[90m[SKIP]\033[0m"
	default:
		return "[" + status + "]"
	}
}

func buildSafeArgs(tool DiagnoseTool) (map[string]string, string) {
	args := make(map[string]string)
	for _, p := range tool.Parameters {
		if !p.Required {
			if v, ok := safeDefaults[p.Name]; ok && v != "" {
				args[p.Name] = v
			}
			continue
		}

		v, ok := safeDefaults[p.Name]
		if !ok || v == "" {
			return nil, fmt.Sprintf("requires '%s' parameter (no safe default)", p.Name)
		}
		args[p.Name] = v
	}
	return args, ""
}

func runOneTool(category string, tool DiagnoseTool, args map[string]string, timeout time.Duration) SelfTestResult {
	r := SelfTestResult{
		Category: category,
		Tool:     tool.Name,
		Args:     args,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()

	// Catch panics
	var output string
	var execErr error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				execErr = fmt.Errorf("PANIC: %v", rec)
			}
		}()
		output, execErr = tool.Execute(ctx, args)
	}()

	r.Duration = time.Since(start)
	r.OutSize = len(output)

	if execErr != nil {
		errMsg := execErr.Error()

		if isExpectedPlatformError(errMsg) {
			r.Status = "SKIP"
			r.Message = errMsg
			return r
		}

		if isCommandNotFound(errMsg) {
			r.Status = "WARN"
			r.Message = errMsg
			return r
		}

		r.Status = "FAIL"
		r.Message = truncMessage(errMsg, 120)
		return r
	}

	if output == "" {
		r.Status = "WARN"
		r.Message = "empty output"
		return r
	}

	if !utf8.ValidString(output) {
		r.Status = "FAIL"
		r.Message = "output contains invalid UTF-8"
		return r
	}

	if r.Duration > 15*time.Second {
		r.Status = "WARN"
		r.Message = fmt.Sprintf("slow execution (%s)", formatDuration(r.Duration))
		return r
	}

	r.Status = "PASS"
	return r
}

func isExpectedPlatformError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "requires linux") ||
		strings.Contains(lower, "linux only") ||
		strings.Contains(lower, "not supported on")
}

func isCommandNotFound(msg string) bool {
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "command not found") ||
		strings.Contains(msg, "no such file or directory")
}

func truncMessage(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return TruncateUTF8(s, maxBytes-3) + "..."
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.0fµs", float64(d.Microseconds()))
	}
	if d < time.Second {
		return fmt.Sprintf("%.0fms", float64(d.Milliseconds()))
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func formatBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}

// installHint extracts the missing command from the error message and
// suggests install commands for common package managers.
func installHint(msg string) string {
	cmd := extractMissingCommand(msg)
	if cmd == "" {
		return ""
	}

	pkg := commandToPackage(cmd)
	return fmt.Sprintf("fix: apt install %s  OR  yum install %s  OR  dnf install %s", pkg, pkg, pkg)
}

func extractMissingCommand(msg string) string {
	// Pattern: exec: "traceroute": executable file not found in $PATH
	if i := strings.Index(msg, `exec: "`); i >= 0 {
		rest := msg[i+7:]
		if j := strings.IndexByte(rest, '"'); j > 0 {
			return rest[:j]
		}
	}
	// Pattern: traceroute not found
	if i := strings.Index(msg, " not found"); i > 0 {
		parts := strings.Fields(msg[:i])
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return ""
}

var commandPackageMap = map[string]string{
	"traceroute":  "traceroute",
	"ss":          "iproute2",
	"ip":          "iproute2",
	"nft":         "nftables",
	"iptables":    "iptables",
	"vgs":         "lvm2",
	"lvs":         "lvm2",
	"getenforce":  "libselinux-utils",
	"ausearch":    "auditd",
	"aa-status":   "apparmor-utils",
	"coredumpctl": "systemd-coredump",
	"lsblk":       "util-linux",
	"dmesg":       "util-linux",
}

func commandToPackage(cmd string) string {
	if pkg, ok := commandPackageMap[cmd]; ok {
		return pkg
	}
	return cmd
}

func formatTestArgs(args map[string]string) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, k+"="+v)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
