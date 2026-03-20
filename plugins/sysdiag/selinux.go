package sysdiag

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/cmdx"
)

const (
	selinuxTimeout  = 10 * time.Second
	selinuxMaxRead  = 256 * 1024
	selinuxModePath = "/sys/fs/selinux/enforce"
	apparmorPath    = "/sys/module/apparmor/parameters/enabled"
)

func registerSELinux(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_mac", "sysdiag:mac",
		"Mandatory Access Control diagnostics (SELinux/AppArmor status and recent denials). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_mac", diagnose.DiagnoseTool{
		Name:        "selinux_status",
		Description: "Show SELinux or AppArmor status and recent access denials. Auto-detects active MAC system.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "max_denials", Type: "string", Description: "Maximum denial entries to show (default: 20, max: 100)"},
		},
		Execute: execSELinuxStatus,
	})
}

type macSystem int

const (
	macNone macSystem = iota
	macSELinux
	macAppArmor
)

func execSELinuxStatus(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("selinux_status requires linux (current: %s)", runtime.GOOS)
	}

	maxDenials := 20
	if v := strings.TrimSpace(args["max_denials"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return "", fmt.Errorf("max_denials must be a non-negative integer")
		}
		if n > 100 {
			return "", fmt.Errorf("max_denials must be <= 100")
		}
		if n == 0 {
			n = 20
		}
		maxDenials = n
	}

	sys := detectMAC()

	var b strings.Builder
	switch sys {
	case macSELinux:
		formatSELinux(ctx, &b, maxDenials)
	case macAppArmor:
		formatAppArmor(ctx, &b, maxDenials)
	default:
		b.WriteString("No Mandatory Access Control system detected.\n")
		b.WriteString("Neither SELinux nor AppArmor appears to be active.\n")
	}

	return b.String(), nil
}

func detectMAC() macSystem {
	if _, err := os.Stat(selinuxModePath); err == nil {
		return macSELinux
	}
	if data, err := readSmallFile(apparmorPath); err == nil {
		if strings.TrimSpace(string(data)) == "Y" {
			return macAppArmor
		}
	}
	if _, err := exec.LookPath("getenforce"); err == nil {
		return macSELinux
	}
	if _, err := exec.LookPath("aa-status"); err == nil {
		return macAppArmor
	}
	return macNone
}

func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 256))
}

func formatSELinux(ctx context.Context, b *strings.Builder, maxDenials int) {
	b.WriteString("Security Module: SELinux\n")
	b.WriteString(strings.Repeat("=", 40))
	b.WriteString("\n\n")

	mode := readSELinuxMode(ctx)
	fmt.Fprintf(b, "Mode: %s\n", mode)

	if strings.EqualFold(mode, "Disabled") {
		b.WriteString("\nSELinux is disabled. No denial data available.\n")
		return
	}

	denials := getSELinuxDenials(ctx, maxDenials)
	b.WriteString("\n")
	if len(denials) == 0 {
		b.WriteString("Recent denials: none found\n")
	} else {
		fmt.Fprintf(b, "Recent denials (last %d):\n", len(denials))
		for _, d := range denials {
			fmt.Fprintf(b, "  %s\n", d)
		}
	}
}

func readSELinuxMode(ctx context.Context) string {
	if data, err := readSmallFile(selinuxModePath); err == nil {
		switch strings.TrimSpace(string(data)) {
		case "1":
			return "Enforcing"
		case "0":
			return "Permissive"
		}
	}

	if ge, err := exec.LookPath("getenforce"); err == nil {
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, ge)
		cmd.Stdout = &out
		if err, _ := cmdx.RunTimeout(cmd, selinuxTimeout); err == nil {
			return strings.TrimSpace(out.String())
		}
	}

	return "Unknown"
}

