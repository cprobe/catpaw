package sysdiag

import (
	"strings"
	"testing"
)

func TestExtractRTTFromInfo(t *testing.T) {
	tests := []struct {
		info string
		want float64
	}{
		{"cubic wscale:7,7 rto:204 rtt:1.5/0.75 ato:40 mss:1448 pmtu:1500 rcvmss:536 advmss:1448 cwnd:10", 1.5},
		{"rtt:0.045/0.022 mss:1448", 0.045},
		{"rtt:250.5/125.2", 250.5},
		{"no rtt here", 0},
		{"", 0},
	}
	for _, tc := range tests {
		got := extractRTTFromInfo(tc.info)
		if got != tc.want {
			t.Errorf("extractRTTFromInfo(%q) = %f, want %f", tc.info[:min(40, len(tc.info))], got, tc.want)
		}
	}
}

func TestParseSSRTT(t *testing.T) {
	raw := `ESTAB  0      0      10.0.0.1:45678   10.0.0.2:3306
	 cubic wscale:7,7 rto:204 rtt:2.5/1.25 ato:40 mss:1448
ESTAB  0      0      10.0.0.1:45679   10.0.0.2:3306
	 cubic wscale:7,7 rto:204 rtt:3.0/1.5 ato:40 mss:1448
ESTAB  0      0      10.0.0.1:45680   10.0.0.3:6379
	 cubic wscale:7,7 rto:204 rtt:0.5/0.25 ato:40 mss:1448
`
	conns := parseSSRTT(raw)
	if len(conns) != 3 {
		t.Fatalf("expected 3 connections, got %d", len(conns))
	}
	if conns[0].rttMs != 2.5 {
		t.Errorf("conn[0].rttMs=%f, want 2.5", conns[0].rttMs)
	}
	if conns[2].remote != "10.0.0.3:6379" {
		t.Errorf("conn[2].remote=%q, want 10.0.0.3:6379", conns[2].remote)
	}
}

func TestGroupByRemote(t *testing.T) {
	conns := []connRTT{
		{remote: "10.0.0.2:3306", rttMs: 2.5},
		{remote: "10.0.0.2:3306", rttMs: 3.0},
		{remote: "10.0.0.3:6379", rttMs: 0.5},
	}
	groups := groupByRemote(conns)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}

	// Sorted by max descending
	if groups[0].remote != "10.0.0.2:3306" {
		t.Errorf("groups[0].remote=%q, want 10.0.0.2:3306", groups[0].remote)
	}
	if groups[0].count != 2 {
		t.Errorf("groups[0].count=%d, want 2", groups[0].count)
	}
	if groups[0].max != 3.0 {
		t.Errorf("groups[0].max=%f, want 3.0", groups[0].max)
	}
}

func TestFormatConnLatency(t *testing.T) {
	groups := []rttGroup{
		{remote: "10.0.0.2:3306", count: 10, sum: 1500, max: 500, values: []float64{150}},
		{remote: "10.0.0.3:6379", count: 5, sum: 2.5, max: 0.8, values: []float64{0.5, 0.8}},
	}
	out := formatConnLatency(groups, 30)
	if !strings.Contains(out, "10.0.0.2:3306") {
		t.Error("expected mysql endpoint")
	}
	if !strings.Contains(out, "[!!!]") {
		t.Error("expected high latency marker for 500ms")
	}
}

func TestFormatConnLatencyEmpty(t *testing.T) {
	out := formatConnLatency(nil, 30)
	if !strings.Contains(out, "No established") {
		t.Error("expected empty message")
	}
}

func TestStripPort(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"10.0.0.1:3306", "10.0.0.1:3306"},
		{"[::1]:8080", "[::1]:8080"},
		{"invalid", "invalid"},
	}
	for _, tc := range tests {
		got := stripPort(tc.input)
		if got != tc.want {
			t.Errorf("stripPort(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
