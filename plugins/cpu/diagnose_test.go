package cpu

import (
	"testing"

	"github.com/cprobe/catpaw/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &CpuPlugin{}
	p.RegisterDiagnoseTools(registry)

	expected := []string{"cpu_usage", "cpu_load_average", "top_cpu_processes"}
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
	if len(cats) != 1 || cats[0] != "cpu" {
		t.Errorf("expected 1 category 'cpu', got %v", cats)
	}
}
