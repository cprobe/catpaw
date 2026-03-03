package ntp

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"runtime"
	"strings"
	"time"

	"os/exec"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/pkg/cmdx"
	"github.com/cprobe/catpaw/plugins"
)

var _ plugins.Diagnosable = (*NTPPlugin)(nil)

const diagnoseTimeout = 10 * time.Second

func (p *NTPPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("ntp", "ntp",
		"NTP diagnostic tools (sync status, offset, stratum). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("ntp", diagnose.DiagnoseTool{
		Name:        "ntp_status",
		Description: "Show NTP synchronization status, offset, stratum, and source. Auto-detects chrony/ntpd/timedatectl.",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execNtpStatus,
	})
}

func execNtpStatus(ctx context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("ntp_status requires linux (current: %s)", runtime.GOOS)
	}

	mode, bin, err := autoDetect()
	if err != nil {
		return "", err
	}

	result, err := queryNtp(ctx, mode, bin)
	if err != nil {
		return "", fmt.Errorf("NTP query failed (%s): %w", mode, err)
	}

	return formatNtpResult(mode, result), nil
}

func queryNtp(ctx context.Context, mode, bin string) (*ntpResult, error) {
	var args []string
	switch mode {
	case modeChrony:
		args = []string{"-n", "tracking"}
	case modeNtpd:
		args = []string{"-pn"}
	case modeTimedatectl:
		args = []string{"show"}
	default:
		return nil, fmt.Errorf("unknown mode: %s", mode)
	}

	timeout := diagnoseTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return nil, fmt.Errorf("%s timed out after %s", bin, timeout)
	}
	if runErr != nil {
		return nil, fmt.Errorf("%s failed: %v (stderr: %s)", bin, runErr, strings.TrimSpace(stderr.String()))
	}

	switch mode {
	case modeChrony:
		return parseChronyTracking(stdout.Bytes())
	case modeNtpd:
		return parseNtpqOutput(stdout.Bytes())
	case modeTimedatectl:
		return parseTimedatectl(stdout.Bytes())
	}
	return nil, fmt.Errorf("unreachable")
}

func formatNtpResult(mode string, r *ntpResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mode:       %s\n", mode)
	if r.synced {
		b.WriteString("Synced:     yes\n")
	} else {
		b.WriteString("Synced:     NO\n")
	}
	if r.source != "" {
		fmt.Fprintf(&b, "Source:     %s\n", r.source)
	}
	if r.stratum > 0 {
		fmt.Fprintf(&b, "Stratum:    %d\n", r.stratum)
	}
	if r.offset != 0 {
		fmt.Fprintf(&b, "Offset:     %s (abs: %s)\n", r.offset, time.Duration(math.Abs(float64(r.offset))))
	}
	if len(r.extra) > 0 {
		b.WriteString("\nDetails:\n")
		for k, v := range r.extra {
			fmt.Fprintf(&b, "  %-20s %s\n", k+":", v)
		}
	}
	return b.String()
}
