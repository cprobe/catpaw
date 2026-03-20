package sysdiag

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/cmdx"
)

const (
	fwTimeout   = 15 * time.Second
	fwMaxOutput = 128 * 1024
)

func registerFirewall(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_firewall", "sysdiag:firewall",
		"Firewall diagnostic tools (iptables/nftables summary). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_firewall", diagnose.DiagnoseTool{
		Name:        "firewall_summary",
		Description: "Show firewall rules summary: chain counts, DROP/REJECT rules, packet/byte counters. Tries nftables first, falls back to iptables.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "table", Type: "string", Description: "Table to inspect (default 'filter'). For iptables: filter/nat/mangle/raw. For nft: inet/ip/ip6 filter."},
		},
		Execute: execFirewallSummary,
	})
}

type fwChainInfo struct {
	name       string
	policy     string
	ruleCount  int
	dropCount  int
	rejectCount int
}

func execFirewallSummary(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("firewall_summary requires linux (current: %s)", runtime.GOOS)
	}

	table := strings.TrimSpace(args["table"])
	if table == "" {
		table = "filter"
	}

	validTables := map[string]bool{"filter": true, "nat": true, "mangle": true, "raw": true, "security": true}
	if !validTables[table] {
		return "", fmt.Errorf("invalid table %q (valid: filter, nat, mangle, raw, security)", table)
	}

	out, src, err := tryFirewall(ctx, table)
	if err != nil {
		return "", err
	}

	return formatFirewallOutput(out, src, table), nil
}

func tryFirewall(ctx context.Context, table string) (string, string, error) {
	if out, err := runNFT(ctx); err == nil && out != "" {
		return out, "nftables", nil
	}

	if out, err := runIptables(ctx, table); err == nil && out != "" {
		return out, "iptables", nil
	}

	return "", "", fmt.Errorf("neither nft nor iptables available or accessible (may need root)")
}

func runNFT(ctx context.Context) (string, error) {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return "", err
	}

	var outBuf cappedBuf
	outBuf.buf = bytes.NewBuffer(make([]byte, 0, 4096))
	outBuf.max = fwMaxOutput

	cmd := exec.CommandContext(ctx, nft, "list", "ruleset")
	cmd.Stdout = &outBuf
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err, _ := cmdx.RunTimeout(cmd, fwTimeout); err != nil {
		return "", fmt.Errorf("nft: %w (%s)", err, strings.TrimSpace(stderrBuf.String()))
	}
	return outBuf.buf.String(), nil
}

func runIptables(ctx context.Context, table string) (string, error) {
	ipt, err := exec.LookPath("iptables")
	if err != nil {
		return "", err
	}

	var outBuf cappedBuf
	outBuf.buf = bytes.NewBuffer(make([]byte, 0, 4096))
	outBuf.max = fwMaxOutput

	cmd := exec.CommandContext(ctx, ipt, "-t", table, "-L", "-n", "-v", "--line-numbers")
	cmd.Stdout = &outBuf
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err, _ := cmdx.RunTimeout(cmd, fwTimeout); err != nil {
		return "", fmt.Errorf("iptables: %w (%s)", err, strings.TrimSpace(stderrBuf.String()))
	}
	return outBuf.buf.String(), nil
}

func formatFirewallOutput(raw, source, table string) string {
	var b strings.Builder
	if source == "nftables" {
		fmt.Fprintf(&b, "Firewall Summary (source: %s, full ruleset)\n", source)
	} else {
		fmt.Fprintf(&b, "Firewall Summary (source: %s, table: %s)\n", source, table)
	}
	b.WriteString(strings.Repeat("=", 50))
	b.WriteString("\n\n")

	if source == "iptables" {
		chains := parseIptablesChains(raw)
		formatIptablesChains(&b, chains)
	} else {
		formatNFTSummary(&b, raw)
	}

	return b.String()
}

func parseIptablesChains(raw string) []fwChainInfo {
	var chains []fwChainInfo
	var current *fwChainInfo

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "Chain ") {
			if current != nil {
				chains = append(chains, *current)
			}
			current = parseIptablesChainHeader(line)
			continue
		}

		if current == nil {
			continue
		}

		if strings.HasPrefix(line, "num") || strings.HasPrefix(line, "pkts") {
			continue
		}

		current.ruleCount++
		upper := strings.ToUpper(line)
		if strings.Contains(upper, " DROP ") || strings.Contains(upper, " DROP") {
			current.dropCount++
		}
		if strings.Contains(upper, " REJECT ") || strings.Contains(upper, " REJECT") {
			current.rejectCount++
		}
	}

	if current != nil {
		chains = append(chains, *current)
	}

	return chains
}

