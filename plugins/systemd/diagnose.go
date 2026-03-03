package systemd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/pkg/cmdx"
	"github.com/cprobe/catpaw/plugins"
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
		return "", fmt.Errorf("systemctl show failed: %v (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))
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
