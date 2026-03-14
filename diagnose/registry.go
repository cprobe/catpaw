package diagnose

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
)

// ToolRegistry manages all diagnostic tools registered by plugins.
// Thread-safe for concurrent reads; writes happen only at startup.
type ToolRegistry struct {
	mu               sync.RWMutex
	categories       map[string]*ToolCategory
	toolIndex        map[string]*DiagnoseTool          // name → tool (flat index for fast lookup)
	accessorFactory  map[string]AccessorFactory         // plugin → factory
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		categories:      make(map[string]*ToolCategory),
		toolIndex:       make(map[string]*DiagnoseTool),
		accessorFactory: make(map[string]AccessorFactory),
	}
}

// Register adds a tool under the given category. If the category doesn't exist,
// it is created with the provided scope and description.
// Duplicate tool names are logged and skipped (programming error, not runtime condition).
func (r *ToolRegistry) Register(category string, tool DiagnoseTool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.toolIndex[tool.Name]; dup {
		log.Printf("[warn] diagnose: duplicate tool name %q in category %q, skipped", tool.Name, category)
		return
	}

	cat, ok := r.categories[category]
	if !ok {
		cat = &ToolCategory{
			Name:   category,
			Plugin: category,
			Scope:  tool.Scope,
		}
		r.categories[category] = cat
	}
	tp := new(DiagnoseTool)
	*tp = tool
	cat.Tools = append(cat.Tools, *tp)
	r.toolIndex[tool.Name] = tp
}

// RegisterCategory registers or updates a category's metadata.
func (r *ToolRegistry) RegisterCategory(name, plugin, description string, scope ToolScope) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cat, ok := r.categories[name]
	if !ok {
		cat = &ToolCategory{Name: name}
		r.categories[name] = cat
	}
	cat.Plugin = plugin
	cat.Description = description
	cat.Scope = scope
}

// RegisterAccessorFactory registers a factory that creates a shared Accessor
// for remote plugin tools within a DiagnoseSession.
func (r *ToolRegistry) RegisterAccessorFactory(plugin string, factory AccessorFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accessorFactory[plugin] = factory
}

// CreateAccessor calls the registered factory for the given plugin.
func (r *ToolRegistry) CreateAccessor(ctx context.Context, plugin string, instanceRef any) (any, error) {
	r.mu.RLock()
	factory, ok := r.accessorFactory[plugin]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no accessor factory registered for plugin %q", plugin)
	}
	return factory(ctx, instanceRef)
}

// Get returns a tool by name.
func (r *ToolRegistry) Get(name string) (*DiagnoseTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.toolIndex[name]
	return t, ok
}

// ByPlugin returns all tools registered under the given plugin/category name.
func (r *ToolRegistry) ByPlugin(plugin string) []DiagnoseTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cat, ok := r.categories[plugin]
	if !ok {
		return nil
	}
	result := make([]DiagnoseTool, len(cat.Tools))
	copy(result, cat.Tools)
	return result
}

// ByPluginForOS returns tools under the given plugin/category that are supported
// on the specified operating system.
func (r *ToolRegistry) ByPluginForOS(plugin, goos string) []DiagnoseTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cat, ok := r.categories[plugin]
	if !ok || !categorySupportsOS(cat, goos) {
		return nil
	}
	filtered := make([]DiagnoseTool, 0, len(cat.Tools))
	for _, tool := range cat.Tools {
		if tool.SupportsOS(goos) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// ListCategories returns a formatted string of all categories for the AI,
// sorted alphabetically for deterministic output.
func (r *ToolRegistry) ListCategories() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.categories))
	for name := range r.categories {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		cat := r.categories[name]
		desc := cat.Description
		if desc == "" {
			desc = cat.Name + " diagnostics"
		}
		fmt.Fprintf(&b, "%-12s (%d tools) - %s\n", cat.Name, len(cat.Tools), desc)
	}
	return b.String()
}

