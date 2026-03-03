package sysdiag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/pkg/cmdx"
)

const lsblkTimeout = 10 * time.Second

func registerBlockDevices(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_block", "sysdiag:block",
		"Block device diagnostic tools (disk topology, partitions, LVM, mount points). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_block", diagnose.DiagnoseTool{
		Name:        "block_devices",
		Description: "Show block device topology: disks, partitions, LVM volumes, and their mount points. Tree view showing parent-child relationships.",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execBlockDevices,
	})
}

type blockDev struct {
	Name       string
	Type       string // disk, part, lvm, rom, loop, etc
	Size       string
	FsType     string
	MountPoint string
	RO         bool
	Children   []blockDev
}

func execBlockDevices(ctx context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("block_devices requires linux (current: %s)", runtime.GOOS)
	}

	bin, err := exec.LookPath("lsblk")
	if err != nil {
		return "", fmt.Errorf("lsblk not found: %w", err)
	}

	devices, err := tryLsblkJSON(ctx, bin)
	if err != nil {
		return tryLsblkText(ctx, bin)
	}

	return formatBlockDevs(devices), nil
}

type lsblkJSON struct {
	BlockDevices []lsblkEntry `json:"blockdevices"`
}

type lsblkEntry struct {
	Name       string       `json:"name"`
	Type       string       `json:"type"`
	Size       string       `json:"size"`
	FsType     interface{}  `json:"fstype"` // can be string or null
	MountPoint interface{}  `json:"mountpoint"` // can be string or null
	RO         bool         `json:"ro"`
	Children   []lsblkEntry `json:"children"`
}

func tryLsblkJSON(ctx context.Context, bin string) ([]blockDev, error) {
	cmd := exec.Command(bin, "-J", "-o", "NAME,TYPE,SIZE,FSTYPE,MOUNTPOINT,RO")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	timeout := lsblkTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return nil, fmt.Errorf("lsblk timed out")
	}
	if runErr != nil {
		return nil, runErr
	}

	var result lsblkJSON
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, err
	}

	return convertLsblkEntries(result.BlockDevices), nil
}

func convertLsblkEntries(entries []lsblkEntry) []blockDev {
	devs := make([]blockDev, 0, len(entries))
	for _, e := range entries {
		devs = append(devs, convertLsblkEntry(e))
	}
	return devs
}

func convertLsblkEntry(e lsblkEntry) blockDev {
	dev := blockDev{
		Name: e.Name,
		Type: e.Type,
		Size: e.Size,
		RO:   e.RO,
	}
	if s, ok := e.FsType.(string); ok {
		dev.FsType = s
	}
	if s, ok := e.MountPoint.(string); ok {
		dev.MountPoint = s
	}
	for _, child := range e.Children {
		dev.Children = append(dev.Children, convertLsblkEntry(child))
	}
	return dev
}

func tryLsblkText(ctx context.Context, bin string) (string, error) {
	cmd := exec.Command(bin, "-o", "NAME,TYPE,SIZE,FSTYPE,MOUNTPOINT,RO", "--tree")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	timeout := lsblkTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("lsblk timed out")
	}

	output := strings.TrimSpace(stdout.String())
	if runErr != nil && output == "" {
		return "", fmt.Errorf("lsblk failed: %v (%s)", runErr, strings.TrimSpace(stderr.String()))
	}

	if output == "" {
		return "No block devices found.", nil
	}

	var b strings.Builder
	b.WriteString("Block Device Topology (lsblk text fallback)\n\n")
	b.WriteString(output)
	b.WriteByte('\n')
	return b.String(), nil
}

func formatBlockDevs(devices []blockDev) string {
	if len(devices) == 0 {
		return "No block devices found."
	}

	var b strings.Builder
	totalDisks := 0
	for _, d := range devices {
		if d.Type == "disk" {
			totalDisks++
		}
	}

	fmt.Fprintf(&b, "Block Device Topology: %d devices (%d disks)\n\n", countDevices(devices), totalDisks)

	fmt.Fprintf(&b, "%-24s  %-6s  %8s  %-10s  %-3s  %s\n",
		"NAME", "TYPE", "SIZE", "FSTYPE", "RO", "MOUNTPOINT")
	b.WriteString(strings.Repeat("-", 80))
	b.WriteByte('\n')

	for _, d := range devices {
		writeBlockDev(&b, d, 0)
	}

	return b.String()
}

func writeBlockDev(b *strings.Builder, dev blockDev, depth int) {
	writeBlockDevWithSibling(b, dev, depth, true)
}

func writeBlockDevWithSibling(b *strings.Builder, dev blockDev, depth int, isLast bool) {
	indent := strings.Repeat("  ", depth)
	prefix := ""
	if depth > 0 {
		if isLast {
			prefix = "└─"
		} else {
			prefix = "├─"
		}
	}

	name := indent + prefix + dev.Name
	if len(name) > 24 {
		name = name[:21] + "..."
	}

	fstype := dev.FsType
	if fstype == "" {
		fstype = "-"
	}
	mp := dev.MountPoint
	if mp == "" {
		mp = "-"
	}
	ro := "-"
	if dev.RO {
		ro = "RO"
	}

	marker := ""
	if dev.RO && dev.MountPoint != "" {
		marker = " [!]"
	}

	fmt.Fprintf(b, "%-24s  %-6s  %8s  %-10s  %-3s  %s%s\n",
		name, dev.Type, dev.Size, fstype, ro, mp, marker)

	for i, child := range dev.Children {
		writeBlockDevWithSibling(b, child, depth+1, i == len(dev.Children)-1)
	}
}

func countDevices(devices []blockDev) int {
	count := len(devices)
	for _, d := range devices {
		count += countDevices(d.Children)
	}
	return count
}
