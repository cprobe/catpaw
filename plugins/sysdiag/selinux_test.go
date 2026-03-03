package sysdiag

import (
	"strings"
	"testing"
)

func TestExtractDenialLines(t *testing.T) {
	text := `type=AVC msg=audit(1234567890.123:456): avc:  denied  { read } for  pid=1234 comm="httpd"
type=SYSCALL msg=audit(1234567890.123:456): arch=c000003e syscall=2
type=AVC msg=audit(1234567891.123:457): avc:  denied  { write } for  pid=5678 comm="nginx"
type=PATH msg=audit(1234567891.123:457): item=0 name="/etc/passwd"
`
	lines := extractDenialLines(text, 10)
	if len(lines) != 2 {
		t.Fatalf("expected 2 denial lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "denied") {
		t.Error("first line should contain 'denied'")
	}
}

func TestExtractDenialLinesMax(t *testing.T) {
	text := ""
	for i := 0; i < 50; i++ {
		text += "avc:  denied  { read } entry " + string(rune('A'+i%26)) + "\n"
	}
	lines := extractDenialLines(text, 5)
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}
}

func TestExtractLastN(t *testing.T) {
	text := "line1\nline2\nline3\nline4\nline5\n"
	lines := extractLastN(text, 3)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "line3" {
		t.Errorf("expected line3, got %q", lines[0])
	}
}

func TestSummarizeAppArmorText(t *testing.T) {
	raw := `apparmor module is loaded.
45 profiles are loaded.
45 profiles are in enforce mode.
   /sbin/dhclient
   /usr/bin/evince
0 profiles are in complain mode.
2 processes have profiles defined.
2 processes are in enforce mode.
0 processes are unconfined.
`
	result := summarizeAppArmorText(raw)
	if !strings.Contains(result, "45 profiles are loaded") {
		t.Error("expected profiles loaded line")
	}
	if !strings.Contains(result, "2 processes have profiles") {
		t.Error("expected processes line")
	}
	if strings.Contains(result, "/sbin/dhclient") {
		t.Error("should not include individual profile paths")
	}
}

func TestExecSELinuxStatusParamValidation(t *testing.T) {
	tests := []struct {
		val       string
		wantErr   bool
		errSubstr string
	}{
		{"10", false, ""},
		{"abc", true, "non-negative integer"},
		{"0", false, ""},
		{"101", true, "<= 100"},
		{"-5", true, "non-negative integer"},
	}

	for _, tc := range tests {
		args := map[string]string{"max_denials": tc.val}
		_, err := execSELinuxStatus(nil, args)
		if tc.wantErr {
			if err == nil {
				t.Errorf("val=%q: expected error", tc.val)
			} else if !strings.Contains(err.Error(), tc.errSubstr) && !strings.Contains(err.Error(), "linux") {
				t.Errorf("val=%q: err=%q, want containing %q", tc.val, err.Error(), tc.errSubstr)
			}
		} else {
			if err != nil && !strings.Contains(err.Error(), "linux") {
				t.Errorf("val=%q: unexpected error: %v", tc.val, err)
			}
		}
	}
}
