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
	r.RegisterAccessorFactory("redis", func(ctx context.Context, insRef any, target string) (any, error) {
		return "mock-accessor-" + target, nil
	})

	acc, err := r.CreateAccessor(context.Background(), "redis", nil, "10.0.0.1:6379")
	if err != nil {
		t.Fatal(err)
	}
	if acc != "mock-accessor-10.0.0.1:6379" {
		t.Errorf("accessor = %v, want mock-accessor-10.0.0.1:6379", acc)
	}

	_, err = r.CreateAccessor(context.Background(), "unknown", nil, "")
	if err == nil {
		t.Fatal("expected error for unknown plugin")
	}
}

func TestRegistryBaselinePlugins(t *testing.T) {
	r := NewToolRegistry()

	r.RegisterPreCollector("cpu", func(ctx context.Context, _ any) string { return "cpu data" })
	r.RegisterPreCollector("mem", func(ctx context.Context, _ any) string { return "mem data" })
	r.RegisterPreCollector("disk", func(ctx context.Context, _ any) string { return "disk data" })

	r.RegisterBaselinePlugin("cpu")
	r.RegisterBaselinePlugin("mem")
	r.RegisterBaselinePlugin("disk")
	r.RegisterBaselinePlugin("cpu") // duplicate should be ignored

	baseline := r.RunBaselinePreCollectors(context.Background(), "")
	if len(baseline) != 3 {
		t.Fatalf("expected 3 baseline entries, got %d", len(baseline))
	}

	baseline2 := r.RunBaselinePreCollectors(context.Background(), "mem")
	if len(baseline2) != 2 {
		t.Fatalf("expected 2 baseline entries (mem excluded), got %d", len(baseline2))
	}
	if _, ok := baseline2["mem"]; ok {
		t.Fatal("mem should be excluded")
	}
	if baseline2["cpu"] != "cpu data" {
		t.Fatalf("cpu baseline mismatch: %q", baseline2["cpu"])
	}
}

func TestRegistryPreCollectorNoAccessor(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterPreCollector("cpu", func(ctx context.Context, _ any) string { return "ok" })

	data := r.RunPreCollector(context.Background(), "cpu", nil)
	if data != "ok" {
		t.Fatalf("local PreCollector should work with nil accessor, got %q", data)
	}
}

func TestRegistryFiltersLinuxOnlyToolsOnDarwin(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterCategory("systemd", "systemd", "Systemd diagnostic tools. Linux only.", ToolScopeLocal)
	r.Register("systemd", DiagnoseTool{
		Name:        "service_list_failed",
		Description: "List failed units",
		Scope:       ToolScopeLocal,
	})
	r.RegisterCategory("mem", "mem", "Memory diagnostics", ToolScopeLocal)
	r.Register("mem", DiagnoseTool{
		Name:        "mem_usage",
		Description: "Memory usage",
		Scope:       ToolScopeLocal,
	})

	if got := r.ByPluginForOS("systemd", "darwin"); len(got) != 0 {
		t.Fatalf("ByPluginForOS(systemd, darwin) len = %d, want 0", len(got))
	}
	if got := r.ByPluginForOS("mem", "darwin"); len(got) != 1 {
		t.Fatalf("ByPluginForOS(mem, darwin) len = %d, want 1", len(got))
	}
	if r.ToolSupportedOn("service_list_failed", "darwin") {
		t.Fatal("service_list_failed should be filtered on darwin")
	}
	if !r.ToolSupportedOn("mem_usage", "darwin") {
		t.Fatal("mem_usage should remain available on darwin")
	}

	cats := r.ListCategoriesForOS("darwin")
	if strings.Contains(cats, "systemd") {
		t.Fatalf("ListCategoriesForOS(darwin) should not include systemd: %q", cats)
	}

	catalog := r.ListToolCatalogSmartForOS("darwin")
	if strings.Contains(catalog, "service_list_failed") {
		t.Fatalf("ListToolCatalogSmartForOS(darwin) should not include linux-only tool: %q", catalog)
	}
	if !strings.Contains(catalog, "mem_usage") {
		t.Fatalf("ListToolCatalogSmartForOS(darwin) should include mem_usage: %q", catalog)
	}
}
