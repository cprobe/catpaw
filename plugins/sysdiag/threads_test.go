package sysdiag

import (
	"strings"
	"testing"
)

func TestParseThreadStat(t *testing.T) {
	tests := []struct {
		line  string
		state byte
		utime uint64
		stime uint64
	}{
		{
			"12345 (my process) S 1 12345 12345 0 -1 4194304 100 0 0 0 150 30 0 0 20 0 1 0 12345 0 0",
			'S', 150, 30,
		},
		{
			"999 (kworker/0:1-events) D 2 0 0 0 -1 69238880 0 0 0 0 0 5 0 0 20 0 1 0 0 0 0",
			'D', 0, 5,
		},
		{
			"1 (bash (extra)) S 0 1 1 0 -1 4194560 500 0 0 0 200 100 0 0 20 0 1 0 0 0 0",
			'S', 200, 100,
		},
		{"", '?', 0, 0},
		{"short", '?', 0, 0},
	}

	for _, tc := range tests {
		state, utime, stime := parseThreadStat(tc.line)
		if state != tc.state {
			t.Errorf("line=%q: state=%c, want %c", tc.line[:min(40, len(tc.line))], state, tc.state)
		}
		if utime != tc.utime {
			t.Errorf("line=%q: utime=%d, want %d", tc.line[:min(40, len(tc.line))], utime, tc.utime)
		}
		if stime != tc.stime {
			t.Errorf("line=%q: stime=%d, want %d", tc.line[:min(40, len(tc.line))], stime, tc.stime)
		}
	}
}

func TestFormatThreads(t *testing.T) {
	threads := []threadInfo{
		{tid: 100, comm: "main", state: 'S', utime: 500, stime: 200},
		{tid: 101, comm: "worker-1", state: 'S', utime: 300, stime: 100},
		{tid: 102, comm: "io-handler", state: 'D', utime: 10, stime: 5},
	}
	out := formatThreads(threads, 1234, 50)

	if !strings.Contains(out, "3 threads") {
		t.Error("expected thread count")
	}
	if !strings.Contains(out, "[!]") {
		t.Error("expected D-state warning")
	}
	if !strings.Contains(out, "main") {
		t.Error("expected main thread")
	}
}

func TestFormatThreadsMaxShow(t *testing.T) {
	threads := make([]threadInfo, 10)
	for i := range threads {
		threads[i] = threadInfo{tid: 100 + i, comm: "t", state: 'S'}
	}
	out := formatThreads(threads, 1, 3)
	if !strings.Contains(out, "7 more threads") {
		t.Error("expected truncation notice")
	}
}

func TestExecProcThreadsValidation(t *testing.T) {
	tests := []struct {
		args    map[string]string
		wantErr string
	}{
		{map[string]string{}, "pid parameter is required"},
		{map[string]string{"pid": "abc"}, "invalid pid"},
		{map[string]string{"pid": "-1"}, "invalid pid"},
		{map[string]string{"pid": "1", "max_threads": "999"}, "max_threads must be"},
		{map[string]string{"pid": "1", "max_threads": "0"}, "max_threads must be"},
	}

	for _, tc := range tests {
		_, err := execProcThreads(nil, tc.args)
		if err == nil {
			t.Errorf("args=%v: expected error containing %q", tc.args, tc.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("args=%v: err=%q, want containing %q", tc.args, err.Error(), tc.wantErr)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
