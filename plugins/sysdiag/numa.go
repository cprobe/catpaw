package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/cprobe/digcore/diagnose"
)

const (
	numaBasePath   = "/sys/devices/system/node"
	numaMaxRead    = 16 * 1024
)

func registerNUMA(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_numa", "sysdiag:numa",
		"NUMA diagnostic tools (memory distribution, cross-node access). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_numa", diagnose.DiagnoseTool{
		Name:        "numa_stat",
		Description: "Show NUMA memory distribution per node and cross-node access statistics (numa_hit/miss/foreign). Important for high-performance workloads (databases, Redis).",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execNUMAStat,
	})
}

type numaNode struct {
	id        int
	memTotal  uint64
	memFree   uint64
	memUsed   uint64
	numaHit   uint64
	numaMiss  uint64
	foreign   uint64
	localNode uint64
	otherNode uint64
}

func execNUMAStat(_ context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("numa_stat requires linux (current: %s)", runtime.GOOS)
	}

	nodes, err := discoverNUMANodes(numaBasePath)
	if err != nil {
		return "", err
	}

	return formatNUMANodes(nodes), nil
}

func discoverNUMANodes(basePath string) ([]numaNode, error) {
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (NUMA info unavailable)", basePath, err)
	}

	var nodes []numaNode
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "node") || !entry.IsDir() {
			continue
		}
		idStr := name[4:]
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}

		node := numaNode{id: id}
		nodePath := filepath.Join(basePath, name)

		readNUMAMeminfo(nodePath, &node)
		readNUMAStats(nodePath, &node)

		nodes = append(nodes, node)
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("no NUMA nodes found in %s", basePath)
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].id < nodes[j].id
	})
	return nodes, nil
}

func readNUMAMeminfo(nodePath string, node *numaNode) {
	path := filepath.Join(nodePath, "meminfo")
	data, err := readLimitedFile(path, numaMaxRead)
	if err != nil {
		return
	}

	for _, line := range strings.Split(string(data), "\n") {
		// Format: "Node 0 MemTotal:  16384000 kB"
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		key := strings.TrimSuffix(fields[2], ":")
		val, err := strconv.ParseUint(fields[3], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			node.memTotal = val * 1024
		case "MemFree":
			node.memFree = val * 1024
		}
	}
	if node.memTotal > node.memFree {
		node.memUsed = node.memTotal - node.memFree
	}
}

func readNUMAStats(nodePath string, node *numaNode) {
	path := filepath.Join(nodePath, "numastat")
	data, err := readLimitedFile(path, numaMaxRead)
	if err != nil {
		return
	}

	for _, line := range strings.Split(string(data), "\n") {
		// Format: "numa_hit 12345"
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "numa_hit":
			node.numaHit = val
		case "numa_miss":
			node.numaMiss = val
		case "numa_foreign":
			node.foreign = val
		case "local_node":
			node.localNode = val
		case "other_node":
			node.otherNode = val
		}
	}
}

func readLimitedFile(path string, maxSize int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxSize))
}

func formatNUMANodes(nodes []numaNode) string {
	var b strings.Builder
	fmt.Fprintf(&b, "NUMA Topology: %d nodes\n\n", len(nodes))

	// Memory distribution
	b.WriteString("Memory Distribution:\n")
	fmt.Fprintf(&b, "  %-8s  %10s  %10s  %10s  %7s\n", "NODE", "TOTAL", "USED", "FREE", "USED%")
	b.WriteString("  " + strings.Repeat("-", 52) + "\n")

	var totalMem, totalUsed uint64
	for _, n := range nodes {
		pct := float64(0)
		if n.memTotal > 0 {
			pct = float64(n.memUsed) / float64(n.memTotal) * 100
		}
		fmt.Fprintf(&b, "  node%-3d  %10s  %10s  %10s  %6.1f%%\n",
			n.id,
			humanBytes(n.memTotal),
			humanBytes(n.memUsed),
			humanBytes(n.memFree),
			pct)
		totalMem += n.memTotal
		totalUsed += n.memUsed
	}

	// Check for memory imbalance
	if len(nodes) > 1 && totalMem > 0 {
		avgMem := totalMem / uint64(len(nodes))
		var maxDev float64
		for _, n := range nodes {
			if avgMem > 0 {
				dev := float64(n.memTotal) / float64(avgMem)
				if dev > maxDev {
					maxDev = dev
				}
			}
		}
		if maxDev > 1.5 {
			b.WriteString("\n  [!] Asymmetric NUMA: node memory sizes differ significantly\n")
		}
	}

	// NUMA access statistics
	hasStats := false
	for _, n := range nodes {
		if n.numaHit > 0 || n.numaMiss > 0 {
			hasStats = true
			break
		}
	}

	if hasStats {
		b.WriteString("\nCross-Node Access Statistics:\n")
		fmt.Fprintf(&b, "  %-8s  %12s  %12s  %12s  %12s  %12s  %7s\n",
			"NODE", "NUMA_HIT", "NUMA_MISS", "FOREIGN", "LOCAL", "OTHER", "MISS%")
		b.WriteString("  " + strings.Repeat("-", 85) + "\n")

		for _, n := range nodes {
			total := n.numaHit + n.numaMiss
			missPct := float64(0)
			if total > 0 {
				missPct = float64(n.numaMiss) / float64(total) * 100
			}
			marker := ""
			if missPct >= 10.0 {
				marker = " [!]"
			}
			fmt.Fprintf(&b, "  node%-3d  %12d  %12d  %12d  %12d  %12d  %6.1f%%%s\n",
				n.id, n.numaHit, n.numaMiss, n.foreign, n.localNode, n.otherNode, missPct, marker)
		}
	}

	return b.String()
}
