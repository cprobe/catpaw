package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanCoredumpDirs(t *testing.T) {
	// Save original dirs and restore
	orig := coredumpDirs
	defer func() { coredumpDirs = orig }()

	dir := t.TempDir()
	coredumpDirs = []string{dir}

	// Empty dir
	out := scanCoredumpDirs(20)
	if !strings.Contains(out, "No coredumps found") {
		t.Fatal("expected 'No coredumps found' for empty dir")
	}

	// Create some core files
	for _, name := range []string{"core.nginx.1234.xz", "core.java.5678.lz4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("dummy"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	out = scanCoredumpDirs(20)
	if !strings.Contains(out, "2 found") {
		t.Fatal("expected '2 found' in output")
	}
	if !strings.Contains(out, "core.nginx") {
		t.Fatal("expected core.nginx in output")
	}
}

func TestScanCoredumpDirsMaxEntries(t *testing.T) {
	orig := coredumpDirs
	defer func() { coredumpDirs = orig }()

	dir := t.TempDir()
	coredumpDirs = []string{dir}

	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, "core."+strings.Repeat("a", i+1)+".xz")
		if err := os.WriteFile(name, []byte("dummy"), 0644); err != nil {
			t.Fatal(err)
		}
		// Stagger modification times
		ts := time.Now().Add(time.Duration(-i) * time.Minute)
		os.Chtimes(name, ts, ts)
	}

	out := scanCoredumpDirs(3)
	if !strings.Contains(out, "5 found") {
		t.Fatal("expected '5 found' total count")
	}
	if !strings.Contains(out, "showing 3") {
		t.Fatal("expected truncation notice")
	}
}

func TestExecCoredumpList_Validation(t *testing.T) {
	_, err := execCoredumpList(t.Context(), map[string]string{"max_entries": "abc"})
	if err == nil || !strings.Contains(err.Error(), "invalid max_entries") {
		t.Fatalf("expected 'invalid max_entries' error, got: %v", err)
	}
}
