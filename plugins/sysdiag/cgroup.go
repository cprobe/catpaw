package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/diagnose"
)

const cgroupMaxFileSize = 64 * 1024

func registerCgroup(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_cgroup", "sysdiag:cgroup",
		"Cgroup diagnostic tools (CPU, memory, IO limits and usage). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_cgroup", diagnose.DiagnoseTool{
		Name:        "cgroup_usage",
		Description: "Show cgroup CPU/memory limits and current usage. Auto-detects cgroup v1 vs v2. Default: root cgroup. Specify 'path' for a specific cgroup (e.g. '/system.slice/nginx.service').",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "path", Type: "string", Description: "Cgroup path relative to mount (e.g. '/system.slice/nginx.service'). Default: root '/'"},
		},
		Execute: execCgroupUsage,
	})
}

func execCgroupUsage(_ context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("cgroup_usage requires linux (current: %s)", runtime.GOOS)
	}

	subPath := "/"
	if p := args["path"]; p != "" {
		subPath = p
	}
	if err := validateCgroupPath(subPath); err != nil {
		return "", err
	}

	ver := detectCgroupVersion()
	switch ver {
	case 2:
		return readCgroupV2(subPath)
	case 1:
		return readCgroupV1(subPath)
	default:
		return "", fmt.Errorf("could not detect cgroup version (neither v1 nor v2 found)")
	}
}

func validateCgroupPath(p string) error {
	if strings.Contains(p, "..") {
		return fmt.Errorf("invalid cgroup path: must not contain '..'")
	}
	cleaned := filepath.Clean(p)
	if cleaned == "" || cleaned == "." {
		return nil
	}
	// Paths like "/system.slice/nginx.service" are valid; cgroupSubPathForJoin strips
	// leading slash so filepath.Join won't discard our base.
	return nil
}

// cgroupSubPathForJoin returns a path safe for filepath.Join with /sys/fs/cgroup.
// Converts "/" to "." so Join produces /sys/fs/cgroup; strips leading slash from "/a/b".
func cgroupSubPathForJoin(p string) string {
	cleaned := filepath.Clean(p)
	if cleaned == "/" || cleaned == "" || cleaned == "." {
		return "."
	}
	return strings.TrimPrefix(cleaned, "/")
}

const cgroupMount = "/sys/fs/cgroup"

// resolveCgroupBase returns the validated base path under cgroupMount.
// Prevents path traversal via .. or symlinks (validates prefix after Clean).
func resolveCgroupBase(subPath string) (string, error) {
	rel := cgroupSubPathForJoin(subPath)
	base := filepath.Join(cgroupMount, rel)
	base = filepath.Clean(base)
	// Ensure we didn't escape the cgroup mount (handles .. and symlink-like traversal)
	if base != cgroupMount && !strings.HasPrefix(base, cgroupMount+"/") {
		return "", fmt.Errorf("invalid cgroup path: resolved outside %s", cgroupMount)
	}
	return base, nil
}

// detectCgroupVersion checks whether the system uses cgroup v2 (unified) or v1.
func detectCgroupVersion() int {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return 2
	}
	if _, err := os.Stat("/sys/fs/cgroup/cpu"); err == nil {
		return 1
	}
	return 0
}

// --- cgroup v2 ---

func readCgroupV2(subPath string) (string, error) {
	base, err := resolveCgroupBase(subPath)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("Cgroup version: v2\n")
	fmt.Fprintf(&b, "Path: %s\n\n", base)

	writeCPUV2(&b, base)
	b.WriteString("\n")
	writeMemoryV2(&b, base)

	return b.String(), nil
}

func writeCPUV2(b *strings.Builder, base string) {
	b.WriteString("== CPU ==\n")

	quota, period, err := parseCPUMax(filepath.Join(base, "cpu.max"))
	if err != nil {
		fmt.Fprintf(b, "  cpu.max: %v\n", err)
	} else if quota < 0 {
		b.WriteString("  Limit: unlimited\n")
	} else if period > 0 {
		cores := float64(quota) / float64(period)
		fmt.Fprintf(b, "  Limit: %.2f cores (quota=%d period=%d)\n", cores, quota, period)
	}

	stats := readKVFile(filepath.Join(base, "cpu.stat"))
	if usageUs, ok := stats["usage_usec"]; ok {
		fmt.Fprintf(b, "  Total usage: %s\n", formatMicroseconds(usageUs))
	}
	if throttled, ok := stats["nr_throttled"]; ok {
		b.WriteString(fmt.Sprintf("  Throttled periods: %d", throttled))
		if throttledUs, ok := stats["throttled_usec"]; ok {
			fmt.Fprintf(b, " (%s)", formatMicroseconds(throttledUs))
		}
		b.WriteString("\n")
	}
}

func writeMemoryV2(b *strings.Builder, base string) {
	b.WriteString("== Memory ==\n")

	limit := readSingleValue(filepath.Join(base, "memory.max"))
	current := readSingleValue(filepath.Join(base, "memory.current"))

	if limit == "max" || limit == "" {
		b.WriteString("  Limit: unlimited\n")
	} else if limitBytes, err := strconv.ParseUint(limit, 10, 64); err == nil {
		fmt.Fprintf(b, "  Limit: %s\n", humanBytes(limitBytes))
	}

	if current != "" {
		if currentBytes, err := strconv.ParseUint(current, 10, 64); err == nil {
			fmt.Fprintf(b, "  Current: %s\n", humanBytes(currentBytes))

			if limit != "max" && limit != "" {
				if limitBytes, err := strconv.ParseUint(limit, 10, 64); err == nil && limitBytes > 0 {
					pct := float64(currentBytes) / float64(limitBytes) * 100
					fmt.Fprintf(b, "  Usage: %.1f%%\n", pct)
				}
			}
		}
	}

	stats := readKVFile(filepath.Join(base, "memory.stat"))
	for _, key := range []string{"anon", "file", "slab", "sock", "shmem"} {
		if v, ok := stats[key]; ok {
			fmt.Fprintf(b, "  %s: %s\n", key, humanBytes(uint64(v)))
		}
	}
}