func parseIptablesChainHeader(line string) *fwChainInfo {
	// "Chain INPUT (policy ACCEPT 12345 packets, 67890 bytes)"
	// "Chain DOCKER (0 references)"
	parts := strings.Fields(line)
	info := &fwChainInfo{}
	if len(parts) >= 2 {
		info.name = parts[1]
	}
	if idx := strings.Index(line, "policy "); idx >= 0 {
		rest := line[idx+len("policy "):]
		if sp := strings.IndexAny(rest, " )"); sp > 0 {
			info.policy = rest[:sp]
		} else {
			info.policy = strings.TrimRight(rest, ")")
		}
	}
	return info
}

func formatIptablesChains(b *strings.Builder, chains []fwChainInfo) {
	if len(chains) == 0 {
		b.WriteString("No chains found (table may be empty or inaccessible).\n")
		return
	}

	totalRules := 0
	totalDrop := 0
	totalReject := 0

	fmt.Fprintf(b, "%-20s %-10s %6s %6s %6s\n", "CHAIN", "POLICY", "RULES", "DROP", "REJECT")
	b.WriteString(strings.Repeat("-", 52))
	b.WriteString("\n")

	for _, c := range chains {
		policy := c.policy
		if policy == "" {
			policy = "-"
		}
		marker := ""
		if policy == "DROP" || policy == "REJECT" {
			marker = " [!]"
		}
		fmt.Fprintf(b, "%-20s %-10s %6d %6d %6d%s\n",
			truncStr(c.name, 20), policy, c.ruleCount, c.dropCount, c.rejectCount, marker)
		totalRules += c.ruleCount
		totalDrop += c.dropCount
		totalReject += c.rejectCount
	}

	b.WriteString(strings.Repeat("-", 52))
	b.WriteString("\n")
	fmt.Fprintf(b, "Total: %d chains, %d rules (%d DROP, %d REJECT)\n",
		len(chains), totalRules, totalDrop, totalReject)

	if totalDrop == 0 && totalReject == 0 {
		b.WriteString("\n[info] No DROP/REJECT rules found. Firewall may be permissive.\n")
	}
}

func formatNFTSummary(b *strings.Builder, raw string) {
	type nftTable struct {
		family string
		name   string
		chains []string
		rules  int
		drops  int
	}

	var tables []nftTable
	var cur *nftTable
	var curChain string
	depth := 0

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "table ") && strings.HasSuffix(trimmed, "{") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 3 {
				cur = &nftTable{family: parts[1], name: strings.TrimSuffix(parts[2], "{")}
				cur.name = strings.TrimSpace(cur.name)
			}
			depth = 1
			continue
		}

		if cur == nil {
			continue
		}

		if strings.HasPrefix(trimmed, "chain ") && strings.HasSuffix(trimmed, "{") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				curChain = strings.TrimSuffix(parts[1], "{")
				curChain = strings.TrimSpace(curChain)
				cur.chains = append(cur.chains, curChain)
			}
			depth++
			continue
		}

		if trimmed == "}" {
			depth--
			if depth <= 0 && cur != nil {
				tables = append(tables, *cur)
				cur = nil
				curChain = ""
				depth = 0
			} else {
				curChain = ""
			}
			continue
		}

		if curChain != "" && !strings.HasPrefix(trimmed, "type ") && !strings.HasPrefix(trimmed, "policy ") {
			cur.rules++
			upper := strings.ToUpper(trimmed)
			if strings.Contains(upper, "DROP") || strings.Contains(upper, "REJECT") {
				cur.drops++
			}
		}
	}

	if len(tables) == 0 {
		b.WriteString("nftables: no tables defined (empty ruleset).\n")
		return
	}

	sort.Slice(tables, func(i, j int) bool {
		if tables[i].family != tables[j].family {
			return tables[i].family < tables[j].family
		}
		return tables[i].name < tables[j].name
	})

	for _, t := range tables {
		fmt.Fprintf(b, "Table: %s %s (%d chains, %d rules", t.family, t.name, len(t.chains), t.rules)
		if t.drops > 0 {
			fmt.Fprintf(b, ", %d drop/reject", t.drops)
		}
		b.WriteString(")\n")
		for _, ch := range t.chains {
			fmt.Fprintf(b, "  chain: %s\n", ch)
		}
		b.WriteString("\n")
	}
}

func truncStr(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
