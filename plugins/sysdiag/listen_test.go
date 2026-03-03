package sysdiag

import (
	"strings"
	"testing"
)

func TestParseSSListenOutput(t *testing.T) {
	raw := `State  Recv-Q  Send-Q  Local Address:Port  Peer Address:Port  Process
LISTEN 0       128     0.0.0.0:22          0.0.0.0:*          users:(("sshd",pid=1234,fd=3))
LISTEN 5       128     0.0.0.0:80          0.0.0.0:*          users:(("nginx",pid=5678,fd=6))
LISTEN 0       511     0.0.0.0:443         0.0.0.0:*          users:(("nginx",pid=5678,fd=7))
LISTEN 120     128     127.0.0.1:3306      0.0.0.0:*          users:(("mysqld",pid=9012,fd=22))
`
	sockets := parseSSListenOutput(raw)
	if len(sockets) != 4 {
		t.Fatalf("expected 4 sockets, got %d", len(sockets))
	}

	if sockets[0].recvQ != 0 || sockets[0].sendQ != 128 {
		t.Errorf("socket[0]: recvQ=%d sendQ=%d", sockets[0].recvQ, sockets[0].sendQ)
	}
	if sockets[1].recvQ != 5 {
		t.Errorf("socket[1].recvQ=%d, want 5", sockets[1].recvQ)
	}
	if sockets[3].recvQ != 120 {
		t.Errorf("socket[3].recvQ=%d, want 120", sockets[3].recvQ)
	}
}

func TestExtractProcessName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`users:(("sshd",pid=1234,fd=3))`, "sshd"},
		{`users:(("nginx",pid=5678,fd=6))`, "nginx"},
		{`users:(("my-app",pid=100,fd=5))`, "my-app"},
		{``, ""},
	}
	for _, tc := range tests {
		got := extractProcessName(tc.input)
		if got != tc.want {
			t.Errorf("extractProcessName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestListenSocketUsage(t *testing.T) {
	s := listenSocket{recvQ: 64, sendQ: 128}
	pct := s.usage()
	if pct != 50.0 {
		t.Errorf("usage=%f, want 50.0", pct)
	}

	s2 := listenSocket{recvQ: 0, sendQ: 0}
	if s2.usage() != 0 {
		t.Error("usage should be 0 when sendQ is 0")
	}
}

func TestFormatListenOverflow(t *testing.T) {
	sockets := []listenSocket{
		{recvQ: 0, sendQ: 128, local: "0.0.0.0:22", process: "sshd"},
		{recvQ: 120, sendQ: 128, local: "127.0.0.1:3306", process: "mysqld"},
	}
	out := formatListenOverflow(sockets, 1500, 1200)

	if !strings.Contains(out, "ListenOverflows: 1500") {
		t.Error("expected overflow counter")
	}
	if !strings.Contains(out, "[!]") {
		t.Error("expected [!] for non-zero overflows")
	}
	if !strings.Contains(out, "[!!!]") {
		t.Error("expected [!!!] for >90% queue usage")
	}
	if !strings.Contains(out, "pending connections") {
		t.Error("expected pending connections summary")
	}
}

func TestFormatListenOverflowEmpty(t *testing.T) {
	out := formatListenOverflow(nil, 0, 0)
	if !strings.Contains(out, "No LISTEN sockets") {
		t.Error("expected empty message")
	}
}
