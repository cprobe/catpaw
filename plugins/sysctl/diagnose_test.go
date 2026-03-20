package sysctl

import (
	"runtime"
	"strings"
	"testing"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &SysctlPlugin{}
	p.RegisterDiagnoseTools(registry)

	for _, name := range []string{"sysctl_snapshot", "sysctl_get"} {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.Scope != diagnose.ToolScopeLocal {
			t.Fatalf("tool %q should be local scope", name)
		}
	}
}

func TestExecSysctlSnapshot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	result, err := execSysctlSnapshot(nil, nil)
	if err != nil {
		t.Fatalf("execSysctlSnapshot: %v", err)
	}
	if !strings.Contains(result, "vm.swappiness") {
		t.Fatalf("expected vm.swappiness in output, got: %s", result)
	}
}

func TestExecSysctlSnapshotCustomKeys(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	result, err := execSysctlSnapshot(nil, map[string]string{"keys": "vm.swappiness, fs.file-max"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "vm.swappiness") || !strings.Contains(result, "fs.file-max") {
		t.Fatalf("expected both keys in output, got: %s", result)
	}
}

func TestExecSysctlGet(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	result, err := execSysctlGet(nil, map[string]string{"key": "vm.swappiness"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.HasPrefix(result, "vm.swappiness = ") {
		t.Fatalf("unexpected output: %s", result)
	}
}

func TestExecSysctlGetMissing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	result, err := execSysctlGet(nil, map[string]string{"key": "nonexistent.param.xxx"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "not found") {
		t.Fatalf("expected 'not found' for missing key, got: %s", result)
	}
}

func TestExecSysctlGetEmptyKey(t *testing.T) {
	_, err := execSysctlGet(nil, map[string]string{"key": ""})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}
