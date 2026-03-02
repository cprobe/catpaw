package diagnose

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const maxDescriptionBytes = 2048

// FormatReportForFlashDuty builds a concise diagnosis report suitable for
// FlashDuty's description field (max 2048 bytes). It prioritizes:
//  1. Header (plugin, target, time, status)
//  2. AI summary/report body
//  3. A footer with record path for full details
//
// If the report exceeds 2048 bytes, the AI body is truncated.
func FormatReportForFlashDuty(record *DiagnoseRecord, report string) string {
	header := formatHeader(record)
	footer := formatFooter(record)

	headerBytes := len(header)
	footerBytes := len(footer)
	overhead := headerBytes + footerBytes

	if overhead >= maxDescriptionBytes {
		return truncateUTF8(header, maxDescriptionBytes)
	}

	budget := maxDescriptionBytes - overhead
	body := report
	if len(body) > budget {
		body = truncateUTF8(body, budget-len("\n...[诊断报告已截断，完整内容请查看本地记录]")) + "\n...[诊断报告已截断，完整内容请查看本地记录]"
		if len(body) > budget {
			body = truncateUTF8(report, budget)
		}
	}

	return header + body + footer
}

func formatHeader(record *DiagnoseRecord) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔍 AI 诊断报告 [%s]\n", record.Status))
	b.WriteString(fmt.Sprintf("插件: %s | 目标: %s\n", record.Alert.Plugin, record.Alert.Target))
	b.WriteString(fmt.Sprintf("诊断时间: %s | 耗时: %dms | AI轮次: %d\n",
		record.CreatedAt.Format(time.DateTime),
		record.DurationMs,
		record.AI.TotalRounds))
	b.WriteString("---\n")
	return b.String()
}

func formatFooter(record *DiagnoseRecord) string {
	return fmt.Sprintf("\n---\n完整记录: %s\n", record.FilePath())
}

// truncateUTF8 truncates s to at most maxBytes bytes without breaking
// multi-byte UTF-8 characters.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
