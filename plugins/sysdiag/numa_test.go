package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadNUMAMeminfo(t *testing.T) {
	dir := t.TempDir()
	content := `Node 0 MemTotal:       16384000 kB
Node 0 MemFree:         8192000 kB
Node 0 MemUsed:         8192000 kB
`
	if err := os.WriteFile(filepath.Join(dir, "meminfo"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	node := numaNode{}
	readNUMAMeminfo(dir, &node)

	if node.memTotal != 16384000*1024 {
		t.Errorf("memTotal=%d, want %d", node.memTotal, 16384000*1024)
	}
	if node.memFree != 8192000*1024 {
		t.Errorf("memFree=%d, want %d", node.memFree, 8192000*1024)
	}
	if node.memUsed != node.memTotal-node.memFree {
		t.Errorf("memUsed=%d, want %d", node.memUsed, node.memTotal-node.memFree)
	}
}

func TestReadNUMAStats(t *testing.T) {
	dir := t.TempDir()
	content := `numa_hit 100000
numa_miss 500
numa_foreign 300
interleave_hit 0
local_node 99500
other_node 1000
`
	if err := os.WriteFile(filepath.Join(dir, "numastat"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	node := numaNode{}
	readNUMAStats(dir, &node)

	if node.numaHit != 100000 {
		t.Errorf("numaHit=%d, want 100000", node.numaHit)
	}
	if node.numaMiss != 500 {
		t.Errorf("numaMiss=%d, want 500", node.numaMiss)
	}
	if node.otherNode != 1000 {
		t.Errorf("otherNode=%d, want 1000", node.otherNode)
	}
}

func TestDiscoverNUMANodes(t *testing.T) {
	base := t.TempDir()

	// Create two fake NUMA nodes
	for _, id := range []string{"node0", "node1"} {
		nodeDir := filepath.Join(base, id)
		os.MkdirAll(nodeDir, 0755)
		os.WriteFile(filepath.Join(nodeDir, "meminfo"), []byte("Node 0 MemTotal: 8192000 kB\nNode 0 MemFree: 4096000 kB\n"), 0644)
		os.WriteFile(filepath.Join(nodeDir, "numastat"), []byte("numa_hit 50000\nnuma_miss 100\nnuma_foreign 50\nlocal_node 49900\nother_node 200\n"), 0644)
	}

	nodes, err := discoverNUMANodes(base)
	if err != nil {
		t.Fatalf("discoverNUMANodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].id != 0 || nodes[1].id != 1 {
		t.Errorf("unexpected node IDs: %d, %d", nodes[0].id, nodes[1].id)
	}
}

func TestFormatNUMANodes(t *testing.T) {
	nodes := []numaNode{
		{id: 0, memTotal: 16 << 30, memFree: 8 << 30, memUsed: 8 << 30, numaHit: 100000, numaMiss: 500},
		{id: 1, memTotal: 16 << 30, memFree: 4 << 30, memUsed: 12 << 30, numaHit: 80000, numaMiss: 20000},
	}

	out := formatNUMANodes(nodes)
	if !strings.Contains(out, "2 nodes") {
		t.Fatal("expected '2 nodes' in output")
	}
	if !strings.Contains(out, "node0") {
		t.Fatal("expected node0 in output")
	}
	// node1 has 20% miss rate -> should trigger [!]
	if !strings.Contains(out, "[!]") {
		t.Fatal("expected [!] marker for high miss rate on node1")
	}
}
