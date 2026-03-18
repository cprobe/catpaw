package tcpstate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cprobe/digcore/diagnose"
	"github.com/cprobe/digcore/plugins"
	gonet "github.com/shirou/gopsutil/v3/net"
)

var _ plugins.Diagnosable = (*TcpstatePlugin)(nil)

func (p *TcpstatePlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("tcpstate", "tcpstate",
		"TCP/socket diagnostic tools (state distribution, top ports, socket stats)", diagnose.ToolScopeLocal)

	registry.Register("tcpstate", diagnose.DiagnoseTool{
		Name:        "tcp_state_distribution",
		Description: "Show full TCP connection state distribution (ESTABLISHED, CLOSE_WAIT, TIME_WAIT, FIN_WAIT, etc.) with counts",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			conns, err := gonet.ConnectionsWithContext(ctx, "tcp")
			if err != nil {
				return "", fmt.Errorf("get tcp connections: %w", err)
			}

			stateCounts := make(map[string]int)
			for _, c := range conns {
				state := c.Status
				if state == "" {
					state = "UNKNOWN"
				}
				stateCounts[state]++
			}

			orderedStates := []string{
				"LISTEN", "ESTABLISHED", "SYN_SENT", "SYN_RECV",
				"FIN_WAIT1", "FIN_WAIT2", "TIME_WAIT",
				"CLOSE_WAIT", "LAST_ACK", "CLOSING", "CLOSE",
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Total TCP connections: %d\n\n", len(conns))
			fmt.Fprintf(&b, "%-16s  %s\n", "STATE", "COUNT")
			for _, state := range orderedStates {
				if count, ok := stateCounts[state]; ok {
					fmt.Fprintf(&b, "%-16s  %d\n", state, count)
					delete(stateCounts, state)
				}
			}
			for state, count := range stateCounts {
				fmt.Fprintf(&b, "%-16s  %d\n", state, count)
			}
			return b.String(), nil
		},
	})

	registry.Register("tcpstate", diagnose.DiagnoseTool{
		Name:        "top_connections_by_port",
		Description: "Show top remote ports by connection count, useful for identifying connection-heavy targets. Optional parameter: state (e.g. CLOSE_WAIT, TIME_WAIT)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "state", Type: "string", Description: "Filter by TCP state (e.g. CLOSE_WAIT, TIME_WAIT, ESTABLISHED). Empty means all.", Required: false},
		},
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			conns, err := gonet.ConnectionsWithContext(ctx, "tcp")
			if err != nil {
				return "", fmt.Errorf("get tcp connections: %w", err)
			}

			filterState := strings.ToUpper(args["state"])

			type portKey struct {
				ip   string
				port uint32
			}
			portCounts := make(map[portKey]int)
			matched := 0
			for _, c := range conns {
				if c.Status == "LISTEN" {
					continue
				}
				if filterState != "" && c.Status != filterState {
					continue
				}
				if c.Raddr.Port > 0 {
					key := portKey{ip: c.Raddr.IP, port: c.Raddr.Port}
					portCounts[key]++
					matched++
				}
			}

			type entry struct {
				key   portKey
				count int
			}
			var entries []entry
			for k, v := range portCounts {
				entries = append(entries, entry{k, v})
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })

			var b strings.Builder
			if filterState != "" {
				fmt.Fprintf(&b, "Connections in state %s: %d\n\n", filterState, matched)
			} else {
				fmt.Fprintf(&b, "Non-LISTEN connections: %d\n\n", matched)
			}

			fmt.Fprintf(&b, "%-24s  %s\n", "REMOTE ENDPOINT", "COUNT")
			limit := 20
			if limit > len(entries) {
				limit = len(entries)
			}
			for i := 0; i < limit; i++ {
				e := entries[i]
				fmt.Fprintf(&b, "%-24s  %d\n", fmt.Sprintf("%s:%d", e.key.ip, e.key.port), e.count)
			}
			if len(entries) > limit {
				fmt.Fprintf(&b, "\n... and %d more unique endpoints\n", len(entries)-limit)
			}
			return b.String(), nil
		},
	})

	registry.Register("tcpstate", diagnose.DiagnoseTool{
		Name:        "top_connections_by_local_port",
		Description: "Show top local (listening) ports by inbound connection count, useful for finding overloaded services",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			conns, err := gonet.ConnectionsWithContext(ctx, "tcp")
			if err != nil {
				return "", fmt.Errorf("get tcp connections: %w", err)
			}

			listenPorts := make(map[uint32]bool)
			for _, c := range conns {
				if c.Status == "LISTEN" {
					listenPorts[c.Laddr.Port] = true
				}
			}

			portCounts := make(map[uint32]int)
			portStates := make(map[uint32]map[string]int)
			for _, c := range conns {
				if c.Status == "LISTEN" {
					continue
				}
				port := c.Laddr.Port
				if !listenPorts[port] {
					continue
				}
				portCounts[port]++
				if portStates[port] == nil {
					portStates[port] = make(map[string]int)
				}
				portStates[port][c.Status]++
			}

			type entry struct {
				port  uint32
				count int
			}
			var entries []entry
			for port, count := range portCounts {
				entries = append(entries, entry{port, count})
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })

			var b strings.Builder
			fmt.Fprintf(&b, "%-8s  %8s  %s\n", "PORT", "CONNS", "STATE BREAKDOWN")
			limit := 20
			if limit > len(entries) {
				limit = len(entries)
			}
			for i := 0; i < limit; i++ {
				e := entries[i]
				states := portStates[e.port]
				var parts []string
				for s, n := range states {
					parts = append(parts, fmt.Sprintf("%s:%d", s, n))
				}
				sort.Strings(parts)
				fmt.Fprintf(&b, "%-8d  %8d  %s\n", e.port, e.count, strings.Join(parts, " "))
			}
			return b.String(), nil
		},
	})
}
