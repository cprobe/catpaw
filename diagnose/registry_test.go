package diagnose

import (
	"context"
	"strings"
	"testing"
)

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewToolRegistry()

	r.RegisterCategory("redis", "redis", "Redis diagnostics", ToolScopeRemote)
	r.Register("redis", DiagnoseTool{
		Name:        "redis_info",
		Description: "Get Redis INFO",
		Scope:       ToolScopeRemote,
		Parameters:  []ToolParam{{Name: "section", Type: "string", Description: "INFO section"}},
	})
	r.Register("redis", DiagnoseTool{
		Name:        "redis_slowlog",
		Description: "Get Redis SLOWLOG",
		Scope:       ToolScopeRemote,
	})

	if r.ToolCount() != 2 {
		t.Fatalf("ToolCount() = %d, want 2", r.ToolCount())
	}

	tool, ok := r.Get("redis_info")
	if !ok {
		t.Fatal("Get(redis_info) not found")
	}
	if tool.Name != "redis_info" {
		t.Errorf("Name = %q, want redis_info", tool.Name)
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("Get(nonexistent) should return false")
	}

	tools := r.ByPlugin("redis")
	if len(tools) != 2 {
		t.Fatalf("ByPlugin(redis) len = %d, want 2", len(tools))
	}

	tools = r.ByPlugin("unknown")
	if len(tools) != 0 {
		t.Fatalf("ByPlugin(unknown) len = %d, want 0", len(tools))
	}
}

func TestRegistryDuplicateTool(t *testing.T) {
	r := NewToolRegistry()
	r.Register("a", DiagnoseTool{Name: "tool1", Scope: ToolScopeLocal})
	r.Register("b", DiagnoseTool{Name: "tool1", Scope: ToolScopeLocal})
	if r.ToolCount() != 1 {
		t.Fatalf("expected 1 tool (duplicate skipped), got %d", r.ToolCount())
	}
}

func TestRegistryListCategories(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterCategory("disk", "disk", "Disk diagnostics", ToolScopeLocal)
	r.Register("disk", DiagnoseTool{Name: "disk_iostat", Description: "IO stats", Scope: ToolScopeLocal})
	r.Register("disk", DiagnoseTool{Name: "disk_usage", Description: "Disk usage", Scope: ToolScopeLocal})

	out := r.ListCategories()
	if !strings.Contains(out, "disk") || !strings.Contains(out, "2 tools") {
		t.Errorf("ListCategories() = %q, expected disk with 2 tools", out)
	}
}

func TestRegistryListAllTools(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterCategory("cpu", "cpu", "CPU diagnostics", ToolScopeLocal)
	r.Register("cpu", DiagnoseTool{
		Name:        "cpu_usage",
		Description: "Show CPU usage",
		Scope:       ToolScopeLocal,
	})
	r.RegisterCategory("disk", "disk", "Disk diagnostics", ToolScopeLocal)
	r.Register("disk", DiagnoseTool{
		Name:        "disk_usage",
		Description: "Show disk usage",
		Scope:       ToolScopeLocal,
		Parameters:  []ToolParam{{Name: "path", Type: "string", Description: "Mount path", Required: true}},
	})

	out := r.ListAllTools()

	if !strings.Contains(out, "[cpu]") {
		t.Error("expected [cpu] category header")
	}
	if !strings.Contains(out, "[disk]") {
		t.Error("expected [disk] category header")
	}
	if !strings.Contains(out, "cpu_usage()") {
		t.Error("expected cpu_usage() with no params")
	}
	if !strings.Contains(out, "disk_usage(path*)") {
		t.Error("expected disk_usage(path*) with required param marker")
	}

	// Verify alphabetical ordering: cpu before disk
	cpuIdx := strings.Index(out, "[cpu]")
	diskIdx := strings.Index(out, "[disk]")
	if cpuIdx >= diskIdx {
		t.Errorf("expected cpu before disk, got cpu@%d disk@%d", cpuIdx, diskIdx)
	}
}

func TestRegistryListAllToolsEmpty(t *testing.T) {
	r := NewToolRegistry()
	out := r.ListAllTools()
	if out != "" {
		t.Errorf("expected empty string for empty registry, got %q", out)
	}
}

func TestRegistryAccessorFactory(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterAccessorFactory("redis", func(ctx context.Context, insRef any) (any, error) {
		return "mock-accessor", nil
	})

	acc, err := r.CreateAccessor(context.Background(), "redis", nil)
	if err != nil {
		t.Fatal(err)
	}
	if acc != "mock-accessor" {
		t.Errorf("accessor = %v, want mock-accessor", acc)
	}

	_, err = r.CreateAccessor(context.Background(), "unknown", nil)
	if err == nil {
		t.Fatal("expected error for unknown plugin")
	}
}
