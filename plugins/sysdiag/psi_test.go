package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePSILine(t *testing.T) {
	tests := []struct {
		line   string
		wantOK bool
		cat    string
		avg10  float64
	}{
		{"some avg10=1.23 avg60=0.45 avg300=0.12 total=123456", true, "some", 1.23},
		{"full avg10=0.00 avg60=0.00 avg300=0.00 total=0", true, "full", 0.00},
		{"bad line", false, "", 0},
		{"", false, "", 0},
		{"some", false, "", 0},
	}
	for _, tt := range tests {
		pl, ok := parsePSILine(tt.line)
		if ok != tt.wantOK {
			t.Errorf("parsePSILine(%q): ok=%v, want %v", tt.line, ok, tt.wantOK)
			continue
		}
		if ok {
			if pl.category != tt.cat {
				t.Errorf("parsePSILine(%q): category=%q, want %q", tt.line, pl.category, tt.cat)
			}
			if pl.avg10 != tt.avg10 {
				t.Errorf("parsePSILine(%q): avg10=%f, want %f", tt.line, pl.avg10, tt.avg10)
			}
		}
	}
}

func TestReadPSIFile(t *testing.T) {
	content := `some avg10=2.50 avg60=1.00 avg300=0.50 total=100000
full avg10=0.10 avg60=0.05 avg300=0.02 total=5000
`
	dir := t.TempDir()
	path := filepath.Join(dir, "cpu")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	lines, err := readPSIFile(path)
	if err != nil {
		t.Fatalf("readPSIFile: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0].category != "some" || lines[0].avg10 != 2.50 {
		t.Errorf("unexpected first line: %+v", lines[0])
	}
	if lines[1].category != "full" || lines[1].totalUs != 5000 {
		t.Errorf("unexpected second line: %+v", lines[1])
	}
}

func TestFormatPSI(t *testing.T) {
	results := []psiResult{
		{
			resource: "cpu",
			lines: []psiLine{
				{category: "some", avg10: 30.0, avg60: 10.0, avg300: 5.0, totalUs: 100000},
			},
		},
		{
			resource: "memory",
			lines: []psiLine{
				{category: "some", avg10: 0.5, avg60: 0.2, avg300: 0.1, totalUs: 500},
				{category: "full", avg10: 0.0, avg60: 0.0, avg300: 0.0, totalUs: 0},
			},
		},
	}

	out := formatPSI(results)
	if !strings.Contains(out, "PSI") {
		t.Fatal("expected PSI header")
	}
	if !strings.Contains(out, "[!!!]") {
		t.Fatal("expected [!!!] marker for avg10=30")
	}
	if !strings.Contains(out, "cpu") {
		t.Fatal("expected cpu resource")
	}
}

func TestFormatPSIAllError(t *testing.T) {
	results := []psiResult{
		{resource: "cpu", err: os.ErrNotExist},
	}
	out := formatPSI(results)
	if !strings.Contains(out, "not available") {
		t.Fatal("expected 'not available' message")
	}
}

func TestExecPSICheck_Validation(t *testing.T) {
	_, err := execPSICheck(t.Context(), map[string]string{"resource": "gpu"})
	if err == nil {
		t.Fatal("expected error for invalid resource")
	}
	if !strings.Contains(err.Error(), "invalid resource") {
		t.Fatalf("unexpected error: %v", err)
	}
}
