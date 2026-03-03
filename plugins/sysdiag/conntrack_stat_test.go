package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConntrackStat(t *testing.T) {
	content := `entries  searched found new invalid ignore delete delete_list insert insert_failed drop early_drop icmp_error  expect_new expect_create expect_delete search_restart
00000042 00000100 00000050 00000020 00000005 00000000 00000010 00000008 00000015 00000002 00000001 00000000 00000000  00000000 00000000 00000000 00000003
00000042 00000080 00000030 00000010 00000003 00000000 00000005 00000004 0000000a 00000001 00000000 00000001 00000000  00000000 00000000 00000000 00000002
`
	dir := t.TempDir()
	path := filepath.Join(dir, "nf_conntrack")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := parseConntrackStat(path)
	if err != nil {
		t.Fatalf("parseConntrackStat: %v", err)
	}

	// entries: 0x42 + 0x42 = 132
	if stats.entries != 132 {
		t.Errorf("entries=%d, want 132", stats.entries)
	}
	// insert_failed: 0x2 + 0x1 = 3
	if stats.insertFailed != 3 {
		t.Errorf("insertFailed=%d, want 3", stats.insertFailed)
	}
	// drop: 0x1 + 0x0 = 1
	if stats.drop != 1 {
		t.Errorf("drop=%d, want 1", stats.drop)
	}
	// search_restart: 0x3 + 0x2 = 5
	if stats.searchRestart != 5 {
		t.Errorf("searchRestart=%d, want 5", stats.searchRestart)
	}
}

func TestHexField(t *testing.T) {
	fields := []string{"0000000a", "0000001f"}
	idx := map[string]int{"a": 0, "b": 1}

	if v := hexField(fields, idx, "a"); v != 10 {
		t.Errorf("hexField 'a': got %d, want 10", v)
	}
	if v := hexField(fields, idx, "b"); v != 31 {
		t.Errorf("hexField 'b': got %d, want 31", v)
	}
	if v := hexField(fields, idx, "missing"); v != 0 {
		t.Errorf("hexField 'missing': got %d, want 0", v)
	}
}

func TestFormatConntrackStat(t *testing.T) {
	stats := ctStat{
		entries: 100, insert: 50, insertFailed: 2, drop: 1,
		earlyDrop: 0, searchRestart: 3, new: 40, delete: 30,
	}

	out := formatConntrackStat(1000, 10000, nil, nil, stats, nil)
	if !strings.Contains(out, "1000 / 10000") {
		t.Fatal("expected usage in output")
	}
	if !strings.Contains(out, "10.0%") {
		t.Fatal("expected percentage in output")
	}
	if !strings.Contains(out, "[!]") {
		t.Fatal("expected [!] marker for non-zero drops")
	}
}

func TestFormatConntrackStatHighUsage(t *testing.T) {
	out := formatConntrackStat(9500, 10000, nil, nil, ctStat{}, nil)
	if !strings.Contains(out, "[!!!]") {
		t.Fatal("expected [!!!] for 95% usage")
	}
}

func TestReadUint64File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "val")
	if err := os.WriteFile(path, []byte("12345\n"), 0644); err != nil {
		t.Fatal(err)
	}
	v, err := readUint64File(path)
	if err != nil {
		t.Fatalf("readUint64File: %v", err)
	}
	if v != 12345 {
		t.Errorf("got %d, want 12345", v)
	}
}
