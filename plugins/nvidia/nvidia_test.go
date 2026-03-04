package nvidia

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── peermem / GDR ────────────────────────────────────────────────────────────

func TestCountACSEntries_AllDisabled(t *testing.T) {
	input := `
00:1c.0 PCI bridge: ...
		ACSCtl:	SrcValid- TransBlk- ReqRedir- CmpltRedir- UpstreamFwd- EgressCtrl- DirectTrans-
00:1c.4 PCI bridge: ...
		ACSCtl:	SrcValid- TransBlk- ReqRedir- CmpltRedir- UpstreamFwd- EgressCtrl- DirectTrans-
`
	total, disabled := countACSEntries(input)
	if total != 2 {
		t.Errorf("total=%d, want 2", total)
	}
	if disabled != 2 {
		t.Errorf("disabled=%d, want 2", disabled)
	}
}

func TestCountACSEntries_SomeEnabled(t *testing.T) {
	input := `
		ACSCtl:	SrcValid- TransBlk- ReqRedir-
		ACSCtl:	SrcValid+ TransBlk- ReqRedir-
		ACSCtl:	SrcValid- TransBlk- ReqRedir-
		ACSCtl:	SrcValid+ TransBlk- ReqRedir-
`
	total, disabled := countACSEntries(input)
	if total != 4 {
		t.Errorf("total=%d, want 4", total)
	}
	if disabled != 2 {
		t.Errorf("disabled=%d, want 2", disabled)
	}
}

func TestCountACSEntries_NoEntries(t *testing.T) {
	input := "some random lspci output\nno acs here\n"
	total, disabled := countACSEntries(input)
	if total != 0 {
		t.Errorf("total=%d, want 0", total)
	}
	if disabled != 0 {
		t.Errorf("disabled=%d, want 0", disabled)
	}
}

func TestCountACSEntries_Empty(t *testing.T) {
	total, disabled := countACSEntries("")
	if total != 0 || disabled != 0 {
		t.Errorf("expected 0,0 got %d,%d", total, disabled)
	}
}

func TestIsPeermemPersistedIn_Found(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "modules.conf"), []byte("# comment\nnvidia_peermem\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := isPeermemPersistedIn(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected persisted=true")
	}
}

func TestIsPeermemPersistedIn_NotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "modules.conf"), []byte("# comment\nsome_other_module\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := isPeermemPersistedIn(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected persisted=false")
	}
}

func TestIsPeermemPersistedIn_CommentedOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "modules.conf"), []byte("# nvidia_peermem\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := isPeermemPersistedIn(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("commented-out line should not count as persisted")
	}
}

func TestIsPeermemPersistedIn_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.conf"), []byte("other_module\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.conf"), []byte("nvidia_peermem\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := isPeermemPersistedIn(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected persisted=true from b.conf")
	}
}

func TestIsPeermemPersistedIn_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := isPeermemPersistedIn(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("empty dir should return false")
	}
}

func TestIsPeermemPersistedIn_DirNotExist(t *testing.T) {
	_, err := isPeermemPersistedIn("/nonexistent/path/xyz")
	if err == nil {
		t.Error("expected error for nonexistent dir")
	}
}

// ── gpu_link ─────────────────────────────────────────────────────────────────

func TestParseLinkInfo_FullMatch(t *testing.T) {
	input := `
03:00.0 3D controller: NVIDIA Corporation Device 2330 (rev a1)
	Capabilities: [ac0 v1] Express (v2) Endpoint, MSI 00
		LnkCap:	Port #0, Speed 16GT/s, Width x16, ASPM not supported
		LnkSta:	Speed 16GT/s (ok), Width x16 (ok)
`
	speedCap, widthCap, speedSta, widthSta := parseLinkInfo(input)
	if speedCap != "16gt/s" {
		t.Errorf("speedCap=%q, want 16gt/s", speedCap)
	}
	if widthCap != "x16" {
		t.Errorf("widthCap=%q, want x16", widthCap)
	}
	if speedSta != "16gt/s" {
		t.Errorf("speedSta=%q, want 16gt/s", speedSta)
	}
	if widthSta != "x16" {
		t.Errorf("widthSta=%q, want x16", widthSta)
	}
}

