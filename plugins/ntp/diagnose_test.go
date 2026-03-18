package ntp

import (
	"strings"
	"testing"

	"github.com/cprobe/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &NTPPlugin{}
	p.RegisterDiagnoseTools(registry)

	tool, ok := registry.Get("ntp_status")
	if !ok {
		t.Fatal("tool ntp_status not registered")
	}
	if tool.Scope != diagnose.ToolScopeLocal {
		t.Fatal("ntp_status should be local scope")
	}
}

func TestFormatNtpResult(t *testing.T) {
	r := &ntpResult{
		synced:  true,
		source:  "10.0.0.1",
		stratum: 3,
		offset:  150000,
		extra:   map[string]string{"leap_status": "Normal"},
	}

	out := formatNtpResult("chrony", r)
	if !strings.Contains(out, "chrony") {
		t.Fatal("expected mode in output")
	}
	if !strings.Contains(out, "yes") {
		t.Fatal("expected synced=yes")
	}
	if !strings.Contains(out, "10.0.0.1") {
		t.Fatal("expected source in output")
	}
	if !strings.Contains(out, "3") {
		t.Fatal("expected stratum in output")
	}
}

func TestFormatNtpResultNotSynced(t *testing.T) {
	r := &ntpResult{
		synced: false,
		extra:  map[string]string{},
	}
	out := formatNtpResult("timedatectl", r)
	if !strings.Contains(out, "NO") {
		t.Fatal("expected synced=NO")
	}
}
