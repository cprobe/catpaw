package sysdiag

import (
	"testing"
)

func TestParseDurationToSeconds(t *testing.T) {
	tests := []struct {
		input string
		want  int
		err   bool
	}{
		{"5m", 300, false},
		{"1h", 3600, false},
		{"30s", 30, false},
		{"0s", 0, true},
		{"-5m", 0, true},
		{"invalid", 0, true},
		{"200h", 0, true}, // exceeds 7 day limit
	}
	for _, tt := range tests {
		got, err := parseDurationToSeconds(tt.input)
		if tt.err && err == nil {
			t.Errorf("parseDurationToSeconds(%q): expected error", tt.input)
		}
		if !tt.err && err != nil {
			t.Errorf("parseDurationToSeconds(%q): unexpected error: %v", tt.input, err)
		}
		if !tt.err && got != tt.want {
			t.Errorf("parseDurationToSeconds(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestIsValidDmesgLevel(t *testing.T) {
	for _, level := range []string{"emerg", "alert", "crit", "err", "warn", "notice", "info", "debug"} {
		if !isValidDmesgLevel(level) {
			t.Errorf("isValidDmesgLevel(%q) = false, want true", level)
		}
	}
	if isValidDmesgLevel("invalid") {
		t.Error("isValidDmesgLevel(\"invalid\") = true, want false")
	}
}
