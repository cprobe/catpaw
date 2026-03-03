package sysdiag

import (
	"strings"
	"testing"
)

func TestParseIptablesChains(t *testing.T) {
	raw := `Chain INPUT (policy ACCEPT 12345 packets, 67890 bytes)
 pkts bytes target     prot opt in     out     source               destination
 1234   56K DROP       all  --  *      *       10.0.0.0/8           0.0.0.0/0
  567  123K ACCEPT     tcp  --  *      *       0.0.0.0/0            0.0.0.0/0            tcp dpt:22
  890   45K REJECT     all  --  *      *       192.168.0.0/16       0.0.0.0/0            reject-with icmp-port-unreachable

Chain FORWARD (policy DROP 0 packets, 0 bytes)
 pkts bytes target     prot opt in     out     source               destination

Chain OUTPUT (policy ACCEPT 0 packets, 0 bytes)
 pkts bytes target     prot opt in     out     source               destination
`
	chains := parseIptablesChains(raw)
	if len(chains) != 3 {
		t.Fatalf("expected 3 chains, got %d", len(chains))
	}

	input := chains[0]
	if input.name != "INPUT" {
		t.Errorf("chain[0].name=%q, want INPUT", input.name)
	}
	if input.policy != "ACCEPT" {
		t.Errorf("chain[0].policy=%q, want ACCEPT", input.policy)
	}
	if input.ruleCount != 3 {
		t.Errorf("chain[0].ruleCount=%d, want 3", input.ruleCount)
	}
	if input.dropCount != 1 {
		t.Errorf("chain[0].dropCount=%d, want 1", input.dropCount)
	}
	if input.rejectCount != 1 {
		t.Errorf("chain[0].rejectCount=%d, want 1", input.rejectCount)
	}

	forward := chains[1]
	if forward.policy != "DROP" {
		t.Errorf("FORWARD policy=%q, want DROP", forward.policy)
	}
	if forward.ruleCount != 0 {
		t.Errorf("FORWARD rules=%d, want 0", forward.ruleCount)
	}
}

func TestParseIptablesChainHeader(t *testing.T) {
	tests := []struct {
		line   string
		name   string
		policy string
	}{
		{"Chain INPUT (policy ACCEPT 12345 packets, 67890 bytes)", "INPUT", "ACCEPT"},
		{"Chain FORWARD (policy DROP 0 packets, 0 bytes)", "FORWARD", "DROP"},
		{"Chain DOCKER (0 references)", "DOCKER", ""},
		{"Chain OUTPUT (policy ACCEPT)", "OUTPUT", "ACCEPT"},
	}
	for _, tc := range tests {
		info := parseIptablesChainHeader(tc.line)
		if info.name != tc.name {
			t.Errorf("line=%q: name=%q, want %q", tc.line, info.name, tc.name)
		}
		if info.policy != tc.policy {
			t.Errorf("line=%q: policy=%q, want %q", tc.line, info.policy, tc.policy)
		}
	}
}

func TestFormatIptablesChains(t *testing.T) {
	chains := []fwChainInfo{
		{name: "INPUT", policy: "ACCEPT", ruleCount: 5, dropCount: 2, rejectCount: 1},
		{name: "FORWARD", policy: "DROP", ruleCount: 0},
		{name: "OUTPUT", policy: "ACCEPT", ruleCount: 1},
	}
	var b strings.Builder
	formatIptablesChains(&b, chains)
	out := b.String()

	if !strings.Contains(out, "INPUT") {
		t.Error("expected INPUT chain")
	}
	if !strings.Contains(out, "[!]") {
		t.Error("expected [!] marker for DROP policy")
	}
	if !strings.Contains(out, "Total: 3 chains") {
		t.Error("expected total summary")
	}
}

func TestFormatNFTSummary(t *testing.T) {
	raw := `table inet filter {
	chain input {
		type filter hook input priority filter; policy accept;
		ct state established,related accept
		iif "lo" accept
		tcp dport 22 accept
		drop
	}
	chain forward {
		type filter hook forward priority filter; policy drop;
	}
	chain output {
		type filter hook output priority filter; policy accept;
	}
}
`
	var b strings.Builder
	formatNFTSummary(&b, raw)
	out := b.String()

	if !strings.Contains(out, "inet") {
		t.Error("expected inet family")
	}
	if !strings.Contains(out, "filter") {
		t.Error("expected filter table")
	}
	if !strings.Contains(out, "3 chains") {
		t.Error("expected 3 chains count")
	}
	if !strings.Contains(out, "drop/reject") {
		t.Error("expected drop/reject count")
	}
}

func TestFormatNFTSummaryEmpty(t *testing.T) {
	var b strings.Builder
	formatNFTSummary(&b, "")
	if !strings.Contains(b.String(), "empty ruleset") {
		t.Error("expected empty ruleset message")
	}
}

func TestTruncStr(t *testing.T) {
	if truncStr("short", 10) != "short" {
		t.Error("short string should not be truncated")
	}
	r := truncStr("verylongchainname", 10)
	if len([]rune(r)) > 10 {
		t.Errorf("truncated string too long: %q (%d runes)", r, len([]rune(r)))
	}
}
