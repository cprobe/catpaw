package sysdiag

import (
	"os"
	"strings"
	"testing"
)

func TestValidateCgroupPath(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"/", false},
		{"/system.slice/nginx.service", false},
		{"/../etc/passwd", true},
		{"/a/b/../c", true},
		{"relative/path", false},
	}
	for _, tt := range tests {
		err := validateCgroupPath(tt.path)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateCgroupPath(%q): err=%v, wantErr=%v", tt.path, err, tt.wantErr)
		}
	}
}

func TestParseCPUMax(t *testing.T) {
	tests := []struct {
		name    string
		content string
		quota   int64
		period  int64
		wantErr bool
	}{
		{"unlimited", "max 100000", -1, 100000, false},
		{"limited", "50000 100000", 50000, 100000, false},
		{"bad format", "invalid", 0, 0, true},
		{"period zero", "50000 0", 0, 0, true},
		{"max period zero", "max 0", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := dir + "/cpu.max"
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			q, p, err := parseCPUMax(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCPUMax: err=%v, wantErr=%v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if q != tt.quota {
					t.Errorf("quota = %d, want %d", q, tt.quota)
				}
				if p != tt.period {
					t.Errorf("period = %d, want %d", p, tt.period)
				}
			}
		})
	}
}

func TestParseCPUMaxMissing(t *testing.T) {
	_, _, err := parseCPUMax("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadKVFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/stat"
	content := "usage_usec 12345678\nnr_throttled 100\nthrottled_usec 5000000\nbad line here\nanother 999\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	m := readKVFile(path)
	if m["usage_usec"] != 12345678 {
		t.Errorf("usage_usec = %d, want 12345678", m["usage_usec"])
	}
	if m["nr_throttled"] != 100 {
		t.Errorf("nr_throttled = %d, want 100", m["nr_throttled"])
	}
	if m["another"] != 999 {
		t.Errorf("another = %d, want 999", m["another"])
	}
}

func TestReadKVFileNotExist(t *testing.T) {
	m := readKVFile("/nonexistent/path")
	if m != nil {
		t.Fatal("expected nil for nonexistent file")
	}
}

func TestFormatMicroseconds(t *testing.T) {
	tests := []struct {
		us   int64
		want string
	}{
		{0, "0.00s"},
		{1500000, "1.50s"},
		{90000000, "1.5m"},
		{7200000000, "2.0h"},
		{-100, "0.00s"},
		{36e12 + 1, ">10000h"}, // just over 10000h
	}
	for _, tt := range tests {
		got := formatMicroseconds(tt.us)
		if got != tt.want {
			t.Errorf("formatMicroseconds(%d) = %q, want %q", tt.us, got, tt.want)
		}
	}
}

func TestDetectCgroupVersion(t *testing.T) {
	ver := detectCgroupVersion()
	if ver < 0 || ver > 2 {
		t.Errorf("detectCgroupVersion() = %d, expected 0, 1, or 2", ver)
	}
}

func TestReadSingleValue(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/value"
	if err := os.WriteFile(path, []byte("42\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got := readSingleValue(path)
	if got != "42" {
		t.Errorf("readSingleValue = %q, want '42'", got)
	}

	got = readSingleValue("/nonexistent/path")
	if got != "" {
		t.Errorf("readSingleValue for nonexistent = %q, want ''", got)
	}
}

func TestHumanBytesReuse(t *testing.T) {
	result := humanBytes(1048576)
	if !strings.Contains(result, "M") {
		t.Errorf("humanBytes(1MB) = %q, expected to contain 'M'", result)
	}
}
