package systemd

import (
	"strings"
	"testing"
)

func TestValidPriorities(t *testing.T) {
	valid := []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug", "0", "3", "7"}
	for _, p := range valid {
		if !validPriorities[p] {
			t.Errorf("expected %q to be valid", p)
		}
	}

	invalid := []string{"error", "warn", "8", "-1", "critical"}
	for _, p := range invalid {
		if validPriorities[p] {
			t.Errorf("expected %q to be invalid", p)
		}
	}
}

func TestSinceFormat(t *testing.T) {
	valid := []string{
		"5m ago",
		"1h ago",
		"24h ago",
		"today",
		"yesterday",
		"2026-03-01",
		"2026-03-01 10:00:00",
	}
	for _, s := range valid {
		if !reSinceFormat.MatchString(s) {
			t.Errorf("expected %q to be valid since format", s)
		}
	}

	invalid := []string{
		"; rm -rf /",
		"$(whoami)",
		"`id`",
		"foo bar",
		"--output json",
	}
	for _, s := range invalid {
		if reSinceFormat.MatchString(s) {
			t.Errorf("expected %q to be rejected as since format", s)
		}
	}
}

func TestExecJournalQuery_Validation(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]string
		wantErr string
	}{
		{"empty unit", map[string]string{}, "unit parameter is required"},
		{"invalid unit chars", map[string]string{"unit": "nginx;rm"}, "invalid unit name"},
		{"bad since", map[string]string{"unit": "nginx", "since": "$(evil)"}, "invalid since format"},
		{"bad priority", map[string]string{"unit": "nginx", "priority": "fatal"}, "invalid priority"},
		{"bad max_lines", map[string]string{"unit": "nginx", "max_lines": "-1"}, "invalid max_lines"},
		{"zero max_lines", map[string]string{"unit": "nginx", "max_lines": "0"}, "invalid max_lines"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := execJournalQuery(t.Context(), tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

