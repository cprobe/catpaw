package scriptfilter

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/pkg/filter"
)

func TestInitRejectsNegativeTimeout(t *testing.T) {
	ins := &Instance{
		Command:       "echo ok",
		FilterInclude: []string{"*ok*"},
		Timeout:       config.Duration(-1 * time.Second),
	}

	if err := ins.Init(); err == nil {
		t.Fatal("expected Init to fail for negative timeout")
	}
}

func TestInitRejectsEmptyFilterInclude(t *testing.T) {
	ins := &Instance{
		Command: "echo ok",
	}

	err := ins.Init()
	if err == nil {
		t.Fatal("expected Init to fail for empty filter_include")
	}
}

func TestInitRejectsInvalidSeverity(t *testing.T) {
	ins := &Instance{
		Command:       "echo ok",
		FilterInclude: []string{"*ok*"},
		Match:         MatchCheck{Severity: "warning"},
	}

	err := ins.Init()
	if err == nil {
		t.Fatal("expected Init to fail for invalid severity")
	}
}

func TestBuildTargetSimple(t *testing.T) {
	cases := []struct {
		command  string
		expected string
	}{
		{"/usr/local/bin/check.sh --run", "check.sh"},
		{"echo hello", "echo"},
		{"\"/opt/my scripts/health.sh\" --check", "health.sh"},
		{"check.sh", "check.sh"},
	}

	for _, tc := range cases {
		got := buildTarget(tc.command)
		if got != tc.expected {
			t.Errorf("buildTarget(%q) = %q, want %q", tc.command, got, tc.expected)
		}
	}
}

func TestInitTrimsCommand(t *testing.T) {
	ins := &Instance{
		Command:       "  /usr/local/bin/check.sh --run  ",
		FilterInclude: []string{"*WARN*"},
	}

	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if ins.Command != "/usr/local/bin/check.sh --run" {
		t.Fatalf("expected trimmed command, got %q", ins.Command)
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected []string
	}{
		{"normal", "line1\nline2\nline3\n", []string{"line1", "line2", "line3"}},
		{"empty lines", "a\n\n\nb\n", []string{"a", "b"}},
		{"empty input", "", nil},
		{"single line no newline", "hello", []string{"hello"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitLines([]byte(tc.input))
			if len(got) != len(tc.expected) {
				t.Fatalf("expected %d lines, got %d: %v", len(tc.expected), len(got), got)
			}
			for i := range got {
				if got[i] != tc.expected[i] {
					t.Errorf("line[%d] = %q, want %q", i, got[i], tc.expected[i])
				}
			}
		})
	}
}

func TestMatchLine(t *testing.T) {
	inc, _ := filter.Compile([]string{"*ERROR*", "/(?i)panic/"})
	exc, _ := filter.Compile([]string{"*ignore*"})

	ins := &Instance{
		includeFilter: inc,
		excludeFilter: exc,
	}

	cases := []struct {
		line    string
		matches bool
	}{
		{"this is an ERROR line", true},
		{"KERNEL PANIC detected", true},
		{"normal log line", false},
		{"ERROR but should ignore this", false},
	}

	for _, tc := range cases {
		got := ins.matchLine(tc.line)
		if got != tc.matches {
			t.Errorf("matchLine(%q) = %v, want %v", tc.line, got, tc.matches)
		}
	}
}

func TestFormatExecError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		stderr   []byte
		contains string
		excludes string
	}{
		{
			name:     "with stderr",
			err:      fmt.Errorf("command not found"),
			stderr:   []byte("  bash: no such file  "),
			contains: "(stderr: bash: no such file)",
		},
		{
			name:     "empty stderr",
			err:      fmt.Errorf("timeout"),
			stderr:   nil,
			excludes: "stderr",
		},
		{
			name:     "long stderr truncated",
			err:      fmt.Errorf("failed"),
			stderr:   []byte(strings.Repeat("x", 300)),
			contains: "...",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatExecError(tc.err, tc.stderr)
			if tc.contains != "" && !strings.Contains(got, tc.contains) {
				t.Errorf("expected %q to contain %q", got, tc.contains)
			}
			if tc.excludes != "" && strings.Contains(got, tc.excludes) {
				t.Errorf("expected %q to NOT contain %q", got, tc.excludes)
			}
		})
	}
}
