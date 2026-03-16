package cpu

import (
	"context"
	"testing"

	"github.com/cprobe/catpaw/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &CpuPlugin{}
	p.RegisterDiagnoseTools(registry)

	expected := []string{"cpu_overview", "top_cpu_processes"}
	for _, name := range expected {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		if tool.Scope != diagnose.ToolScopeLocal {
			t.Errorf("tool %s scope = %d, want ToolScopeLocal", name, tool.Scope)
		}
		if tool.Execute == nil {
			t.Errorf("tool %s has nil Execute", name)
		}
	}

	removed := []string{"cpu_usage", "cpu_load_average"}
	for _, name := range removed {
		if _, ok := registry.Get(name); ok {
			t.Errorf("tool %s should have been merged into cpu_overview", name)
		}
	}

	if registry.ToolCount() != len(expected) {
		t.Errorf("expected %d tools, got %d", len(expected), registry.ToolCount())
	}

	cats := registry.Categories()
	if len(cats) != 1 || cats[0] != "cpu" {
		t.Errorf("expected 1 category 'cpu', got %v", cats)
	}

	hints := registry.GetDiagnoseHints("cpu")
	if hints == "" {
		t.Error("DiagnoseHints for cpu should not be empty")
	}
}

func TestPreCollectorRegistered(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &CpuPlugin{}
	p.RegisterDiagnoseTools(registry)

	data := registry.RunPreCollector(context.Background(), "cpu", nil)
	if data == "" {
		t.Fatal("cpu PreCollector should return non-empty data")
	}

	baseline := registry.RunBaselinePreCollectors(context.Background(), "nonexistent")
	if _, ok := baseline["cpu"]; !ok {
		t.Fatal("cpu should appear in baseline results")
	}

	baseline2 := registry.RunBaselinePreCollectors(context.Background(), "cpu")
	if _, ok := baseline2["cpu"]; ok {
		t.Fatal("cpu should be excluded when it is the triggering plugin")
	}
}
