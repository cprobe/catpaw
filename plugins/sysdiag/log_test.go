package sysdiag

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTailFile(t *testing.T) {
	tmp := writeTempFile(t, "line1\nline2\nline3\nline4\nline5\n")
	defer os.Remove(tmp)

	result, err := tailFile(context.Background(), tmp, 3, nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "line3") || !strings.Contains(result, "line4") || !strings.Contains(result, "line5") {
		t.Fatalf("expected last 3 lines, got: %s", result)
	}
	if strings.Contains(result, "line1") {
		t.Fatalf("line1 should not be in last 3 lines")
	}
}

func TestTailFileWithFilter(t *testing.T) {
	tmp := writeTempFile(t, "INFO ok\nERROR bad1\nINFO ok2\nERROR bad2\nINFO ok3\n")
	defer os.Remove(tmp)

	re := regexp.MustCompile("ERROR")
	result, err := tailFile(context.Background(), tmp, 10, re)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "ERROR bad1") || !strings.Contains(result, "ERROR bad2") {
		t.Fatalf("expected ERROR lines, got: %s", result)
	}
	if strings.Contains(result, "INFO") {
		t.Fatalf("INFO lines should be filtered out")
	}
}

func TestTailFileEmpty(t *testing.T) {
	tmp := writeTempFile(t, "")
	defer os.Remove(tmp)

	result, err := tailFile(context.Background(), tmp, 10, nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "empty") {
		t.Fatalf("expected empty message, got: %s", result)
	}
}

func TestTailFileRingBuffer(t *testing.T) {
	var lines strings.Builder
	for i := 0; i < 1000; i++ {
		lines.WriteString("line\n")
	}
	tmp := writeTempFile(t, lines.String())
	defer os.Remove(tmp)

	result, err := tailFile(context.Background(), tmp, 5, nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	count := strings.Count(result, "line\n")
	if count != 5 {
		t.Fatalf("expected 5 lines, got %d in output: %s", count, result)
	}
}

func TestGrepFile(t *testing.T) {
	tmp := writeTempFile(t, "info: ok\nerror: bad1\ninfo: ok2\nerror: bad2\ninfo: ok3\n")
	defer os.Remove(tmp)

	re := regexp.MustCompile("error")
	result, err := grepFile(context.Background(), tmp, re, 10)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "matched: 2") {
		t.Fatalf("expected 2 matches, got: %s", result)
	}
}

func TestExecLogTailMissingFile(t *testing.T) {
	_, err := execLogTail(context.Background(), map[string]string{"file": "/nonexistent/path"})
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestExecLogGrepInvalidPattern(t *testing.T) {
	_, err := execLogGrep(context.Background(), map[string]string{
		"file":    "/tmp/test",
		"pattern": "[invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestSanitizeLogPathTraversal(t *testing.T) {
	_, err := sanitizeLogPath("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	_, err = sanitizeLogPath("/var/log/../etc/shadow")
	if err == nil {
		t.Fatal("expected error for embedded ..")
	}
}

func TestSanitizeLogPathEmpty(t *testing.T) {
	_, err := sanitizeLogPath("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestSanitizeLogPathValid(t *testing.T) {
	p, err := sanitizeLogPath("/var/log/syslog")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != "/var/log/syslog" {
		t.Fatalf("expected /var/log/syslog, got %s", p)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "sysdiag-test-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	return f.Name()
}
