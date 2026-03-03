package sysdiag

import (
	"strings"
	"testing"
)

func TestEvaluateParam(t *testing.T) {
	tests := []struct {
		param   tcpParam
		value   string
		wantOK  bool
		wantMsg string
	}{
		{
			tcpParam{sysctl: "net.ipv4.tcp_syn_retries", recMin: 2, recMax: 6},
			"6", true, "",
		},
		{
			tcpParam{sysctl: "net.ipv4.tcp_syn_retries", recMin: 2, recMax: 6},
			"10", false, "above range",
		},
		{
			tcpParam{sysctl: "net.ipv4.tcp_syn_retries", recMin: 2, recMax: 6},
			"1", false, "below range",
		},
		{
			tcpParam{sysctl: "net.core.somaxconn", recMin: 128, recMax: -1},
			"4096", true, "",
		},
		{
			tcpParam{sysctl: "net.core.somaxconn", recMin: 128, recMax: -1},
			"64", false, "below recommended minimum",
		},
		{
			tcpParam{sysctl: "net.ipv4.tcp_congestion_control", recMin: 0, recMax: -1},
			"cubic", true, "",
		},
		{
			tcpParam{sysctl: "net.ipv4.tcp_rmem", recMin: 0, recMax: -1},
			"4096\t131072\t6291456", true, "",
		},
		{
			tcpParam{sysctl: "test", recMin: 1, recMax: 10},
			"(not available)", true, "",
		},
	}

	for _, tc := range tests {
		note := evaluateParam(tc.param, tc.value)
		if tc.wantOK && note != "" {
			t.Errorf("%s=%q: unexpected note: %s", tc.param.sysctl, tc.value, note)
		}
		if !tc.wantOK && !strings.Contains(note, tc.wantMsg) {
			t.Errorf("%s=%q: note=%q, want containing %q", tc.param.sysctl, tc.value, note, tc.wantMsg)
		}
	}
}

func TestFormatTCPTune(t *testing.T) {
	results := []paramResult{
		{param: tcpParam{sysctl: "net.ipv4.tcp_syn_retries", category: "Timeout/Retry", recMin: 2, recMax: 6}, value: "6", note: ""},
		{param: tcpParam{sysctl: "net.ipv4.tcp_retries2", category: "Timeout/Retry", recMin: 8, recMax: 15}, value: "15", note: ""},
		{param: tcpParam{sysctl: "net.core.somaxconn", category: "Backlog", recMin: 128, recMax: -1}, value: "64", note: "[!] below recommended minimum (128)"},
	}

	out := formatTCPTune(results)
	if !strings.Contains(out, "TCP Tuning") {
		t.Error("expected title")
	}
	if !strings.Contains(out, "[Timeout/Retry]") {
		t.Error("expected category header")
	}
	if !strings.Contains(out, "1 parameter(s) outside") {
		t.Error("expected issue count")
	}
}

func TestFormatTCPTuneAllOK(t *testing.T) {
	results := []paramResult{
		{param: tcpParam{sysctl: "net.ipv4.tcp_syn_retries", category: "Timeout/Retry"}, value: "6", note: ""},
	}
	out := formatTCPTune(results)
	if !strings.Contains(out, "All parameters within") {
		t.Error("expected all OK message")
	}
}

func TestReadSysctlValue(t *testing.T) {
	val := readSysctlValue("/nonexistent/path")
	if val != "(not available)" {
		t.Errorf("expected '(not available)', got %q", val)
	}
}
