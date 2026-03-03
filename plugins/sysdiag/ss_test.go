package sysdiag

import (
	"strings"
	"testing"
)

func TestGroupSSConnections(t *testing.T) {
	lines := []string{
		"ESTAB  0  0  10.0.0.1:443  10.0.0.2:54321  users:((\"nginx\",pid=1234,fd=5))",
		"\tcubic wscale:7,7 rto:204 rtt:1.2/0.5 ato:40 mss:1448 pmtu:1500 rcvmss:1448 advmss:1448 cwnd:10 bytes_sent:12345",
		"ESTAB  0  0  10.0.0.1:443  10.0.0.3:54322  users:((\"nginx\",pid=1234,fd=6))",
		"\tcubic wscale:7,7 rto:204 rtt:2.5/1.0 cwnd:8 bytes_sent:67890",
	}

	groups := groupSSConnections(lines)
	if len(groups) != 2 {
		t.Fatalf("expected 2 connection groups, got %d", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Errorf("first group should have 2 lines (main + detail), got %d", len(groups[0]))
	}
	if len(groups[1]) != 2 {
		t.Errorf("second group should have 2 lines, got %d", len(groups[1]))
	}
}

func TestGroupSSConnectionsEmpty(t *testing.T) {
	groups := groupSSConnections(nil)
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups for nil input, got %d", len(groups))
	}
	groups = groupSSConnections([]string{"", ""})
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups for empty lines, got %d", len(groups))
	}
}

func TestFormatSSOutput(t *testing.T) {
	raw := `State  Recv-Q  Send-Q  Local Address:Port  Peer Address:Port  Process
ESTAB  0  0  10.0.0.1:443  10.0.0.2:54321  users:(("nginx",pid=1234,fd=5))
	cubic wscale:7,7 rto:204 rtt:1.2/0.5 cwnd:10`

	out := formatSSOutput(raw, "established", "", 50)
	if !strings.Contains(out, "state=established") {
		t.Fatal("expected state in output")
	}
	if !strings.Contains(out, "1") {
		t.Fatal("expected connection count")
	}
	if !strings.Contains(out, "nginx") {
		t.Fatal("expected process info in output")
	}
}

func TestFormatSSOutputEmpty(t *testing.T) {
	out := formatSSOutput("", "established", "443", 50)
	if !strings.Contains(out, "No TCP connections") {
		t.Fatal("expected 'No TCP connections' message")
	}
}

func TestExecSSDetail_Validation(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]string
		wantErr string
	}{
		{"bad state", map[string]string{"state": "invalid"}, "invalid state"},
		{"bad port neg", map[string]string{"port": "-1"}, "invalid port"},
		{"bad port high", map[string]string{"port": "99999"}, "invalid port"},
		{"bad port str", map[string]string{"port": "abc"}, "invalid port"},
		{"bad max_lines", map[string]string{"max_lines": "0"}, "invalid max_lines"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := execSSDetail(t.Context(), tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
