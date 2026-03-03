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

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/pkg/cmdx"
)

const ipTimeout = 5 * time.Second

func registerIPAddr(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_ipaddr", "sysdiag:ipaddr",
		"Network interface address tools (IP addresses, MAC, MTU, state). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_ipaddr", diagnose.DiagnoseTool{
		Name:        "ip_addr",
		Description: "Show network interfaces with IP addresses, MAC, MTU, and UP/DOWN state. Highlights interfaces that are DOWN.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "show_lo", Type: "string", Description: "Set to 'true' to include loopback (default: false)"},
		},
		Execute: execIPAddr,
	})
}

type ipInterface struct {
	Name   string
	State  string // UP, DOWN, UNKNOWN
	MAC    string
	MTU    int
	Addrs  []ipAddress
	Flags  string
}

type ipAddress struct {
	Family  string // inet, inet6
	Address string // e.g. "10.0.0.1/24"
	Scope   string // global, link, host
}

func execIPAddr(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("ip_addr requires linux (current: %s)", runtime.GOOS)
	}

	showLo := strings.ToLower(args["show_lo"]) == "true"

	ifaces, err := getIPInterfaces(ctx)
	if err != nil {
		return "", err
	}

	if !showLo {
		filtered := make([]ipInterface, 0, len(ifaces))
		for _, iface := range ifaces {
			if iface.Name != "lo" {
				filtered = append(filtered, iface)
			}
		}
		ifaces = filtered
	}

	return formatIPAddr(ifaces), nil
}

func getIPInterfaces(ctx context.Context) ([]ipInterface, error) {
	bin, err := exec.LookPath("ip")
	if err != nil {
		return nil, fmt.Errorf("ip command not found: %w", err)
	}

	// Try JSON output first (iproute2 >= 4.13)
	ifaces, err := tryIPJSON(ctx, bin)
	if err == nil {
		return ifaces, nil
	}

	return tryIPText(ctx, bin)
}

// ipJSONEntry represents a single interface from `ip -j addr show`
type ipJSONEntry struct {
	IfName   string `json:"ifname"`
	OperState string `json:"operstate"`
	Address  string `json:"address"` // MAC
	Mtu      int    `json:"mtu"`
	Flags    []string `json:"flags"`
	AddrInfo []struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
		Scope     string `json:"scope"`
	} `json:"addr_info"`
}

func tryIPJSON(ctx context.Context, bin string) ([]ipInterface, error) {
	cmd := exec.Command(bin, "-j", "addr", "show")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	timeout := ipTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return nil, fmt.Errorf("ip -j timed out")
	}
	if runErr != nil {
		return nil, fmt.Errorf("ip -j failed: %v", runErr)
	}

	var entries []ipJSONEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		return nil, fmt.Errorf("parse ip json: %w", err)
	}

	ifaces := make([]ipInterface, 0, len(entries))
	for _, e := range entries {
		iface := ipInterface{
			Name:  e.IfName,
			State: strings.ToUpper(e.OperState),
			MAC:   e.Address,
			MTU:   e.Mtu,
			Flags: strings.Join(e.Flags, ","),
		}
		for _, ai := range e.AddrInfo {
			iface.Addrs = append(iface.Addrs, ipAddress{
				Family:  ai.Family,
				Address: fmt.Sprintf("%s/%d", ai.Local, ai.PrefixLen),
				Scope:   ai.Scope,
			})
		}
		ifaces = append(ifaces, iface)
	}
	return ifaces, nil
}

func tryIPText(ctx context.Context, bin string) ([]ipInterface, error) {
	cmd := exec.Command(bin, "addr", "show")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	timeout := ipTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return nil, fmt.Errorf("ip addr show timed out")
	}
	if runErr != nil {
		return nil, fmt.Errorf("ip addr show failed: %v (%s)", runErr, strings.TrimSpace(stderr.String()))
	}

	return parseIPAddrText(stdout.String()), nil
}

