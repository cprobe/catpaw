package diagnose

import (
	"testing"
	"time"

	"github.com/cprobe/digcore/types"
)

func TestToolScopeString(t *testing.T) {
	tests := []struct {
		scope ToolScope
		want  string
	}{
		{ToolScopeLocal, "local"},
		{ToolScopeRemote, "remote"},
		{ToolScope(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.scope.String(); got != tt.want {
			t.Errorf("ToolScope(%d).String() = %q, want %q", tt.scope, got, tt.want)
		}
	}
}

func TestDiagnoseSessionClose(t *testing.T) {
	t.Run("nil accessor", func(t *testing.T) {
		s := &DiagnoseSession{}
		s.Close() // should not panic
	})

	t.Run("non-closer accessor", func(t *testing.T) {
		s := &DiagnoseSession{Accessor: "not a closer"}
		s.Close() // should not panic
	})

	t.Run("closer accessor", func(t *testing.T) {
		mc := &mockCloser{}
		s := &DiagnoseSession{Accessor: mc}
		s.Close()
		if !mc.closed {
			t.Error("expected Close() to be called on accessor")
		}
	})
}

type mockCloser struct {
	closed bool
}

func (m *mockCloser) Close() error {
	m.closed = true
	return nil
}

func TestDiagnoseRequestFields(t *testing.T) {
	req := &DiagnoseRequest{
		Events: []*types.Event{
			{AlertKey: "redis::used_memory::10.0.0.1:6379"},
		},
		Plugin: "redis",
		Target: "10.0.0.1:6379",
		Checks: []CheckSnapshot{
			{
				Check:         "redis::used_memory",
				Status:        "Warning",
				CurrentValue:  "1.8GB",
				ThresholdDesc: "Warning ≥ 1GB",
				Description:   "used_memory 1.8GB >= warning threshold 1GB",
			},
		},
		Timeout:  60 * time.Second,
		Cooldown: 10 * time.Minute,
	}

	if req.Plugin != "redis" {
		t.Errorf("Plugin = %q, want %q", req.Plugin, "redis")
	}
	if len(req.Checks) != 1 {
		t.Fatalf("len(Checks) = %d, want 1", len(req.Checks))
	}
	if req.Checks[0].Status != "Warning" {
		t.Errorf("Checks[0].Status = %q, want %q", req.Checks[0].Status, "Warning")
	}
}

func TestSeverityRank(t *testing.T) {
	if SeverityRank("Critical") <= SeverityRank("Warning") {
		t.Error("Critical should rank higher than Warning")
	}
	if SeverityRank("Warning") <= SeverityRank("Info") {
		t.Error("Warning should rank higher than Info")
	}
	if SeverityRank("Info") <= SeverityRank("Ok") {
		t.Error("Info should rank higher than Ok")
	}
	if SeverityRank("Ok") != 0 {
		t.Errorf("SeverityRank(Ok) = %d, want 0", SeverityRank("Ok"))
	}
	if SeverityRank("garbage") != 0 {
		t.Errorf("SeverityRank(garbage) = %d, want 0", SeverityRank("garbage"))
	}
}

func TestDiagnoseRecord(t *testing.T) {
	rec := &DiagnoseRecord{
		ID:     "d8f3a2b1",
		Status: "success",
		Alert: AlertRecord{
			Plugin: "redis",
			Target: "10.0.0.1:6379",
			Checks: []CheckSnapshot{
				{Check: "redis::used_memory", Status: "Warning"},
			},
		},
		AI: AIRecord{
			Model:        "gpt-4o",
			TotalRounds:  3,
			InputTokens:  2840,
			OutputTokens: 680,
		},
		Rounds: []RoundRecord{
			{
				Round: 1,
				ToolCalls: []ToolCallRecord{
					{Name: "redis_info", Args: map[string]string{"section": "memory"}, Result: "ok", DurationMs: 45},
				},
				AIReasoning: "memory is high",
			},
		},
		Report: "## Summary\nMemory too high.",
	}

	if rec.ID != "d8f3a2b1" {
		t.Errorf("ID = %q, want %q", rec.ID, "d8f3a2b1")
	}
	if rec.AI.TotalRounds != 3 {
		t.Errorf("AI.TotalRounds = %d, want 3", rec.AI.TotalRounds)
	}
	if len(rec.Rounds) != 1 {
		t.Fatalf("len(Rounds) = %d, want 1", len(rec.Rounds))
	}
	if len(rec.Rounds[0].ToolCalls) != 1 {
		t.Fatalf("len(Rounds[0].ToolCalls) = %d, want 1", len(rec.Rounds[0].ToolCalls))
	}
}
