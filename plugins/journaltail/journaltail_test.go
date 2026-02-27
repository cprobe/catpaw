package journaltail

import (
	"testing"

	"flashcat.cloud/catpaw/pkg/filter"
)

func TestExtractCursor(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		expected string
	}{
		{
			name:     "normal output with cursor",
			output:   "Jul 28 15:08:04 host systemd[1]: Started nginx.\n-- cursor: s=abc123;i=1;b=def456;m=789;t=012;x=345\n",
			expected: "s=abc123;i=1;b=def456;m=789;t=012;x=345",
		},
		{
			name:     "no cursor in output",
			output:   "Jul 28 15:08:04 host systemd[1]: Started nginx.\n",
			expected: "",
		},
		{
			name:     "empty output",
			output:   "",
			expected: "",
		},
		{
			name:     "cursor with trailing whitespace",
			output:   "-- cursor: s=abc123  \n",
			expected: "s=abc123",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractCursor([]byte(tc.output))
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestExtractLines(t *testing.T) {
	output := "line1\nline2\n\n-- cursor: s=abc\nline3\n"
	lines := extractLines([]byte(output))

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "line1" || lines[1] != "line2" || lines[2] != "line3" {
		t.Fatalf("unexpected lines: %v", lines)
	}
}

func TestBuildArgs(t *testing.T) {
	ins := &Instance{
		Units:    []string{"nginx", "sshd"},
		Priority: "emerg..err",
		cursor:   "s=abc123",
	}

	args := ins.buildArgs()

	hasAfterCursor := false
	hasUnit := 0
	hasPriority := false
	hasShowCursor := false

	for i, a := range args {
		switch a {
		case "--after-cursor":
			hasAfterCursor = true
			if i+1 < len(args) && args[i+1] != "s=abc123" {
				t.Fatalf("expected cursor value 's=abc123', got %q", args[i+1])
			}
		case "--unit":
			hasUnit++
		case "--priority":
			hasPriority = true
			if i+1 < len(args) && args[i+1] != "emerg..err" {
				t.Fatalf("expected priority 'emerg..err', got %q", args[i+1])
			}
		case "--show-cursor":
			hasShowCursor = true
		}
	}

	if !hasAfterCursor {
		t.Fatal("missing --after-cursor")
	}
	if hasUnit != 2 {
		t.Fatalf("expected 2 --unit args, got %d", hasUnit)
	}
	if !hasPriority {
		t.Fatal("missing --priority")
	}
	if !hasShowCursor {
		t.Fatal("missing --show-cursor")
	}
}

func TestBuildArgsFirstRun(t *testing.T) {
	ins := &Instance{
		Timeout: 30_000_000_000,
	}

	args := ins.buildArgs()

	hasSince := false
	hasAfterCursor := false
	for _, a := range args {
		if a == "--since" {
			hasSince = true
		}
		if a == "--after-cursor" {
			hasAfterCursor = true
		}
	}

	if !hasSince {
		t.Fatal("first run should use --since")
	}
	if hasAfterCursor {
		t.Fatal("first run should not use --after-cursor")
	}
}

func TestBuildTarget(t *testing.T) {
	cases := []struct {
		name     string
		ins      Instance
		expected string
	}{
		{
			name:     "with units",
			ins:      Instance{Units: []string{"nginx", "sshd"}},
			expected: "nginx,sshd",
		},
		{
			name:     "with filter_include glob",
			ins:      Instance{FilterInclude: []string{"*error*", "*fail*"}},
			expected: "*error*(+1)",
		},
		{
			name:     "with filter_include regex",
			ins:      Instance{FilterInclude: []string{"/(?i)error/"}},
			expected: "/(?i)error/",
		},
		{
			name:     "fallback",
			ins:      Instance{FilterExclude: []string{"*ignore*"}},
			expected: "journaltail",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ins.buildTarget()
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestMatchLine(t *testing.T) {
	ins := &Instance{}

	// include: glob "*error*" OR regex /(?i)panic/
	inc, err := filter.Compile([]string{"*error*", "/(?i)panic/"})
	if err != nil {
		t.Fatal(err)
	}
	ins.includeFilter = inc

	// exclude: glob "*ignore*"
	exc, err := filter.Compile([]string{"*ignore*"})
	if err != nil {
		t.Fatal(err)
	}
	ins.excludeFilter = exc

	cases := []struct {
		line    string
		matches bool
	}{
		{"this is an error line", true},
		{"KERNEL PANIC detected", true},
		{"normal log line", false},
		{"error but should ignore this", false},
	}

	for _, tc := range cases {
		got := ins.matchLine(tc.line)
		if got != tc.matches {
			t.Errorf("matchLine(%q) = %v, want %v", tc.line, got, tc.matches)
		}
	}
}

func TestCompileMixedPatterns(t *testing.T) {
	// Glob and regex patterns mixed in the same list
	f, err := filter.Compile([]string{"*error*", "/(?i)panic|segfault/"})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		input   string
		matches bool
	}{
		{"an error occurred", true},
		{"KERNEL PANIC", true},
		{"Segfault at 0x0", true},
		{"normal log", false},
	}

	for _, tc := range cases {
		got := f.Match(tc.input)
		if got != tc.matches {
			t.Errorf("Match(%q) = %v, want %v", tc.input, got, tc.matches)
		}
	}
}

func TestCompileRegexOnly(t *testing.T) {
	f, err := filter.Compile([]string{"/^\\d{4}-\\d{2}-\\d{2}/"})
	if err != nil {
		t.Fatal(err)
	}

	if !f.Match("2026-02-26 log entry") {
		t.Error("expected date-prefixed line to match")
	}
	if f.Match("no date here") {
		t.Error("expected non-date line to not match")
	}
}

func TestCompileInvalidRegex(t *testing.T) {
	_, err := filter.Compile([]string{"/(?P<invalid/"})
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}
