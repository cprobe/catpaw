package sysdiag

import (
	"strings"
	"testing"
)

func TestClassifyFD(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"socket:[12345]", "socket"},
		{"pipe:[67890]", "pipe"},
		{"anon_inode:[eventpoll]", "anon"},
		{"/var/log/syslog", "file"},
		{"/dev/null", "file"},
		{"(error)", "?"},
		{"something-else", "other"},
	}
	for _, tt := range tests {
		got := classifyFD(tt.target)
		if got != tt.want {
			t.Errorf("classifyFD(%q) = %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestFormatOpenFiles(t *testing.T) {
	entries := []fdEntry{
		{fd: 0, target: "/dev/null", kind: "file"},
		{fd: 1, target: "socket:[12345]", kind: "socket"},
		{fd: 2, target: "pipe:[67890]", kind: "pipe"},
	}
	out := formatOpenFiles(42, entries, 3, 100)
	if !strings.Contains(out, "PID 42") {
		t.Fatal("expected PID in output")
	}
	if !strings.Contains(out, "3 total") {
		t.Fatal("expected total count in output")
	}
	if !strings.Contains(out, "file=1") {
		t.Fatal("expected file count in type summary")
	}
	if !strings.Contains(out, "socket=1") {
		t.Fatal("expected socket count in type summary")
	}
}

func TestFormatOpenFilesEmpty(t *testing.T) {
	out := formatOpenFiles(1, nil, 0, 100)
	if !strings.Contains(out, "No open file") {
		t.Fatal("expected 'No open file' message")
	}
}

func TestFormatOpenFilesTruncated(t *testing.T) {
	entries := []fdEntry{
		{fd: 0, target: "/dev/null", kind: "file"},
	}
	out := formatOpenFiles(42, entries, 500, 100)
	if !strings.Contains(out, "showing first 100") {
		t.Fatal("expected truncation notice")
	}
}

func TestExecOpenFiles_Validation(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]string
		wantErr string
	}{
		{"empty pid", map[string]string{}, "pid parameter is required"},
		{"zero pid", map[string]string{"pid": "0"}, "invalid pid"},
		{"bad max_fds", map[string]string{"pid": "1", "max_fds": "abc"}, "invalid max_fds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := execOpenFiles(t.Context(), tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
