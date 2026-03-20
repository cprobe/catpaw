package types

import "testing"

func TestEvaluateGeThreshold(t *testing.T) {
	tests := []struct {
		name       string
		value      float64
		warnGe     float64
		criticalGe float64
		want       string
	}{
		{"below both", 50, 80, 90, EventStatusOk},
		{"exactly at warn", 80, 80, 90, EventStatusWarning},
		{"between warn and critical", 85, 80, 90, EventStatusWarning},
		{"exactly at critical", 90, 80, 90, EventStatusCritical},
		{"above critical", 95, 80, 90, EventStatusCritical},
		{"warn only - below", 50, 80, 0, EventStatusOk},
		{"warn only - at warn", 80, 80, 0, EventStatusWarning},
		{"warn only - above", 90, 80, 0, EventStatusWarning},
		{"critical only - below", 80, 0, 90, EventStatusOk},
		{"critical only - at critical", 90, 0, 90, EventStatusCritical},
		{"critical only - above", 95, 0, 90, EventStatusCritical},
		{"both disabled", 99, 0, 0, EventStatusOk},
		{"zero value", 0, 80, 90, EventStatusOk},
		{"value equals zero with warn at zero boundary", 0, 0, 90, EventStatusOk},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateGeThreshold(tt.value, tt.warnGe, tt.criticalGe)
			if got != tt.want {
				t.Errorf("EvaluateGeThreshold(%.1f, %.1f, %.1f) = %s, want %s",
					tt.value, tt.warnGe, tt.criticalGe, got, tt.want)
			}
		})
	}
}
