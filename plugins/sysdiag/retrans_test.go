package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadProcPairFile(t *testing.T) {
	content := `Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts InCsumErrors
Tcp: 1 200 120000 -1 5000 3000 100 50 25 1000000 900000 1500 10 200 0
Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors IgnoredMulti
Udp: 50000 100 5 40000 0 0 0 200
`
	dir := t.TempDir()
	path := filepath.Join(dir, "snmp")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tcp, err := readProcPairFile(path, "Tcp")
	if err != nil {
		t.Fatalf("readProcPairFile Tcp: %v", err)
	}
	if tcp["RetransSegs"] != 1500 {
		t.Errorf("RetransSegs=%d, want 1500", tcp["RetransSegs"])
	}
	if tcp["OutSegs"] != 900000 {
		t.Errorf("OutSegs=%d, want 900000", tcp["OutSegs"])
	}

	udp, err := readProcPairFile(path, "Udp")
	if err != nil {
		t.Fatalf("readProcPairFile Udp: %v", err)
	}
	if udp["InDatagrams"] != 50000 {
		t.Errorf("InDatagrams=%d, want 50000", udp["InDatagrams"])
	}

	_, err = readProcPairFile(path, "Missing")
	if err == nil {
		t.Fatal("expected error for missing section")
	}
}

func TestSafeDelta(t *testing.T) {
	if safeDelta(100, 50) != 50 {
		t.Error("100-50 should be 50")
	}
	if safeDelta(50, 100) != 0 {
		t.Error("counter wrap should return 0")
	}
	if safeDelta(100, 100) != 0 {
		t.Error("same values should return 0")
	}
}

func TestFormatRetransRate(t *testing.T) {
	snap1 := map[string]uint64{
		"RetransSegs":  1000,
		"InErrs":       10,
		"OutRsts":      50,
		"AttemptFails": 5,
		"EstabResets":  2,
		"InSegs":       500000,
		"OutSegs":      400000,
		"ActiveOpens":  3000,
		"PassiveOpens": 2000,
		"TCPTimeouts":  100,
	}
	snap2 := map[string]uint64{
		"RetransSegs":  1050,
		"InErrs":       12,
		"OutRsts":      55,
		"AttemptFails": 6,
		"EstabResets":  2,
		"InSegs":       510000,
		"OutSegs":      409000,
		"ActiveOpens":  3100,
		"PassiveOpens": 2050,
		"TCPTimeouts":  105,
	}

	out := formatRetransRate(snap1, snap2, time.Second)
	if !strings.Contains(out, "RetransSegs") {
		t.Error("expected RetransSegs")
	}
	if !strings.Contains(out, "[!]") {
		t.Error("expected [!] for non-zero error rates")
	}
	if !strings.Contains(out, "Retransmission ratio") {
		t.Error("expected retransmission ratio")
	}
}

func TestIsErrorCounter(t *testing.T) {
	if !isErrorCounter("RetransSegs") {
		t.Error("RetransSegs should be error counter")
	}
	if isErrorCounter("InSegs") {
		t.Error("InSegs should not be error counter")
	}
}
