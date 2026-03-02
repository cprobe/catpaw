package diagnose

import (
	"testing"

	"github.com/cprobe/catpaw/config"
)

func TestShouldTrigger(t *testing.T) {
	tests := []struct {
		name        string
		enabled     bool
		minSeverity string
		eventStatus string
		want        bool
	}{
		{"disabled", false, "Warning", "Critical", false},
		{"critical meets warning", true, "Warning", "Critical", true},
		{"warning meets warning", true, "Warning", "Warning", true},
		{"info below warning", true, "Warning", "Info", false},
		{"ok below warning", true, "Warning", "Ok", false},
		{"critical meets critical", true, "Critical", "Critical", true},
		{"warning below critical", true, "Critical", "Warning", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DiagnoseConfig{
				Enabled:     tt.enabled,
				MinSeverity: tt.minSeverity,
			}
			if got := shouldTrigger(cfg, tt.eventStatus); got != tt.want {
				t.Errorf("shouldTrigger(%q) = %v, want %v", tt.eventStatus, got, tt.want)
			}
		})
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