// ListCategoriesForOS returns a formatted string of categories that support
// the specified operating system.
func (r *ToolRegistry) ListCategoriesForOS(goos string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.categories))
	for name, cat := range r.categories {
		if categorySupportsOS(cat, goos) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		cat := r.categories[name]
		desc := cat.Description
		if desc == "" {
			desc = cat.Name + " diagnostics"
		}
		count := 0
		for _, tool := range cat.Tools {
			if tool.SupportsOS(goos) {
				count++
			}
		}
		if count == 0 {
			continue
		}
		fmt.Fprintf(&b, "%-12s (%d tools) - %s\n", cat.Name, count, desc)
	}
	return b.String()
}

// ListTools returns a formatted string of tools in a category for the AI.
func (r *ToolRegistry) ListTools(category string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cat, ok := r.categories[category]
	if !ok {
		return fmt.Sprintf("unknown category: %q", category)
	}

	var b strings.Builder
	for _, t := range cat.Tools {
		fmt.Fprintf(&b, "%s - %s\n", t.Name, t.Description)
		for _, p := range t.Parameters {
			req := ""
			if p.Required {
				req = " (required)"
			}
			fmt.Fprintf(&b, "  %s (%s): %s%s\n", p.Name, p.Type, p.Description, req)
		}
	}
	return b.String()
}

// ListToolsForOS returns tools in a category filtered by supported OS.
func (r *ToolRegistry) ListToolsForOS(category, goos string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cat, ok := r.categories[category]
	if !ok {
		return fmt.Sprintf("unknown category: %q", category)
	}
	if !categorySupportsOS(cat, goos) {
		return fmt.Sprintf("no tools in category %q support os=%q", category, goos)
	}

	var b strings.Builder
	count := 0
	for _, t := range cat.Tools {
		if !t.SupportsOS(goos) {
			continue
		}
		count++
		fmt.Fprintf(&b, "%s - %s\n", t.Name, t.Description)
		for _, p := range t.Parameters {
			req := ""
			if p.Required {
				req = " (required)"
			}
			fmt.Fprintf(&b, "  %s (%s): %s%s\n", p.Name, p.Type, p.Description, req)
		}
	}
	if count == 0 {
		return fmt.Sprintf("no tools in category %q support os=%q", category, goos)
	}
	return b.String()
}

// Categories returns a sorted snapshot of all category names.
func (r *ToolRegistry) Categories() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.categories))
	for name := range r.categories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ToolCount returns the total number of registered tools.
func (r *ToolRegistry) ToolCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.toolIndex)
}

// CategoriesWithTools returns all categories with their tools, sorted by category name.
func (r *ToolRegistry) CategoriesWithTools() []ToolCategory {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ToolCategory, 0, len(r.categories))
	for _, cat := range r.categories {
		c := ToolCategory{
			Name:        cat.Name,
			Plugin:      cat.Plugin,
			Description: cat.Description,
			Scope:       cat.Scope,
			Tools:       make([]DiagnoseTool, len(cat.Tools)),
		}
		copy(c.Tools, cat.Tools)
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// ListAllTools returns a compact catalog of every registered tool, grouped by
// category and sorted alphabetically. Designed for injection into AI prompts
// so the model can call tools directly without a discovery round-trip.
func (r *ToolRegistry) ListAllTools() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cats := make([]*ToolCategory, 0, len(r.categories))
	for _, cat := range r.categories {
		cats = append(cats, cat)
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Name < cats[j].Name })

	var b strings.Builder
	for _, cat := range cats {
		desc := cat.Description
		if desc == "" {
			desc = cat.Name
		}
		fmt.Fprintf(&b, "[%s] %s\n", cat.Name, desc)
		for _, t := range cat.Tools {
			params := formatParamsCompact(t.Parameters)
			if params != "" {
				fmt.Fprintf(&b, "  %s(%s) - %s\n", t.Name, params, t.Description)
			} else {
				fmt.Fprintf(&b, "  %s() - %s\n", t.Name, t.Description)
			}
		}
	}
	return b.String()
}

