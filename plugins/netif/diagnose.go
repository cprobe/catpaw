package netif

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/plugins"
	gonet "github.com/shirou/gopsutil/v3/net"
)

var _ plugins.Diagnosable = (*NetifPlugin)(nil)

func (p *NetifPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("network", "netif",
		"Network diagnostic tools (interfaces, connections, listen ports)", diagnose.ToolScopeLocal)

	registry.Register("network", diagnose.DiagnoseTool{
		Name:        "network_interfaces",
		Description: "Show all network interfaces with I/O stats: bytes, packets, errors, drops (rx and tx)",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			counters, err := gonet.IOCountersWithContext(ctx, true)
			if err != nil {
				return "", fmt.Errorf("get IO counters: %w", err)
			}

			var b strings.Builder
			fmt.Fprintf(&b, "%-16s  %12s  %12s  %10s  %10s  %8s  %8s  %8s  %8s\n",
				"INTERFACE", "RX_BYTES", "TX_BYTES", "RX_PKTS", "TX_PKTS", "RX_ERR", "TX_ERR", "RX_DROP", "TX_DROP")
			for _, c := range counters {
				fmt.Fprintf(&b, "%-16s  %12s  %12s  %10d  %10d  %8d  %8d  %8d  %8d\n",
					c.Name,
					humanBytes(c.BytesRecv), humanBytes(c.BytesSent),
					c.PacketsRecv, c.PacketsSent,
					c.Errin, c.Errout,
					c.Dropin, c.Dropout)
			}
			return b.String(), nil
		},
	})

	registry.Register("network", diagnose.DiagnoseTool{
		Name:        "network_connections_summary",
		Description: "Show summary of network connections grouped by TCP state (ESTABLISHED, CLOSE_WAIT, TIME_WAIT, etc.)",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			conns, err := gonet.ConnectionsWithContext(ctx, "all")
			if err != nil {
				return "", fmt.Errorf("get connections: %w", err)
			}

			stateCounts := make(map[string]int)
			typeCounts := make(map[string]int)
			for _, c := range conns {
				state := c.Status
				if state == "" {
					state = "NONE"
				}
				stateCounts[state]++
				switch c.Type {
				case 1:
					typeCounts["tcp"]++
				case 2:
					typeCounts["udp"]++
				default:
					typeCounts["other"]++
				}
			}

			type kv struct {
				k string
				v int
			}
			var states []kv
			for k, v := range stateCounts {
				states = append(states, kv{k, v})
			}
			sort.Slice(states, func(i, j int) bool { return states[i].v > states[j].v })

			var b strings.Builder
			fmt.Fprintf(&b, "Total connections: %d (tcp: %d, udp: %d, other: %d)\n\n",
				len(conns), typeCounts["tcp"], typeCounts["udp"], typeCounts["other"])
			fmt.Fprintf(&b, "%-20s  %s\n", "STATE", "COUNT")
			for _, s := range states {
				fmt.Fprintf(&b, "%-20s  %d\n", s.k, s.v)
			}
			return b.String(), nil
		},
	})

	registry.Register("network", diagnose.DiagnoseTool{
		Name:        "network_listen_ports",
		Description: "Show all listening TCP and UDP ports with the owning process PID and name",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			conns, err := gonet.ConnectionsWithContext(ctx, "all")
			if err != nil {
				return "", fmt.Errorf("get connections: %w", err)
			}

			type listener struct {
				proto string
				addr  string
				port  uint32
				pid   int32
			}

			var listeners []listener
			for _, c := range conns {
				if c.Status != "LISTEN" && c.Type != 2 {
					continue
				}
				if c.Status == "LISTEN" || (c.Type == 2 && c.Laddr.Port > 0 && c.Raddr.Port == 0) {
					proto := "tcp"
					if c.Type == 2 {
						proto = "udp"
					}
					listeners = append(listeners, listener{
						proto: proto,
						addr:  c.Laddr.IP,
						port:  c.Laddr.Port,
						pid:   c.Pid,
					})
				}
			}

			sort.Slice(listeners, func(i, j int) bool {
				if listeners[i].port != listeners[j].port {
					return listeners[i].port < listeners[j].port
				}
				return listeners[i].proto < listeners[j].proto
			})

			seen := make(map[string]bool)
			var b strings.Builder
			fmt.Fprintf(&b, "%-6s  %-24s  %7s\n", "PROTO", "LISTEN ADDRESS", "PID")
			for _, l := range listeners {
				key := fmt.Sprintf("%s:%s:%d", l.proto, l.addr, l.port)
				if seen[key] {
					continue
				}
				seen[key] = true
				fmt.Fprintf(&b, "%-6s  %-24s  %7d\n",
					l.proto, fmt.Sprintf("%s:%d", l.addr, l.port), l.pid)
			}
			return b.String(), nil
		},
	})
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