func parseIPAddrText(text string) []ipInterface {
	var ifaces []ipInterface
	var current *ipInterface

	for _, line := range strings.Split(text, "\n") {
		if len(line) == 0 {
			continue
		}

		// New interface starts with a number: "2: eth0: <BROADCAST,...> mtu 1500 ... state UP"
		if len(line) > 0 && line[0] >= '0' && line[0] <= '9' {
			if current != nil {
				ifaces = append(ifaces, *current)
			}
			current = parseInterfaceHeader(line)
			continue
		}

		if current == nil {
			continue
		}

		trimmed := strings.TrimSpace(line)

		// "link/ether aa:bb:cc:dd:ee:ff brd ff:ff:ff:ff:ff:ff"
		if strings.HasPrefix(trimmed, "link/ether") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				current.MAC = fields[1]
			}
		}

		// "inet 10.0.0.1/24 ..." or "inet6 fe80::1/64 ..."
		if strings.HasPrefix(trimmed, "inet6 ") || strings.HasPrefix(trimmed, "inet ") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				family := fields[0]
				addr := fields[1]
				scope := ""
				for i, f := range fields {
					if f == "scope" && i+1 < len(fields) {
						scope = fields[i+1]
						break
					}
				}
				current.Addrs = append(current.Addrs, ipAddress{
					Family:  family,
					Address: addr,
					Scope:   scope,
				})
			}
		}
	}
	if current != nil {
		ifaces = append(ifaces, *current)
	}
	return ifaces
}

func parseInterfaceHeader(line string) *ipInterface {
	// "2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 ... state UP ..."
	iface := &ipInterface{}

	colonParts := strings.SplitN(line, ":", 3)
	if len(colonParts) < 3 {
		return iface
	}
	iface.Name = strings.TrimSpace(colonParts[1])

	rest := colonParts[2]

	// Extract flags from <...>
	if start := strings.IndexByte(rest, '<'); start >= 0 {
		if end := strings.IndexByte(rest[start:], '>'); end >= 0 {
			iface.Flags = rest[start+1 : start+end]
		}
	}

	fields := strings.Fields(rest)
	for i, f := range fields {
		switch f {
		case "mtu":
			if i+1 < len(fields) {
				fmt.Sscanf(fields[i+1], "%d", &iface.MTU)
			}
		case "state":
			if i+1 < len(fields) {
				iface.State = strings.ToUpper(fields[i+1])
			}
		}
	}

	if iface.State == "" {
		if strings.Contains(iface.Flags, "UP") {
			iface.State = "UP"
		} else {
			iface.State = "DOWN"
		}
	}

	return iface
}

func formatIPAddr(ifaces []ipInterface) string {
	if len(ifaces) == 0 {
		return "No network interfaces found."
	}

	downCount := 0
	for _, iface := range ifaces {
		if iface.State == "DOWN" {
			downCount++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Network Interfaces: %d", len(ifaces))
	if downCount > 0 {
		fmt.Fprintf(&b, " [!] %d DOWN", downCount)
	}
	b.WriteString("\n\n")

	for _, iface := range ifaces {
		marker := ""
		if iface.State == "DOWN" {
			marker = " [!]"
		}
		fmt.Fprintf(&b, "%s: %s%s", iface.Name, iface.State, marker)
		if iface.MTU > 0 {
			fmt.Fprintf(&b, "  mtu %d", iface.MTU)
		}
		if iface.MAC != "" {
			fmt.Fprintf(&b, "  mac %s", iface.MAC)
		}
		b.WriteByte('\n')

		if len(iface.Addrs) == 0 {
			b.WriteString("    (no addresses)\n")
		}
		for _, addr := range iface.Addrs {
			scope := ""
			if addr.Scope != "" {
				scope = " scope " + addr.Scope
			}
			fmt.Fprintf(&b, "    %s %s%s\n", addr.Family, addr.Address, scope)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
