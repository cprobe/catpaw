package sockstat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSNMP(t *testing.T) {
	content := `Ip: Forwarding DefaultTTL InReceives InHdrErrors
Ip: 1 64 123456 0
Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts
Tcp: 1 200 120000 -1 5000 3000 100 50 42 900000 850000 1200 5 300
Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors
Udp: 50000 10 3 45000 1 0
`
	dir := t.TempDir()
	path := filepath.Join(dir, "snmp")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	sections, err := parseSNMP(path)
	if err != nil {
		t.Fatalf("parseSNMP: %v", err)
	}

	tcp, ok := sections["Tcp"]
	if !ok {
		t.Fatal("expected Tcp section")
	}
	if tcp["RetransSegs"] != 1200 {
		t.Errorf("RetransSegs = %d, want 1200", tcp["RetransSegs"])
	}
	if tcp["CurrEstab"] != 42 {
		t.Errorf("CurrEstab = %d, want 42", tcp["CurrEstab"])
	}

	udp, ok := sections["Udp"]
	if !ok {
		t.Fatal("expected Udp section")
	}
	if udp["InErrors"] != 3 {
		t.Errorf("InErrors = %d, want 3", udp["InErrors"])
	}
	if udp["RcvbufErrors"] != 1 {
		t.Errorf("RcvbufErrors = %d, want 1", udp["RcvbufErrors"])
	}
}

func TestParseSNMPMalformed(t *testing.T) {
	content := `Ip: Forwarding DefaultTTL
Ip: 1 64 999
Tcp: ActiveOpens
`
	dir := t.TempDir()
	path := filepath.Join(dir, "snmp")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	sections, err := parseSNMP(path)
	if err != nil {
		t.Fatalf("parseSNMP: %v", err)
	}
	// Ip has mismatched header/value count, Tcp has odd number of lines
	if len(sections) > 0 {
		for k := range sections {
			if k == "Ip" {
				t.Fatal("Ip should be skipped due to count mismatch")
			}
		}
	}
}

func TestWriteCounters(t *testing.T) {
	counters := map[string]uint64{
		"RetransSegs": 1200,
		"InErrs":      5,
		"OutSegs":     850000,
		"Irrelevant":  999,
	}
	var b strings.Builder
	writeCounters(&b, counters, tcpInteresting)

	out := b.String()
	if !strings.Contains(out, "RetransSegs") {
		t.Fatal("expected RetransSegs in output")
	}
	if !strings.Contains(out, "1200") {
		t.Fatal("expected value 1200 in output")
	}
	if strings.Contains(out, "Irrelevant") {
		t.Fatal("should not include non-interesting keys")
	}
}

func TestSectionPrefix(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"Tcp: foo bar", "Tcp:"},
		{"Udp: a b c", "Udp:"},
		{"", ""},
		{"nospace", ""},
	}
	for _, tt := range tests {
		got := sectionPrefix(tt.line)
		if got != tt.want {
			t.Errorf("sectionPrefix(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}
