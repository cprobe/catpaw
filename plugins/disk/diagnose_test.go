package disk

import (
	"testing"

	"github.com/cprobe/catpaw/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &DiskPlugin{}
	p.RegisterDiagnoseTools(registry)

	expected := []string{"disk_usage", "disk_partitions", "disk_io_counters"}
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

	cats := registry.Categories()
	if len(cats) != 1 || cats[0] != "disk" {
		t.Errorf("expected 1 category 'disk', got %v", cats)
	}
}
