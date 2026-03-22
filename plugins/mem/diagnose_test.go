package mem

import (
	"context"
	"testing"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &MemPlugin{}
	p.RegisterDiagnoseTools(registry)

	expected := []string{"mem_overview", "top_mem_processes"}
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

	removed := []string{"mem_usage", "swap_usage"}
	for _, name := range removed {
		if _, ok := registry.Get(name); ok {
			t.Errorf("tool %s should have been merged into mem_overview", name)
		}
	}

	if registry.ToolCount() != len(expected) {
		t.Errorf("expected %d tools, got %d", len(expected), registry.ToolCount())
	}

	cats := registry.Categories()
	if len(cats) != 1 || cats[0] != "mem" {
		t.Errorf("expected 1 category 'mem', got %v", cats)
	}

	hints := registry.GetDiagnoseHints("mem")
	if hints == "" {
		t.Error("DiagnoseHints for mem should not be empty")
	}
}

func TestPreCollectorRegistered(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &MemPlugin{}
	p.RegisterDiagnoseTools(registry)

	data := registry.RunPreCollector(context.Background(), "mem", nil)
	if data == "" {
		t.Fatal("mem PreCollector should return non-empty data")
	}

	baseline := registry.RunBaselinePreCollectors(context.Background(), "nonexistent")
	if _, ok := baseline["mem"]; !ok {
		t.Fatal("mem should appear in baseline results")
	}

	baseline2 := registry.RunBaselinePreCollectors(context.Background(), "mem")
	if _, ok := baseline2["mem"]; ok {
		t.Fatal("mem should be excluded when it is the triggering plugin")
	}
}
