package diagnose

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const maxDescriptionBytes = 2048

// FormatReportDescription builds a concise diagnosis report suitable for
// notification description fields (max 2048 bytes). It prioritizes:
//  1. Header (plugin, target, time, status)
//  2. AI summary/report body
//  3. A footer with record ID and CLI hint
//
// If the report exceeds 2048 bytes, the AI body is truncated.
func FormatReportDescription(record *DiagnoseRecord, report string, language string) string {
	header := formatHeader(record, language)
	footer := formatFooter(record, language)

	headerBytes := len(header)
	footerBytes := len(footer)
	overhead := headerBytes + footerBytes

	if overhead >= maxDescriptionBytes {
		return TruncateUTF8(header, maxDescriptionBytes)
	}

	truncSuffix := truncSuffixText(language)
	budget := maxDescriptionBytes - overhead
	body := report
	if len(body) > budget {
		if budget > len(truncSuffix) {
			body = TruncateUTF8(body, budget-len(truncSuffix)) + truncSuffix
		} else {
			body = TruncateUTF8(body, budget)
		}
	}

	return header + body + footer
}

func formatHeader(record *DiagnoseRecord, language string) string {
	var b strings.Builder
	if language == "zh" {
		b.WriteString(fmt.Sprintf("[*] AI 诊断报告 [%s]\n", record.Status))
		b.WriteString(fmt.Sprintf("插件: %s | 目标: %s\n", record.Alert.Plugin, record.Alert.Target))
		b.WriteString(fmt.Sprintf("诊断时间: %s | 耗时: %dms | AI轮次: %d\n",
			record.CreatedAt.Format(time.DateTime),
			record.DurationMs,
			record.AI.TotalRounds))
	} else {
		b.WriteString(fmt.Sprintf("[*] AI Diagnosis Report [%s]\n", record.Status))
		b.WriteString(fmt.Sprintf("Plugin: %s | Target: %s\n", record.Alert.Plugin, record.Alert.Target))
		b.WriteString(fmt.Sprintf("Time: %s | Duration: %dms | Rounds: %d\n",
			record.CreatedAt.Format(time.DateTime),
			record.DurationMs,
			record.AI.TotalRounds))
	}
	b.WriteString("---\n")
	return b.String()
}

func formatFooter(record *DiagnoseRecord, language string) string {
	if language == "zh" {
		return fmt.Sprintf("\n---\n查看命令: catpaw diagnose show %s\n完整记录: %s\n",
			record.ID, record.FilePath())
	}
	return fmt.Sprintf("\n---\nView command: catpaw diagnose show %s\nFull record: %s\n",
		record.ID, record.FilePath())
}

func truncSuffixText(language string) string {
	if language == "zh" {
		return "\n...[诊断报告已截断，完整内容请查看本地记录]"
	}
	return "\n...[Report truncated, see local record for full content]"
}

// TruncateUTF8 truncates s to at most maxBytes bytes without breaking
// multi-byte UTF-8 characters. Exported for reuse across packages.
func TruncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
