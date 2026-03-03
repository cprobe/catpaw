package sysdiag

import (
	"strings"
	"testing"
)

func TestFormatVGS(t *testing.T) {
	raw := `  vg_data  |  100.00  |  20.00  |  2  |  5  |  wz--n-`
	var b strings.Builder
	formatVGS(&b, raw)
	out := b.String()
	if !strings.Contains(out, "vg_data") {
		t.Fatal("expected VG name in output")
	}
	if !strings.Contains(out, "100.00") {
		t.Fatal("expected VG size in output")
	}
}

func TestFormatVGSEmpty(t *testing.T) {
	var b strings.Builder
	formatVGS(&b, "")
	if !strings.Contains(b.String(), "none") {
		t.Fatal("expected 'none' for empty VGs")
	}
}

func TestFormatLVS(t *testing.T) {
	raw := `  root  |  vg_data  |  50.00  |  -wi-a-----`
	var b strings.Builder
	formatLVS(&b, raw)
	out := b.String()
	if !strings.Contains(out, "root") {
		t.Fatal("expected LV name in output")
	}
	if !strings.Contains(out, "vg_data") {
		t.Fatal("expected VG name in output")
	}
}

func TestLVAttrNote(t *testing.T) {
	tests := []struct {
		attr string
		want string
	}{
		{"-wi-a-----", ""},
		{"-wi-s-----", "[!] SUSPENDED"},
		{"-wi-a---p-", "[!] PARTIAL"},
		{"-wi-I---m-", "[!] INVALID, [!] MISMATCHES"},
		{"", ""},
		{"-wi", ""},
	}
	for _, tt := range tests {
		got := lvAttrNote(tt.attr)
		if got != tt.want {
			t.Errorf("lvAttrNote(%q) = %q, want %q", tt.attr, got, tt.want)
		}
	}
}
