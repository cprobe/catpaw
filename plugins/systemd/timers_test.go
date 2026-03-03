package systemd

import (
	"strings"
	"testing"
)

func TestParseTimerOutput(t *testing.T) {
	raw := `Mon 2024-01-15 02:30:00 UTC 5h left Mon 2024-01-15 02:30:00 UTC 19h ago logrotate.timer          logrotate.service
Mon 2024-01-15 10:00:00 UTC 2h left Sun 2024-01-14 10:00:00 UTC 1 day ago systemd-tmpfiles-clean.timer systemd-tmpfiles-clean.service
n/a                          n/a     n/a                          n/a      my-old.timer             my-old.service
`
	entries := parseTimerOutput(raw)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].unit != "logrotate.timer" {
		t.Errorf("entry[0].unit=%q, want logrotate.timer", entries[0].unit)
	}
	if entries[0].activates != "logrotate.service" {
		t.Errorf("entry[0].activates=%q, want logrotate.service", entries[0].activates)
	}

	if entries[2].unit != "my-old.timer" {
		t.Errorf("entry[2].unit=%q, want my-old.timer", entries[2].unit)
	}
}

func TestParseTimerLine(t *testing.T) {
	tests := []struct {
		line       string
		wantUnit   string
		wantActive string
	}{
		{
			"Mon 2024-01-15 02:30:00 UTC 5h left Sun 2024-01-14 02:30:00 UTC 19h ago logrotate.timer logrotate.service",
			"logrotate.timer", "logrotate.service",
		},
		{
			"n/a n/a n/a n/a dead.timer dead.service",
			"dead.timer", "dead.service",
		},
		{
			"",
			"", "",
		},
	}

	for _, tc := range tests {
		entry := parseTimerLine(tc.line)
		if entry.unit != tc.wantUnit {
			t.Errorf("line=%q: unit=%q, want %q", tc.line[:min(40, len(tc.line))], entry.unit, tc.wantUnit)
		}
		if entry.activates != tc.wantActive {
			t.Errorf("line=%q: activates=%q, want %q", tc.line[:min(40, len(tc.line))], entry.activates, tc.wantActive)
		}
	}
}

func TestTimerEntryIsOverdue(t *testing.T) {
	tests := []struct {
		passed  string
		overdue bool
	}{
		{"19h ago", false},
		{"passed", true},
		{"1h passed", true},
		{"", false},
	}
	for _, tc := range tests {
		e := timerEntry{passed: tc.passed}
		if e.isOverdue() != tc.overdue {
			t.Errorf("passed=%q: isOverdue=%v, want %v", tc.passed, e.isOverdue(), tc.overdue)
		}
	}
}

func TestFormatTimers(t *testing.T) {
	entries := []timerEntry{
		{unit: "logrotate.timer", activates: "logrotate.service", next: "Mon 2024-01-15", left: "5h left"},
		{unit: "dead.timer", activates: "dead.service", passed: "3h passed"},
	}
	out := formatTimers(entries, false)

	if !strings.Contains(out, "2 entries") {
		t.Error("expected entry count")
	}
	if !strings.Contains(out, "logrotate.timer") {
		t.Error("expected logrotate timer")
	}
	if !strings.Contains(out, "[!]") {
		t.Error("expected overdue marker")
	}
	if !strings.Contains(out, "1 timer(s) appear overdue") {
		t.Error("expected overdue summary")
	}
}

func TestFormatTimersEmpty(t *testing.T) {
	out := formatTimers(nil, false)
	if !strings.Contains(out, "No") {
		t.Error("expected empty message")
	}
}

func TestSplitTimerBlocks(t *testing.T) {
	text := "Mon 2024-01-15 02:30:00 UTC 5h left Sun 2024-01-14 02:30:00 UTC 19h ago"
	blocks := splitTimerBlocks(text)
	if len(blocks) != 4 {
		t.Fatalf("expected 4 blocks, got %d: %v", len(blocks), blocks)
	}
}

func TestIsTimezone(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"UTC", true},
		{"CST", true},
		{"EST", true},
		{"utc", false},
		{"Z", true},
		{"TOOLONG", false},
		{"123", false},
	}
	for _, tc := range tests {
		if isTimezone(tc.s) != tc.want {
			t.Errorf("isTimezone(%q)=%v, want %v", tc.s, isTimezone(tc.s), tc.want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
