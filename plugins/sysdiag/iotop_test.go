package sysdiag

import (
	"strings"
	"testing"
)

func TestParseIOLine(t *testing.T) {
	tests := []struct {
		line    string
		wantKey string
		wantVal uint64
		wantOk  bool
	}{
		{"read_bytes: 12345", "read_bytes", 12345, true},
		{"write_bytes: 0", "write_bytes", 0, true},
		{"cancelled_write_bytes: 100", "cancelled_write_bytes", 100, true},
		{"bad line", "", 0, false},
		{"key:", "key", 0, false},     // empty value
		{"key: abc", "key", 0, false}, // non-numeric
	}

	for _, tt := range tests {
		k, v, ok := parseIOLine(tt.line)
		if ok != tt.wantOk {
			t.Errorf("parseIOLine(%q): ok=%v, want %v", tt.line, ok, tt.wantOk)
			continue
		}
		if ok && (k != tt.wantKey || v != tt.wantVal) {
			t.Errorf("parseIOLine(%q): got (%q, %d), want (%q, %d)", tt.line, k, v, tt.wantKey, tt.wantVal)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		input uint64
		want  string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{1048576, "1.0M"},
		{1073741824, "1.0G"},
		{1099511627776, "1.0T"},
	}

	for _, tt := range tests {
		got := humanBytes(tt.input)
		if got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSortProcs(t *testing.T) {
	procs := []procIOInfo{
		{pid: 1, readBytes: 100, writeBytes: 200},
		{pid: 2, readBytes: 500, writeBytes: 50},
		{pid: 3, readBytes: 10, writeBytes: 1000},
	}

	sortProcs(procs, "total")
	if procs[0].pid != 3 {
		t.Errorf("sort by total: first should be pid 3 (total 1010), got pid %d", procs[0].pid)
	}

	sortProcs(procs, "read")
	if procs[0].pid != 2 {
		t.Errorf("sort by read: first should be pid 2 (read 500), got pid %d", procs[0].pid)
	}

	sortProcs(procs, "write")
	if procs[0].pid != 3 {
		t.Errorf("sort by write: first should be pid 3 (write 1000), got pid %d", procs[0].pid)
	}
}

func TestFormatIOTop(t *testing.T) {
	procs := []procIOInfo{
		{pid: 42, comm: "myapp", readBytes: 1048576, writeBytes: 2097152},
	}
	out := formatIOTop(procs, 100, 5, "total")

	if !strings.Contains(out, "42") {
		t.Fatal("expected PID in output")
	}
	if !strings.Contains(out, "myapp") {
		t.Fatal("expected process name in output")
	}
	if !strings.Contains(out, "1.0M") {
		t.Fatal("expected read bytes formatted in output")
	}
	if !strings.Contains(out, "skipped 5") {
		t.Fatal("expected skipped count in output")
	}
}

func TestSanitizeIOComm(t *testing.T) {
	if sanitizeIOComm("hello\nworld") != "hello world" {
		t.Fatal("should replace newline with space")
	}
	if sanitizeIOComm("normal") != "normal" {
		t.Fatal("should not change normal string")
	}
}
