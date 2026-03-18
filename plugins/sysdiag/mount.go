package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/cprobe/digcore/diagnose"
)

const (
	mountsPath        = "/proc/mounts"
	mountsMaxFileSize = 256 * 1024
)

// Pseudo/virtual filesystems to exclude from output for reduced noise.
var pseudoFS = map[string]bool{
	"sysfs": true, "proc": true, "devtmpfs": true, "devpts": true,
	"tmpfs": true, "securityfs": true, "cgroup": true, "cgroup2": true,
	"pstore": true, "bpf": true, "tracefs": true, "debugfs": true,
	"hugetlbfs": true, "mqueue": true, "configfs": true, "fusectl": true,
	"efivarfs": true, "autofs": true, "rpc_pipefs": true, "nsfs": true,
}

func registerMount(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_mount", "sysdiag:mount",
		"Mount point diagnostic tools (filesystem status, read-only detection). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_mount", diagnose.DiagnoseTool{
		Name:        "mount_info",
		Description: "Show mounted filesystems with device, type, and mount options. Highlights read-only mounts. Filters out pseudo filesystems by default.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "show_all", Type: "string", Description: "Set to 'true' to include pseudo/virtual filesystems (tmpfs, proc, sysfs, etc)"},
		},
		Execute: execMountInfo,
	})
}

type mountEntry struct {
	device     string
	mountPoint string
	fsType     string
	options    string
	readOnly   bool
}

func execMountInfo(_ context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("mount_info requires linux (current: %s)", runtime.GOOS)
	}

	showAll := strings.ToLower(args["show_all"]) == "true"

	entries, err := parseMounts(mountsPath)
	if err != nil {
		return "", err
	}

	if !showAll {
		entries = filterRealFS(entries)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].mountPoint < entries[j].mountPoint
	})

	return formatMounts(entries), nil
}

func parseMounts(path string) ([]mountEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, mountsMaxFileSize))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var entries []mountEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		e, ok := parseMountLine(line)
		if !ok {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// parseMountLine parses a single line from /proc/mounts.
// Format: device mountpoint fstype options dump pass
func parseMountLine(line string) (mountEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return mountEntry{}, false
	}

	opts := fields[3]
	ro := isReadOnly(opts)

	return mountEntry{
		device:     unescapeMountField(fields[0]),
		mountPoint: unescapeMountField(fields[1]),
		fsType:     fields[2],
		options:    opts,
		readOnly:   ro,
	}, true
}

func isReadOnly(opts string) bool {
	for _, opt := range strings.Split(opts, ",") {
		if opt == "ro" {
			return true
		}
	}
	return false
}

// unescapeMountField handles octal escapes in /proc/mounts (e.g. \040 for space).
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			o1 := s[i+1] - '0'
			o2 := s[i+2] - '0'
			o3 := s[i+3] - '0'
			if o1 <= 7 && o2 <= 7 && o3 <= 7 {
				b.WriteByte(o1*64 + o2*8 + o3)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func filterRealFS(entries []mountEntry) []mountEntry {
	result := make([]mountEntry, 0, len(entries)/2)
	for _, e := range entries {
		if pseudoFS[e.fsType] {
			continue
		}
		result = append(result, e)
	}
	return result
}

func formatMounts(entries []mountEntry) string {
	if len(entries) == 0 {
		return "No mount points found."
	}

	roCount := 0
	for _, e := range entries {
		if e.readOnly {
			roCount++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Mount points: %d", len(entries))
	if roCount > 0 {
		fmt.Fprintf(&b, " [!] %d READ-ONLY", roCount)
	}
	b.WriteString("\n\n")

	maxMP := 10
	maxDev := 6
	maxType := 4
	for _, e := range entries {
		if len(e.mountPoint) > maxMP {
			maxMP = len(e.mountPoint)
		}
		if len(e.device) > maxDev {
			maxDev = len(e.device)
		}
		if len(e.fsType) > maxType {
			maxType = len(e.fsType)
		}
	}
	if maxMP > 40 {
		maxMP = 40
	}
	if maxDev > 30 {
		maxDev = 30
	}

	hdrFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s  %%s\n", maxMP, maxDev, maxType)
	rowFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-4s  %%s\n", maxMP, maxDev, maxType)

	fmt.Fprintf(&b, hdrFmt, "MOUNTPOINT", "DEVICE", "TYPE", "MODE", "OPTIONS")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", maxMP+maxDev+maxType+30))

	for _, e := range entries {
		mode := "rw"
		if e.readOnly {
			mode = "[RO]"
		}
		mp := e.mountPoint
		if len(mp) > maxMP {
			mp = mp[:maxMP-1] + "~"
		}
		dev := e.device
		if len(dev) > maxDev {
			dev = dev[:maxDev-1] + "~"
		}
		// Show abbreviated options to save space
		opts := abbreviateOpts(e.options)
		fmt.Fprintf(&b, rowFmt, mp, dev, e.fsType, mode, opts)
	}
	return b.String()
}

// abbreviateOpts trims lengthy mount options to keep output readable.
func abbreviateOpts(opts string) string {
	if len(opts) <= 60 {
		return opts
	}
	return opts[:57] + "..."
}
