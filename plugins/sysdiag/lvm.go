package sysdiag

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
)

const lvmTimeout = 10 * time.Second

func registerLVM(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_lvm", "sysdiag:lvm",
		"LVM diagnostic tools (volume group and logical volume status). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_lvm", diagnose.DiagnoseTool{
		Name:        "lvm_status",
		Description: "Show LVM volume groups and logical volumes with their status, size, and health. Requires 'vgs' and 'lvs' commands (lvm2 package).",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execLVMStatus,
	})
}

func execLVMStatus(ctx context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("lvm_status requires linux (current: %s)", runtime.GOOS)
	}

	var b strings.Builder

	vgsOut, vgsErr := runLVMCmd(ctx, "vgs",
		"--noheadings", "--nosuffix", "--units", "g",
		"-o", "vg_name,vg_size,vg_free,pv_count,lv_count,vg_attr",
		"--separator", "|")

	lvsOut, lvsErr := runLVMCmd(ctx, "lvs",
		"--noheadings", "--nosuffix", "--units", "g",
		"-o", "lv_name,vg_name,lv_size,lv_attr,pool_lv,origin,data_percent,copy_percent",
		"--separator", "|")

	if vgsErr != nil && lvsErr != nil {
		return "", fmt.Errorf("LVM not available: vgs: %v; lvs: %v", vgsErr, lvsErr)
	}

	b.WriteString("LVM Status\n\n")

	if vgsErr != nil {
		fmt.Fprintf(&b, "Volume Groups: (error: %v)\n\n", vgsErr)
	} else {
		formatVGS(&b, vgsOut)
	}

	if lvsErr != nil {
		fmt.Fprintf(&b, "Logical Volumes: (error: %v)\n\n", lvsErr)
	} else {
		formatLVS(&b, lvsOut)
	}

	return b.String(), nil
}

func runLVMCmd(ctx context.Context, name string, args ...string) (string, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found: %w", name, err)
	}

	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	timeout := lvmTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("%s timed out after %s", name, timeout)
	}
	if runErr != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return "", fmt.Errorf("%s failed: %v (%s)", name, runErr, stderrStr)
		}
		return "", fmt.Errorf("%s failed: %v", name, runErr)
	}

	return strings.TrimSpace(stdout.String()), nil
}

func formatVGS(b *strings.Builder, raw string) {
	if raw == "" {
		b.WriteString("Volume Groups: none\n\n")
		return
	}

	lines := strings.Split(raw, "\n")
	b.WriteString("Volume Groups:\n")
	fmt.Fprintf(b, "  %-15s  %10s  %10s  %5s  %5s  %s\n", "VG", "SIZE(G)", "FREE(G)", "#PV", "#LV", "ATTR")
	b.WriteString("  " + strings.Repeat("-", 60) + "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 6 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		attr := fields[5]
		marker := ""
		if len(attr) >= 1 && attr[0] == 'w' {
			// writable = normal
		}
		if strings.Contains(attr, "p") {
			marker = " [!] partial"
		}

		fmt.Fprintf(b, "  %-15s  %10s  %10s  %5s  %5s  %s%s\n",
			fields[0], fields[1], fields[2], fields[3], fields[4], attr, marker)
	}
	b.WriteByte('\n')
}

func formatLVS(b *strings.Builder, raw string) {
	if raw == "" {
		b.WriteString("Logical Volumes: none\n\n")
		return
	}

	lines := strings.Split(raw, "\n")
	b.WriteString("Logical Volumes:\n")
	fmt.Fprintf(b, "  %-20s  %-15s  %10s  %-10s  %s\n", "LV", "VG", "SIZE(G)", "ATTR", "NOTE")
	b.WriteString("  " + strings.Repeat("-", 70) + "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 4 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		lvName := fields[0]
		vgName := fields[1]
		size := fields[2]
		attr := fields[3]

		note := lvAttrNote(attr)

		fmt.Fprintf(b, "  %-20s  %-15s  %10s  %-10s  %s\n",
			lvName, vgName, size, attr, note)
	}
	b.WriteByte('\n')
}

// lvAttrNote interprets the LV attribute string (10 chars in modern LVM).
// Key positions:
//
//	[0] Volume type: - (normal), V (virtual), t (thin pool), T (thin), etc
//	[4] State: a (active), s (suspended), I (invalid), S (suspended snapshot)
//	[8] Health: - (ok), p (partial), r (refresh), m (mismatches), w (writemostly)
func lvAttrNote(attr string) string {
	if len(attr) < 5 {
		return ""
	}

	var notes []string

	// State indicator (position 4)
	if len(attr) > 4 {
		switch attr[4] {
		case 's', 'S':
			notes = append(notes, "[!] SUSPENDED")
		case 'I':
			notes = append(notes, "[!] INVALID")
		}
	}

	// Health indicator (position 8)
	if len(attr) > 8 {
		switch attr[8] {
		case 'p':
			notes = append(notes, "[!] PARTIAL")
		case 'r':
			notes = append(notes, "[!] REFRESH NEEDED")
		case 'm':
			notes = append(notes, "[!] MISMATCHES")
		}
	}

	return strings.Join(notes, ", ")
}