// ToolSupportedOn reports whether the named tool should be available on the
// specified operating system.
func (r *ToolRegistry) ToolSupportedOn(name, goos string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.toolIndex[name]
	if !ok || !tool.SupportsOS(goos) {
		return false
	}
	for _, cat := range r.categories {
		for _, t := range cat.Tools {
			if t.Name == name {
				return categorySupportsOS(cat, goos)
			}
		}
	}
	return true
}

func categorySupportsOS(cat *ToolCategory, goos string) bool {
	desc := strings.ToLower(cat.Description)
	switch {
	case strings.Contains(desc, "linux only."):
		return goos == "linux"
	case strings.Contains(desc, "darwin only."), strings.Contains(desc, "macos only."):
		return goos == "darwin"
	case strings.Contains(desc, "windows only."):
		return goos == "windows"
	default:
		return true
	}
}

// ListToolCatalogSmart returns a hybrid catalog for diagnose prompts:
//   - Built-in tool categories: full detail (name, params, description)
//   - MCP categories (prefix "mcp:"): summary only (name, tool count, description)
//
// This keeps the prompt concise when many MCP tools are registered while
// still allowing zero-roundtrip access to built-in tools.
func (r *ToolRegistry) ListToolCatalogSmart() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cats := make([]*ToolCategory, 0, len(r.categories))
	for _, cat := range r.categories {
		cats = append(cats, cat)
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Name < cats[j].Name })

	var b strings.Builder
	for _, cat := range cats {
		desc := cat.Description
		if desc == "" {
			desc = cat.Name
		}

		if strings.HasPrefix(cat.Name, "mcp:") {
			fmt.Fprintf(&b, "[%s] (%d tools) %s — 使用前先 list_tools(category=\"%s\")\n",
				cat.Name, len(cat.Tools), desc, cat.Name)
			continue
		}

		fmt.Fprintf(&b, "[%s] %s\n", cat.Name, desc)
		for _, t := range cat.Tools {
			params := formatParamsCompact(t.Parameters)
			if params != "" {
				fmt.Fprintf(&b, "  %s(%s) - %s\n", t.Name, params, t.Description)
			} else {
				fmt.Fprintf(&b, "  %s() - %s\n", t.Name, t.Description)
			}
		}
	}
	return b.String()
}

// ListToolCatalogSmartForOS returns the tool catalog filtered by supported OS.
func (r *ToolRegistry) ListToolCatalogSmartForOS(goos string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cats := make([]*ToolCategory, 0, len(r.categories))
	for _, cat := range r.categories {
		cats = append(cats, cat)
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Name < cats[j].Name })

	var b strings.Builder
	for _, cat := range cats {
		if !categorySupportsOS(cat, goos) {
			continue
		}
		desc := cat.Description
		if desc == "" {
			desc = cat.Name
		}

		filtered := make([]DiagnoseTool, 0, len(cat.Tools))
		for _, t := range cat.Tools {
			if t.SupportsOS(goos) {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			continue
		}

		if strings.HasPrefix(cat.Name, "mcp:") {
			fmt.Fprintf(&b, "[%s] (%d tools) %s — 使用前先 list_tools(category=\"%s\")\n",
				cat.Name, len(filtered), desc, cat.Name)
			continue
		}

		fmt.Fprintf(&b, "[%s] %s\n", cat.Name, desc)
		for _, t := range filtered {
			params := formatParamsCompact(t.Parameters)
			if params != "" {
				fmt.Fprintf(&b, "  %s(%s) - %s\n", t.Name, params, t.Description)
			} else {
				fmt.Fprintf(&b, "  %s() - %s\n", t.Name, t.Description)
			}
		}
	}
	return b.String()
}

func formatParamsCompact(params []ToolParam) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		s := p.Name
		if p.Required {
			s += "*"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// HasAccessorFactory reports whether a plugin has a registered accessor factory.
func (r *ToolRegistry) HasAccessorFactory(plugin string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.accessorFactory[plugin]
	return ok
}

