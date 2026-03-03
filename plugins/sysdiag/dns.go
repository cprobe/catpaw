package sysdiag

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/cprobe/catpaw/diagnose"
)

const dnsTimeout = 10 * time.Second

func registerDNS(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_net", "sysdiag:network",
		"Network diagnostic tools (DNS, ping, traceroute).",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_net", diagnose.DiagnoseTool{
		Name:        "dns_resolve",
		Description: "Resolve a domain name and show all returned addresses with timing",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "domain", Type: "string", Description: "Domain name to resolve", Required: true},
			{Name: "server", Type: "string", Description: "DNS server (e.g. '8.8.8.8'). If empty, uses system resolver."},
		},
		Execute: execDnsResolve,
	})
}

func execDnsResolve(ctx context.Context, args map[string]string) (string, error) {
	domain := strings.TrimSpace(args["domain"])
	if domain == "" {
		return "", fmt.Errorf("domain parameter is required")
	}

	server := strings.TrimSpace(args["server"])

	resolveCtx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()

	var resolver *net.Resolver
	if server != "" {
		if !strings.Contains(server, ":") {
			server = server + ":53"
		}
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: dnsTimeout}
				return d.DialContext(ctx, "udp", server)
			},
		}
	} else {
		resolver = net.DefaultResolver
	}

	start := time.Now()
	addrs, err := resolver.LookupHost(resolveCtx, domain)
	elapsed := time.Since(start)

	var b strings.Builder
	fmt.Fprintf(&b, "Domain:  %s\n", domain)
	if server != "" {
		fmt.Fprintf(&b, "Server:  %s\n", server)
	} else {
		b.WriteString("Server:  system resolver\n")
	}
	fmt.Fprintf(&b, "Time:    %s\n", elapsed.Truncate(time.Microsecond))

	if err != nil {
		fmt.Fprintf(&b, "Error:   %v\n", err)
		return b.String(), nil
	}

	fmt.Fprintf(&b, "Results: %d addresses\n\n", len(addrs))
	for _, addr := range addrs {
		fmt.Fprintf(&b, "  %s\n", addr)
	}
	return b.String(), nil
}
