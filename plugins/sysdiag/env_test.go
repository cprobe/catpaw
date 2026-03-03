package sysdiag

import (
	"strings"
	"testing"
)

func TestMaskSensitive(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"PATH=/usr/bin", "PATH=/usr/bin"},
		{"HOME=/root", "HOME=/root"},
		{"DB_PASSWORD=hunter2", "DB_PASSWORD=***"},
		{"API_KEY=abc123", "API_KEY=***"},
		{"MY_SECRET_TOKEN=xyz", "MY_SECRET_TOKEN=***"},
		{"aws_access_key_id=AKIA...", "aws_access_key_id=***"},
		{"CREDENTIAL_FILE=/etc/creds", "CREDENTIAL_FILE=***"},
		{"auth_token=foo", "auth_token=***"},
		{"NOEQUALSIGN", "NOEQUALSIGN"},
	}
	for _, tt := range tests {
		got := maskSensitive(tt.input)
		if got != tt.want {
			t.Errorf("maskSensitive(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFilterEnvVars(t *testing.T) {
	vars := []string{
		"PATH=/usr/bin",
		"HOME=/root",
		"JAVA_HOME=/usr/lib/jvm",
		"JAVA_OPTS=-Xmx2g",
	}

	result := filterEnvVars(vars, "java")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches for 'java', got %d", len(result))
	}

	result = filterEnvVars(vars, "nonexistent")
	if len(result) != 0 {
		t.Fatalf("expected 0 matches for 'nonexistent', got %d", len(result))
	}
}

func TestFormatEnvVars(t *testing.T) {
	vars := []string{"A=1", "B=2", "DB_PASSWORD=secret"}
	out := formatEnvVars(42, vars, "")
	if !strings.Contains(out, "PID 42") {
		t.Fatal("expected PID in output")
	}
	if !strings.Contains(out, "3 variables") {
		t.Fatal("expected variable count in output")
	}
	if !strings.Contains(out, "DB_PASSWORD=***") {
		t.Fatal("expected masked password in output")
	}
}

func TestFormatEnvVarsEmpty(t *testing.T) {
	out := formatEnvVars(1, nil, "")
	if !strings.Contains(out, "No environment") {
		t.Fatal("expected 'No environment' message")
	}
}

func TestFormatEnvVarsWithFilter(t *testing.T) {
	out := formatEnvVars(1, nil, "java")
	if !strings.Contains(out, "matching") {
		t.Fatal("expected 'matching' in filter message")
	}
}

func TestExecEnvInspect_Validation(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]string
		wantErr string
	}{
		{"empty pid", map[string]string{}, "pid parameter is required"},
		{"negative pid", map[string]string{"pid": "-1"}, "invalid pid"},
		{"zero pid", map[string]string{"pid": "0"}, "invalid pid"},
		{"non-numeric pid", map[string]string{"pid": "abc"}, "invalid pid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := execEnvInspect(t.Context(), tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