func TestParseLinkInfo_SpeedMismatch(t *testing.T) {
	input := `
		LnkCap:	Port #0, Speed 16GT/s, Width x16, ASPM not supported
		LnkSta:	Speed 8GT/s (downgraded), Width x16 (ok)
`
	speedCap, _, speedSta, _ := parseLinkInfo(input)
	if speedCap != "16gt/s" {
		t.Errorf("speedCap=%q, want 16gt/s", speedCap)
	}
	if speedSta != "8gt/s" {
		t.Errorf("speedSta=%q, want 8gt/s", speedSta)
	}
}

func TestParseLinkInfo_WidthMismatch(t *testing.T) {
	input := `
		LnkCap:	Port #0, Speed 16GT/s, Width x16, ASPM not supported
		LnkSta:	Speed 16GT/s (ok), Width x8 (downgraded)
`
	_, widthCap, _, widthSta := parseLinkInfo(input)
	if widthCap != "x16" {
		t.Errorf("widthCap=%q, want x16", widthCap)
	}
	if widthSta != "x8" {
		t.Errorf("widthSta=%q, want x8", widthSta)
	}
}

func TestParseLinkInfo_Empty(t *testing.T) {
	speedCap, widthCap, speedSta, widthSta := parseLinkInfo("")
	if speedCap != "" || widthCap != "" || speedSta != "" || widthSta != "" {
		t.Errorf("expected all empty, got (%q,%q,%q,%q)", speedCap, widthCap, speedSta, widthSta)
	}
}

func TestStripDomain(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"00000000:1B:00.0", "1B:00.0"},
		{"00000000:03:00.0", "03:00.0"},
		{"1B:00.0", "1B:00.0"},       // no domain prefix, unchanged
		{"03:00.0", "03:00.0"},       // no domain prefix, unchanged
		{"", ""},
	}
	for _, tc := range cases {
		got := stripDomain(tc.input)
		if got != tc.want {
			t.Errorf("stripDomain(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFormatLinkReport_AllOK(t *testing.T) {
	infos := []gpuLinkInfo{
		{busID: "1b:00.0", name: "NVIDIA H100", speedCap: "16gt/s", speedSta: "16gt/s", widthCap: "x16", widthSta: "x16"},
		{busID: "1c:00.0", name: "NVIDIA H100", speedCap: "16gt/s", speedSta: "16gt/s", widthCap: "x16", widthSta: "x16"},
	}
	out := formatLinkReport(infos)
	if !strings.Contains(out, "2 GPU(s)") {
		t.Error("expected GPU count in header")
	}
	if strings.Contains(out, "  MISMATCH") {
		t.Error("expected no row with MISMATCH result")
	}
	if !strings.Contains(out, "Summary: 2 OK, 0 MISMATCH") {
		t.Errorf("unexpected summary line, got:\n%s", out)
	}
}

func TestFormatLinkReport_WithMismatch(t *testing.T) {
	infos := []gpuLinkInfo{
		{busID: "1b:00.0", name: "NVIDIA H100", speedCap: "16gt/s", speedSta: "16gt/s", widthCap: "x16", widthSta: "x16"},
		{busID: "1c:00.0", name: "NVIDIA H100", speedCap: "16gt/s", speedSta: "8gt/s", widthCap: "x16", widthSta: "x8"},
	}
	out := formatLinkReport(infos)
	if !strings.Contains(out, "MISMATCH") {
		t.Error("expected MISMATCH in output")
	}
	if !strings.Contains(out, "Summary: 1 OK, 1 MISMATCH") {
		t.Errorf("unexpected summary, got:\n%s", out)
	}
	if !strings.Contains(out, "PCIe link degradation") {
		t.Error("expected degradation note for mismatch")
	}
}

func TestFormatLinkReport_WithLspciError(t *testing.T) {
	infos := []gpuLinkInfo{
		{busID: "1b:00.0", name: "NVIDIA H100", lspciErr: errors.New("permission denied")},
	}
	out := formatLinkReport(infos)
	if !strings.Contains(out, "lspci error") {
		t.Error("expected lspci error in output")
	}
	if !strings.Contains(out, "Summary: 0 OK, 1 MISMATCH") {
		t.Errorf("unexpected summary, got:\n%s", out)
	}
}

func TestFormatLinkReport_Empty(t *testing.T) {
	out := formatLinkReport(nil)
	if !strings.Contains(out, "0 GPU(s)") {
		t.Errorf("expected 0 GPU(s), got:\n%s", out)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		input string
		max   int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 8, "hello..."},
		{"ab", 3, "ab"},
		{"abcd", 3, "..."},
	}
	for _, tc := range cases {
		got := truncate(tc.input, tc.max)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
		}
	}
}
