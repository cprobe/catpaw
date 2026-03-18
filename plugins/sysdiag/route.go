package sysdiag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cprobe/digcore/diagnose"
	"github.com/cprobe/digcore/pkg/cmdx"
)

const routeTimeout = 5 * time.Second

func registerRoute(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_route", "sysdiag:route",
		"Routing table diagnostic tools (routes, gateways, metrics). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_route", diagnose.DiagnoseTool{
		Name:        "route_table",
		Description: "Show the IPv4 routing table: destination, gateway, device, metric, scope, protocol. Highlights default route.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "show_v6", Type: "string", Description: "Set to 'true' to also show IPv6 routes (default: false)"},
		},
		Execute: execRouteTable,
	})
}

type routeEntry struct {
	dst       string
	gateway   string
	dev       string
	src       string
	protocol  string
	scope     string
	metric    string
	isDefault bool
}

func execRouteTable(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("route_table requires linux (current: %s)", runtime.GOOS)
	}

	showV6 := strings.ToLower(args["show_v6"]) == "true"

	bin, err := exec.LookPath("ip")
	if err != nil {
		return "", fmt.Errorf("ip command not found: %w", err)
	}

	routes, err := getRoutes(ctx, bin, "-4")
	if err != nil {
		return "", err
	}

	if showV6 {
		v6Routes, err := getRoutes(ctx, bin, "-6")
		if err == nil {
			routes = append(routes, v6Routes...)
		}
	}

	return formatRoutes(routes, showV6), nil
}

func getRoutes(ctx context.Context, bin, family string) ([]routeEntry, error) {
	// Try JSON first
	routes, err := tryRouteJSON(ctx, bin, family)
	if err == nil {
		return routes, nil
	}
	return tryRouteText(ctx, bin, family)
}

type routeJSONEntry struct {
	Dst      string   `json:"dst"`
	Gateway  string   `json:"gateway"`
	Dev      string   `json:"dev"`
	Prefsrc  string   `json:"prefsrc"`
	Protocol string   `json:"protocol"`
	Scope    string   `json:"scope"`
	Metric   int      `json:"metric"`
	Flags    []string `json:"flags"`
}

func tryRouteJSON(ctx context.Context, bin, family string) ([]routeEntry, error) {
	cmd := exec.Command(bin, family, "-j", "route", "show")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	timeout := routeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return nil, fmt.Errorf("ip route timed out")
	}
	if runErr != nil {
		return nil, runErr
	}

	var entries []routeJSONEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		return nil, err
	}

	routes := make([]routeEntry, 0, len(entries))
	for _, e := range entries {
		r := routeEntry{
			dst:       e.Dst,
			gateway:   e.Gateway,
			dev:       e.Dev,
			src:       e.Prefsrc,
			protocol:  e.Protocol,
			scope:     e.Scope,
			isDefault: e.Dst == "default",
		}
		if e.Metric > 0 {
			r.metric = fmt.Sprintf("%d", e.Metric)
		}
		routes = append(routes, r)
	}
	return routes, nil
}

func tryRouteText(ctx context.Context, bin, family string) ([]routeEntry, error) {
	cmd := exec.Command(bin, family, "route", "show")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	timeout := routeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return nil, fmt.Errorf("ip route timed out")
	}
	if runErr != nil {
		return nil, fmt.Errorf("ip route failed: %v (%s)", runErr, strings.TrimSpace(stderr.String()))
	}

	return parseRouteText(stdout.String()), nil
}

// parseRouteText parses `ip route show` text output.
// Example lines:
//
//	default via 10.0.0.1 dev eth0 proto dhcp metric 100
//	10.0.0.0/24 dev eth0 proto kernel scope link src 10.0.0.5
//	172.17.0.0/16 dev docker0 proto kernel scope link src 172.17.0.1 linkdown
func parseRouteText(text string) []routeEntry {
	var routes []routeEntry
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r := parseRouteLine(line)
		routes = append(routes, r)
	}
	return routes
}

func parseRouteLine(line string) routeEntry {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return routeEntry{}
	}

	r := routeEntry{dst: fields[0], isDefault: fields[0] == "default"}

	for i := 1; i < len(fields); i++ {
		switch fields[i] {
		case "via":
			if i+1 < len(fields) {
				r.gateway = fields[i+1]
				i++
			}
		case "dev":
			if i+1 < len(fields) {
				r.dev = fields[i+1]
				i++
			}
		case "proto":
			if i+1 < len(fields) {
				r.protocol = fields[i+1]
				i++
			}
		case "scope":
			if i+1 < len(fields) {
				r.scope = fields[i+1]
				i++
			}
		case "metric":
			if i+1 < len(fields) {
				r.metric = fields[i+1]
				i++
			}
		case "src":
			if i+1 < len(fields) {
				r.src = fields[i+1]
				i++
			}
		}
	}
	return r
}

func formatRoutes(routes []routeEntry, showV6 bool) string {
	if len(routes) == 0 {
		return "No routes found."
	}

	hasDefault := false
	for _, r := range routes {
		if r.isDefault {
			hasDefault = true
			break
		}
	}

	var b strings.Builder
	label := "IPv4"
	if showV6 {
		label = "IPv4+IPv6"
	}
	fmt.Fprintf(&b, "Routing Table (%s): %d routes", label, len(routes))
	if !hasDefault {
		b.WriteString(" [!] NO DEFAULT ROUTE")
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "%-22s  %-16s  %-10s  %-16s  %-8s  %-8s  %s\n",
		"DESTINATION", "GATEWAY", "DEV", "SRC", "PROTO", "SCOPE", "METRIC")
	b.WriteString(strings.Repeat("-", 90))
	b.WriteByte('\n')

	for _, r := range routes {
		gw := r.gateway
		if gw == "" {
			gw = "-"
		}
		src := r.src
		if src == "" {
			src = "-"
		}
		scope := r.scope
		if scope == "" {
			scope = "-"
		}
		proto := r.protocol
		if proto == "" {
			proto = "-"
		}
		metric := r.metric
		if metric == "" {
			metric = "-"
		}
		marker := ""
		if r.isDefault {
			marker = " *"
		}

		fmt.Fprintf(&b, "%-22s  %-16s  %-10s  %-16s  %-8s  %-8s  %s%s\n",
			r.dst, gw, r.dev, src, proto, scope, metric, marker)
	}

	return b.String()
}