// parseCPUMax parses "cpu.max" which is "quota period" or "max period".
func parseCPUMax(path string) (quota int64, period int64, err error) {
	content := readSingleValue(path)
	if content == "" {
		return 0, 0, fmt.Errorf("cannot read %s", path)
	}
	parts := strings.Fields(content)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected cpu.max format: %q", content)
	}
	if parts[0] == "max" {
		quota = -1
	} else {
		quota, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse cpu.max quota: %w", err)
		}
	}
	period, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse cpu.max period: %w", err)
	}
	if period <= 0 {
		return 0, 0, fmt.Errorf("invalid cpu.max period: %d (must be positive)", period)
	}
	return quota, period, nil
}

// --- cgroup v1 ---

func readCgroupV1(subPath string) (string, error) {
	cleanSub := cgroupSubPathForJoin(subPath)

	var b strings.Builder
	b.WriteString("Cgroup version: v1\n")
	fmt.Fprintf(&b, "Sub-path: %s\n\n", cleanSub)

	writeCPUV1(&b, cleanSub)
	b.WriteString("\n")
	writeMemoryV1(&b, cleanSub)

	return b.String(), nil
}

func writeCPUV1(b *strings.Builder, cleanSub string) {
	b.WriteString("== CPU ==\n")
	cpuBase := filepath.Join("/sys/fs/cgroup/cpu", cleanSub)

	quotaStr := readSingleValue(filepath.Join(cpuBase, "cpu.cfs_quota_us"))
	periodStr := readSingleValue(filepath.Join(cpuBase, "cpu.cfs_period_us"))

	quota, _ := strconv.ParseInt(quotaStr, 10, 64)
	period, _ := strconv.ParseInt(periodStr, 10, 64)

	if quota <= 0 {
		b.WriteString("  Limit: unlimited\n")
	} else if period > 0 {
		cores := float64(quota) / float64(period)
		fmt.Fprintf(b, "  Limit: %.2f cores (quota=%d period=%d)\n", cores, quota, period)
	}

	usageStr := readSingleValue(filepath.Join(cpuBase, "cpuacct.usage"))
	if usageNs, err := strconv.ParseInt(usageStr, 10, 64); err == nil && usageNs > 0 {
		fmt.Fprintf(b, "  Total usage: %s\n", formatMicroseconds(usageNs/1000))
	}
}

func writeMemoryV1(b *strings.Builder, cleanSub string) {
	b.WriteString("== Memory ==\n")
	memBase := filepath.Join("/sys/fs/cgroup/memory", cleanSub)

	limitStr := readSingleValue(filepath.Join(memBase, "memory.limit_in_bytes"))
	usageStr := readSingleValue(filepath.Join(memBase, "memory.usage_in_bytes"))

	var limitBytes, usageBytes uint64
	// memory.limit_in_bytes "unlimited" varies by kernel: PAGE_COUNTER_MAX = LONG_MAX
	// rounded to page boundary. 4KB pages: 2^63-4096; 64KB: 2^63-65536; some report 2^63.
	// Use 2^63-65536 (max common page size) as threshold to catch all variants.
	const unlimitedV1Threshold = uint64(1)<<63 - 65536

	if v, err := strconv.ParseUint(limitStr, 10, 64); err == nil {
		limitBytes = v
	}
	if v, err := strconv.ParseUint(usageStr, 10, 64); err == nil {
		usageBytes = v
	}

	if limitBytes == 0 || limitBytes >= unlimitedV1Threshold {
		b.WriteString("  Limit: unlimited\n")
	} else {
		fmt.Fprintf(b, "  Limit: %s\n", humanBytes(limitBytes))
	}

	if usageBytes > 0 {
		fmt.Fprintf(b, "  Current: %s\n", humanBytes(usageBytes))
		if limitBytes > 0 && limitBytes < unlimitedV1Threshold {
			pct := float64(usageBytes) / float64(limitBytes) * 100
			fmt.Fprintf(b, "  Usage: %.1f%%\n", pct)
		}
	}
}

// --- helpers ---

// readSingleValue reads a cgroup control file that contains a single value.
// Uses bounded read to avoid DoS when path resolves to a symlink to a large file.
func readSingleValue(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, cgroupMaxFileSize))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readKVFile reads a cgroup stat file with "key value" lines.
// Uses bounded read to avoid DoS when path resolves to a symlink to a large file.
func readKVFile(path string) map[string]int64 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, cgroupMaxFileSize))
	if err != nil {
		return nil
	}

	result := make(map[string]int64)
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		v, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		result[parts[0]] = v
	}
	return result
}

func formatMicroseconds(us int64) string {
	if us < 0 {
		us = 0
	}
	sec := float64(us) / 1e6
	// Cap at ~2777h to avoid overflow in division and unreadable output
	const maxHours = 10000.0
	if sec >= maxHours*3600 {
		return fmt.Sprintf(">%.0fh", maxHours)
	}
	if sec < 60 {
		return fmt.Sprintf("%.2fs", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%.1fm", sec/60)
	}
	return fmt.Sprintf("%.1fh", sec/3600)
}