func getSELinuxDenials(ctx context.Context, max int) []string {
	if aus, err := exec.LookPath("ausearch"); err == nil {
		var outBuf cappedBuf
		outBuf.buf = bytes.NewBuffer(make([]byte, 0, 4096))
		outBuf.max = selinuxMaxRead

		cmd := exec.CommandContext(ctx, aus, "-m", "avc", "--start", "recent", "-i")
		cmd.Stdout = &outBuf

		if err, _ := cmdx.RunTimeout(cmd, selinuxTimeout); err == nil {
			return extractDenialLines(outBuf.buf.String(), max)
		}
	}

	if data, err := readAuditLogTail(); err == nil {
		return extractDenialLines(string(data), max)
	}

	return nil
}

func readAuditLogTail() ([]byte, error) {
	f, err := os.Open("/var/log/audit/audit.log")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	readSize := int64(selinuxMaxRead)
	if info.Size() > readSize {
		if _, err := f.Seek(-readSize, io.SeekEnd); err != nil {
			return nil, err
		}
	}

	return io.ReadAll(io.LimitReader(f, readSize))
}

func extractDenialLines(text string, max int) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "denied") || strings.Contains(line, "avc:") {
			clean := strings.TrimSpace(line)
			if clean != "" {
				lines = append(lines, clean)
			}
		}
	}

	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
}

func formatAppArmor(ctx context.Context, b *strings.Builder, maxDenials int) {
	b.WriteString("Security Module: AppArmor\n")
	b.WriteString(strings.Repeat("=", 40))
	b.WriteString("\n\n")

	status := getAppArmorStatus(ctx)
	b.WriteString(status)
	b.WriteString("\n")

	denials := getAppArmorDenials(ctx, maxDenials)
	if len(denials) == 0 {
		b.WriteString("Recent denials: none found\n")
	} else {
		fmt.Fprintf(b, "Recent denials (last %d):\n", len(denials))
		for _, d := range denials {
			fmt.Fprintf(b, "  %s\n", d)
		}
	}
}

func getAppArmorStatus(ctx context.Context) string {
	if aa, err := exec.LookPath("aa-status"); err == nil {
		var outBuf cappedBuf
		outBuf.buf = bytes.NewBuffer(make([]byte, 0, 2048))
		outBuf.max = 16 * 1024

		cmd := exec.CommandContext(ctx, aa, "--json")
		cmd.Stdout = &outBuf

		if err, _ := cmdx.RunTimeout(cmd, selinuxTimeout); err == nil {
			raw := outBuf.buf.String()
			return summarizeAppArmorJSON(raw)
		}

		outBuf.buf.Reset()
		outBuf.n = 0
		cmd2 := exec.CommandContext(ctx, aa)
		cmd2.Stdout = &outBuf
		if err, _ := cmdx.RunTimeout(cmd2, selinuxTimeout); err == nil {
			return summarizeAppArmorText(outBuf.buf.String())
		}
	}
	return "AppArmor enabled (details unavailable without aa-status)\n"
}

func summarizeAppArmorJSON(raw string) string {
	var b strings.Builder
	b.WriteString("Status: enabled\n")

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "profiles") || strings.Contains(line, "processes") {
			clean := strings.Trim(line, `",:{}`)
			if clean != "" {
				b.WriteString("  ")
				b.WriteString(clean)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func summarizeAppArmorText(raw string) string {
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "profiles") || strings.Contains(line, "processes") || strings.Contains(line, "loaded") {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	if b.Len() == 0 {
		return raw
	}
	return b.String()
}

func getAppArmorDenials(ctx context.Context, max int) []string {
	if jctl, err := exec.LookPath("journalctl"); err == nil {
		var outBuf cappedBuf
		outBuf.buf = bytes.NewBuffer(make([]byte, 0, 4096))
		outBuf.max = selinuxMaxRead

		cmd := exec.CommandContext(ctx, jctl, "-k", "--since", "1 hour ago",
			"--no-pager", "--output", "short-iso", "-g", "apparmor.*DENIED")
		cmd.Stdout = &outBuf

		if err, _ := cmdx.RunTimeout(cmd, selinuxTimeout); err == nil {
			return extractLastN(outBuf.buf.String(), max)
		}
	}
	return nil
}

func extractLastN(text string, max int) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
}
