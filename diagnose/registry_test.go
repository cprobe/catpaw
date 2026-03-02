package diagnose

import (
	"context"
	"strings"
	"testing"
)

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewToolRegistry()

	r.RegisterCategory("redis", "redis", "Redis diagnostics", ToolScopeRemote)
	err := r.Register("redis", DiagnoseTool{
		Name:        "redis_info",
		Description: "Get Redis INFO",
		Scope:       ToolScopeRemote,
		Parameters:  []ToolParam{{Name: "section", Type: "string", Description: "INFO section"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = r.Register("redis", DiagnoseTool{
		Name:        "redis_slowlog",
		Description: "Get Redis SLOWLOG",
		Scope:       ToolScopeRemote,
	})
	if err != nil {
		t.Fatal(err)
	}

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
	_ = r.Register("a", DiagnoseTool{Name: "tool1", Scope: ToolScopeLocal})
	err := r.Register("b", DiagnoseTool{Name: "tool1", Scope: ToolScopeLocal})
	if err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestRegistryListCategories(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterCategory("disk", "disk", "Disk diagnostics", ToolScopeLocal)
	_ = r.Register("disk", DiagnoseTool{Name: "disk_iostat", Description: "IO stats", Scope: ToolScopeLocal})
	_ = r.Register("disk", DiagnoseTool{Name: "disk_usage", Description: "Disk usage", Scope: ToolScopeLocal})

	out := r.ListCategories()
	if !strings.Contains(out, "disk") || !strings.Contains(out, "2 tools") {
		t.Errorf("ListCategories() = %q, expected disk with 2 tools", out)
	}
}

func TestRegistryAccessorFactory(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterAccessorFactory("redis", func(ctx context.Context, insRef any) (any, error) {
		return "mock-accessor", nil
	})

	acc, err := r.CreateAccessor("redis", context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if acc != "mock-accessor" {
		t.Errorf("accessor = %v, want mock-accessor", acc)
	}

	_, err = r.CreateAccessor("unknown", context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for unknown plugin")
	}
}
