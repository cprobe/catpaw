package diagnose

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
)

func TestMain(m *testing.M) {
	config.Config = &config.ConfigType{StateDir: os.TempDir()}
	os.Exit(m.Run())
}

func newTestRecord() *DiagnoseRecord {
	return &DiagnoseRecord{
		ID:         "alert_mem_memory_1234567890_abcd",
		Mode:       ModeAlert,
		Status:     "success",
		CreatedAt:  time.Date(2026, 3, 3, 17, 55, 43, 0, time.UTC),
		DurationMs: 76681,
		Alert: AlertRecord{
			Plugin: "mem",
			Target: "memory",
		},
		AI: AIRecord{
			Model:       "gpt-4o",
			TotalRounds: 5,
		},
	}
}

func TestFormatHeader_ZH(t *testing.T) {
	h := formatHeader(newTestRecord(), "zh")
	if !strings.Contains(h, "AI 诊断报告") {
		t.Errorf("zh header should contain Chinese title, got: %s", h)
	}
	if !strings.Contains(h, "插件: mem") {
		t.Errorf("zh header should contain plugin info, got: %s", h)
	}
}

func TestFormatHeader_EN(t *testing.T) {
	h := formatHeader(newTestRecord(), "en")
	if !strings.Contains(h, "AI Diagnosis Report") {
		t.Errorf("en header should contain English title, got: %s", h)
	}
	if !strings.Contains(h, "Plugin: mem") {
		t.Errorf("en header should contain plugin info, got: %s", h)
	}
}

func TestFormatFooter_ZH(t *testing.T) {
	f := formatFooter(newTestRecord(), "zh")
	if !strings.Contains(f, "完整记录:") {
		t.Errorf("zh footer should contain Chinese label, got: %s", f)
	}
	if !strings.Contains(f, "catpaw diagnose show alert_mem_memory_1234567890_abcd") {
		t.Errorf("zh footer should contain CLI command, got: %s", f)
	}
}

func TestFormatFooter_EN(t *testing.T) {
	f := formatFooter(newTestRecord(), "en")
	if !strings.Contains(f, "Full record:") {
		t.Errorf("en footer should contain English label, got: %s", f)
	}
	if !strings.Contains(f, "catpaw diagnose show alert_mem_memory_1234567890_abcd") {
		t.Errorf("en footer should contain CLI command, got: %s", f)
	}
}

func TestFormatReport_Truncation(t *testing.T) {
	record := newTestRecord()
	longBody := strings.Repeat("x", maxDescriptionBytes*2)
	result := FormatReportDescription(record, longBody, "zh")
	if len(result) > maxDescriptionBytes {
		t.Errorf("report should be at most %d bytes, got %d", maxDescriptionBytes, len(result))
	}
	if !strings.Contains(result, "诊断报告已截断") {
		t.Errorf("truncated zh report should contain truncation notice")
	}
}

func TestFormatReport_Truncation_EN(t *testing.T) {
	record := newTestRecord()
	longBody := strings.Repeat("x", maxDescriptionBytes*2)
	result := FormatReportDescription(record, longBody, "en")
	if len(result) > maxDescriptionBytes {
		t.Errorf("report should be at most %d bytes, got %d", maxDescriptionBytes, len(result))
	}
	if !strings.Contains(result, "Report truncated") {
		t.Errorf("truncated en report should contain truncation notice")
	}
}

func TestFormatReport_ShortBody(t *testing.T) {
	record := newTestRecord()
	result := FormatReportDescription(record, "all good", "en")
	if !strings.Contains(result, "all good") {
		t.Errorf("short body should not be truncated, got: %s", result)
	}
	if !strings.Contains(result, "catpaw diagnose show") {
		t.Errorf("result should contain CLI command hint")
	}
}

func TestFormatReportComment_Truncation(t *testing.T) {
	record := newTestRecord()
	longBody := strings.Repeat("诊断结论", 1200)
	result := FormatReportComment(record, longBody, "zh")
	if got := len([]rune(result)); got > maxCommentChars {
		t.Fatalf("comment should be at most %d chars, got %d", maxCommentChars, got)
	}
	if !strings.Contains(result, "catpaw diagnose show "+record.ID) {
		t.Fatalf("comment should contain local lookup hint, got: %s", result)
	}
	if !strings.Contains(result, "[已截断]") {
		t.Fatalf("comment should contain truncation notice, got: %s", result)
	}
}

func TestFormatReportComment_EmptyBody(t *testing.T) {
	record := newTestRecord()
	result := FormatReportComment(record, "   ", "en")
	if !strings.Contains(result, "AI diagnosis completed") {
		t.Fatalf("empty comment should contain fallback text, got: %s", result)
	}
	if !strings.Contains(result, "Details: catpaw diagnose show "+record.ID) {
		t.Fatalf("empty comment should contain CLI hint, got: %s", result)
	}
}

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		input    string
		maxBytes int
		wantLen  bool
	}{
		{"hello", 10, true},
		{"hello", 3, true},
		{"你好世界", 6, true},
		{"", 0, true},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		result := TruncateUTF8(tt.input, tt.maxBytes)
		if len(result) > tt.maxBytes {
			t.Errorf("TruncateUTF8(%q, %d) = %d bytes, exceeds max",
				tt.input, tt.maxBytes, len(result))
		}
	}
}

func TestTruncateUTF8_NoBrokenRunes(t *testing.T) {
	s := "你好世界" // 12 bytes (3 per char)
	result := TruncateUTF8(s, 7)
	if len(result) > 7 {
		t.Errorf("expected at most 7 bytes, got %d", len(result))
	}
	if result != "你好" {
		t.Errorf("expected '你好' (6 bytes), got %q", result)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := TruncateRunes("你好世界", 3); got != "你好世" {
		t.Fatalf("TruncateRunes() = %q, want %q", got, "你好世")
	}
	if got := TruncateRunes("abc", 0); got != "" {
		t.Fatalf("TruncateRunes() = %q, want empty string", got)
	}
}
