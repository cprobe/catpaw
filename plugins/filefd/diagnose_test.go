package filefd

import (
	"runtime"
	"strings"
	"testing"

	"github.com/cprobe/catpaw/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &FilefdPlugin{}
	p.RegisterDiagnoseTools(registry)

	expectedTools := []string{"filefd_usage", "filefd_top_procs"}
	for _, name := range expectedTools {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.Scope != diagnose.ToolScopeLocal {
			t.Fatalf("tool %q should be local scope", name)
		}
	}

	cats := registry.Categories()
	if len(cats) != 1 || cats[0] != "filefd" {
		t.Fatalf("expected single category 'filefd', got %v", cats)
	}
}

func TestExecFilefdUsage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	result, err := execFilefdUsage(nil, nil)
	if err != nil {
		t.Fatalf("execFilefdUsage: %v", err)
	}

	if !strings.Contains(result, "Allocated:") || !strings.Contains(result, "Max:") {
		t.Fatalf("unexpected output: %s", result)
	}
}

func TestExecFilefdTopProcs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	result, err := execFilefdTopProcs(nil, map[string]string{"count": "3"})
	if err != nil {
		t.Fatalf("execFilefdTopProcs: %v", err)
	}

	if !strings.Contains(result, "PID") || !strings.Contains(result, "FDs") {
		t.Fatalf("unexpected output: %s", result)
	}
}

func TestExecFilefdTopProcsInvalidCount(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	_, err := execFilefdTopProcs(nil, map[string]string{"count": "abc"})
	if err == nil {
		t.Fatal("expected error for invalid count")
	}
}
