package diagnose

import (
	"context"
	"fmt"
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
func (r *ToolRegistry) Register(category string, tool DiagnoseTool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.toolIndex[tool.Name]; dup {
		return fmt.Errorf("duplicate tool name: %q", tool.Name)
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
	cat.Tools = append(cat.Tools, tool)
	r.toolIndex[tool.Name] = &cat.Tools[len(cat.Tools)-1]
	return nil
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
func (r *ToolRegistry) CreateAccessor(plugin string, ctx context.Context, instanceRef any) (any, error) {
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

// ListCategories returns a formatted string of all categories for the AI.
func (r *ToolRegistry) ListCategories() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder
	for _, cat := range r.categories {
		desc := cat.Description
		if desc == "" {
			desc = cat.Name + " diagnostics"
		}
		fmt.Fprintf(&b, "%-12s (%d tools) - %s\n", cat.Name, len(cat.Tools), desc)
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

// Categories returns a snapshot of all category names.
func (r *ToolRegistry) Categories() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.categories))
	for name := range r.categories {
		names = append(names, name)
	}
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

