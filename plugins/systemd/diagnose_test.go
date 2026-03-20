package systemd

import (
	"strings"
	"testing"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &SystemdPlugin{}
	p.RegisterDiagnoseTools(registry)

	for _, name := range []string{"service_status", "service_list_failed"} {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.Scope != diagnose.ToolScopeLocal {
			t.Fatalf("tool %q should be local scope", name)
		}
	}
}

func TestFormatUnitProps(t *testing.T) {
	props := map[string]string{
		"Description":           "OpenSSH Daemon",
		"LoadState":             "loaded",
		"ActiveState":           "active",
		"SubState":              "running",
		"MainPID":               "1234",
		"ActiveEnterTimestamp":  "Mon 2026-03-02 10:00:00 CST",
	}

	out := formatUnitProps("sshd.service", props)
	if !strings.Contains(out, "sshd.service") {
		t.Fatal("expected unit name in output")
	}
	if !strings.Contains(out, "active") {
		t.Fatal("expected active state")
	}
	if !strings.Contains(out, "1234") {
		t.Fatal("expected PID")
	}
}

func TestExecServiceStatusMissingUnit(t *testing.T) {
	_, err := execServiceStatus(nil, map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing unit")
	}
}
