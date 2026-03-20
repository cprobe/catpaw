package systemd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
	
	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/cmdx"
	"github.com/cprobe/catpaw/digcore/plugins"
)

var _ plugins.Diagnosable = (*SystemdPlugin)(nil)

const diagnoseTimeout = 10 * time.Second

func (p *SystemdPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("systemd", "systemd",
		"Systemd diagnostic tools (service status, failed units). Linux only.",
		diagnose.ToolScopeLocal)
	
	registry.Register("systemd", diagnose.DiagnoseTool{
		Name:        "service_status",
		Description: "Show the status of a systemd unit (active state, sub state, PID, description, restarts)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "unit", Type: "string", Description: "Unit name (e.g. 'nginx', 'sshd.service')", Required: true},
		},
		Execute: execServiceStatus,
	})
	
	registry.Register("systemd", diagnose.DiagnoseTool{
		Name:        "service_list_failed",
		Description: "List all systemd units in failed state",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execServiceListFailed,
	})
	
	registerJournalQuery(registry)
	registerTimers(registry)
}

func execServiceStatus(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("service_status requires linux (current: %s)", runtime.GOOS)
	}
	
	unit := strings.TrimSpace(args["unit"])
	if unit == "" {
		return "", fmt.Errorf("unit parameter is required")
	}
	if strings.ContainsAny(unit, "\n\r\t;|&$`") || len(unit) > 256 {
		return "", fmt.Errorf("invalid unit name %q", unit)
	}
	unit = normalizeUnitName(unit)
	
	bin, err := exec.LookPath("systemctl")
	if err != nil {
		return "", fmt.Errorf("systemctl not found: %w", err)
	}
	
	cmd := exec.Command(bin, "show", unit, "--property="+strings.Join(queryProperties, ","))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	
	timeout := effectiveTimeout(ctx, diagnoseTimeout)
	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("systemctl show timed out after %s", diagnoseTimeout)
	}
	
	if runErr != nil {
		var exitErr *exec.ExitError
		// 检查错误类型是否为 ExitError，以及退出状态码是否为 1
		isExitCode1 := errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1
		
		// 针对 CentOS 7 等老系统的兼容处理：
		// 如果是退出码 1 (意味着存在不支持的属性查询)，但标准输出(stdout)里依然有获取到的数据，
		// 我们选择容忍该错误，继续解析。
		if isExitCode1 && stdout.Len() > 0 {
			// 可以选择在这里添加一行日志记录 (如果你有 logger 的话)
			// log.Warnf("systemctl show returned exit code 1, but output was captured. Proceeding...")
		} else {
			// 如果是其他致命错误，或者标准输出根本没有数据，则真正报错
			return "", fmt.Errorf("systemctl show failed: %v (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))
		}
	}
	
	props := parseProperties(stdout.Bytes())
	return formatUnitProps(unit, props), nil
}

func execServiceListFailed(ctx context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("service_list_failed requires linux (current: %s)", runtime.GOOS)
	}
	
	bin, err := exec.LookPath("systemctl")
	if err != nil {
		return "", fmt.Errorf("systemctl not found: %w", err)
	}
	
	cmd := exec.Command(bin, "list-units", "--state=failed", "--no-pager", "--no-legend", "--plain")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	
	timeout := effectiveTimeout(ctx, diagnoseTimeout)
	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("systemctl list-units timed out after %s", diagnoseTimeout)
	}
	if runErr != nil {
		return "", fmt.Errorf("systemctl list-units failed: %v (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))
	}
	
	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return "No failed units.", nil
	}
	
	lines := strings.Split(output, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "Failed units: %d\n\n", len(lines))
	for _, line := range lines {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func effectiveTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < fallback {
			return remaining
		}
	}
	return fallback
}

func formatUnitProps(unit string, props map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unit:        %s\n", unit)
	
	fields := []struct{ label, key string }{
		{"Description", "Description"},
		{"LoadState", "LoadState"},
		{"ActiveState", "ActiveState"},
		{"SubState", "SubState"},
		{"Type", "Type"},
		{"MainPID", "MainPID"},
		{"NRestarts", "NRestarts"},
		{"Result", "Result"},
		{"UnitFile", "UnitFileState"},
		{"Fragment", "FragmentPath"},
		{"ActiveSince", "ActiveEnterTimestamp"},
	}
	
	for _, f := range fields {
		v := props[f.key]
		if v == "" || (v == "0" && (f.key == "MainPID" || f.key == "NRestarts")) {
			continue
		}
		fmt.Fprintf(&b, "%-13s%s\n", f.label+":", v)
	}
	return b.String()
}
