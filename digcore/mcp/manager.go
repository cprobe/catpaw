package mcp

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/diagnose"
)

const (
	categoryPrefix   = "mcp:"
	initTimeout      = 15 * time.Second
	toolsListTimeout = 10 * time.Second
)

// Manager manages the lifecycle of multiple MCP server connections and
// registers their tools into catpaw's ToolRegistry.
type Manager struct {
	mu       sync.RWMutex
	clients  map[string]*Client // name → client
	configs  map[string]*config.MCPServerConfig
	builtins map[string]string // host-level built-in variables, computed once
}

// NewManager creates an unstarted Manager.
func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]*Client),
		configs: make(map[string]*config.MCPServerConfig),
	}
}

// StartAll connects to all configured MCP servers, discovers their tools,
// and registers them into the ToolRegistry.
// Servers that fail to start are logged and skipped (best-effort).
func (m *Manager) StartAll(ctx context.Context, mcpCfg config.MCPConfig, registry *diagnose.ToolRegistry) {
	m.builtins = config.HostBuiltins()
	for i := range mcpCfg.Servers {
		srv := &mcpCfg.Servers[i]
		if srv.Name == "" || srv.Command == "" {
			log.Printf("[mcp] skipping server with empty name or command")
			continue
		}
		if err := m.startServer(ctx, srv, mcpCfg.DefaultIdentity, registry); err != nil {
			log.Printf("[mcp] failed to start %s: %v", srv.Name, err)
		}
	}
}

func (m *Manager) startServer(ctx context.Context, srv *config.MCPServerConfig, defaultIdentity string, registry *diagnose.ToolRegistry) error {
	// Use parent ctx for process lifetime (not a short timeout).
	client, err := NewClient(ctx, srv.Name, srv.Command, srv.Args, srv.Env)
	if err != nil {
		return err
	}

	initCtx, initCancel := context.WithTimeout(ctx, initTimeout)
	defer initCancel()

	if err := client.Initialize(initCtx); err != nil {
		client.Close()
		return fmt.Errorf("initialize: %w", err)
	}

	listCtx, listCancel := context.WithTimeout(ctx, toolsListTimeout)
	defer listCancel()

	tools, err := client.ListTools(listCtx)
	if err != nil {
		client.Close()
		return fmt.Errorf("tools/list: %w", err)
	}

	m.mu.Lock()
	m.clients[srv.Name] = client
	m.configs[srv.Name] = srv
	m.mu.Unlock()

	identity := srv.ResolvedIdentity(defaultIdentity, m.builtins)

	registered := m.registerTools(srv, tools, client, identity, registry)
	log.Printf("[mcp] %s: %d/%d tools registered (identity: %s)", srv.Name, registered, len(tools), identity)
	return nil
}

func (m *Manager) registerTools(srv *config.MCPServerConfig, tools []Tool, client *Client, identity string, registry *diagnose.ToolRegistry) int {
	catName := categoryPrefix + srv.Name

	// Build the tool list first; only register the category if at least one tool passes the filter.
	var toRegister []diagnose.DiagnoseTool
	for _, mcpTool := range tools {
		if !srv.IsToolAllowed(mcpTool.Name) {
			continue
		}

		params := convertParams(mcpTool.ExtractParams())
		toolName := srv.Name + "_" + mcpTool.Name
		captured := mcpTool.Name
		capturedClient := client

		toRegister = append(toRegister, diagnose.DiagnoseTool{
			Name:        toolName,
			Description: mcpTool.Description,
			Parameters:  params,
			Scope:       diagnose.ToolScopeLocal,
			Execute: func(ctx context.Context, args map[string]string) (string, error) {
				return capturedClient.CallTool(ctx, captured, toAnyMap(args))
			},
		})
	}

	if len(toRegister) == 0 {
		return 0
	}

	desc := fmt.Sprintf("External data via MCP server %q", srv.Name)
	if identity != "" {
		desc += fmt.Sprintf(" (this host: %s)", identity)
	}
	registry.RegisterCategory(catName, catName, desc, diagnose.ToolScopeLocal)

	for _, dt := range toRegister {
		registry.Register(catName, dt)
	}
	return len(toRegister)
}

// Close shuts down all MCP server connections.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, c := range m.clients {
		if err := c.Close(); err != nil {
			log.Printf("[mcp] error closing %s: %v", name, err)
		}
	}
	m.clients = make(map[string]*Client)
}

// ServerNames returns sorted names of all connected servers.
func (m *Manager) ServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.clients))
	for n := range m.clients {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ServerCount returns the number of connected servers.
func (m *Manager) ServerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients)
}

// IdentitySummary returns a prompt-friendly summary of all MCP server identities.
func (m *Manager) IdentitySummary(defaultIdentity string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.configs) == 0 {
		return ""
	}

	var b strings.Builder
	names := make([]string, 0, len(m.configs))
	for n := range m.configs {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		cfg := m.configs[n]
		id := cfg.ResolvedIdentity(defaultIdentity, m.builtins)
		if id != "" {
			fmt.Fprintf(&b, "- %s: %s\n", n, id)
		}
	}
	return b.String()
}

// --- helpers ---

func convertParams(mcpParams []ToolParam) []diagnose.ToolParam {
	if len(mcpParams) == 0 {
		return nil
	}
	result := make([]diagnose.ToolParam, len(mcpParams))
	for i, p := range mcpParams {
		result[i] = diagnose.ToolParam{
			Name:        p.Name,
			Type:        p.Type,
			Description: p.Description,
			Required:    p.Required,
		}
	}
	return result
}

func toAnyMap(m map[string]string) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
