package disk

import (
	"context"
	"testing"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &DiskPlugin{}
	p.RegisterDiagnoseTools(registry)

	expected := []string{"disk_overview", "disk_io_counters"}
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

	removed := []string{"disk_usage", "disk_partitions"}
	for _, name := range removed {
		if _, ok := registry.Get(name); ok {
			t.Errorf("tool %s should have been merged into disk_overview", name)
		}
	}

	if registry.ToolCount() != len(expected) {
		t.Errorf("expected %d tools, got %d", len(expected), registry.ToolCount())
	}

	cats := registry.Categories()
	if len(cats) != 1 || cats[0] != "disk" {
		t.Errorf("expected 1 category 'disk', got %v", cats)
	}

	hints := registry.GetDiagnoseHints("disk")
	if hints == "" {
		t.Error("DiagnoseHints for disk should not be empty")
	}
}

func TestPreCollectorRegistered(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &DiskPlugin{}
	p.RegisterDiagnoseTools(registry)

	data := registry.RunPreCollector(context.Background(), "disk", nil)
	if data == "" {
		t.Fatal("disk PreCollector should return non-empty data")
	}

	baseline := registry.RunBaselinePreCollectors(context.Background(), "nonexistent")
	if _, ok := baseline["disk"]; !ok {
		t.Fatal("disk should appear in baseline results")
	}

	baseline2 := registry.RunBaselinePreCollectors(context.Background(), "disk")
	if _, ok := baseline2["disk"]; ok {
		t.Fatal("disk should be excluded when it is the triggering plugin")
	}
}
