package sockstat

import (
	"runtime"
	"strings"
	"testing"

	"github.com/cprobe/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &SockstatPlugin{}
	p.RegisterDiagnoseTools(registry)

	tool, ok := registry.Get("sockstat_summary")
	if !ok {
		t.Fatal("tool sockstat_summary not registered")
	}
	if tool.Scope != diagnose.ToolScopeLocal {
		t.Fatal("sockstat_summary should be local scope")
	}
}

func TestExecSockstatSummary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	result, err := execSockstatSummary(nil, nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "TcpExt") {
		t.Fatalf("expected TcpExt header, got: %s", result)
	}
}
