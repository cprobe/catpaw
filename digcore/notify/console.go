package notify

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/types"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorGreen  = "\033[32m"
	colorGray   = "\033[90m"
	colorBold   = "\033[1m"
)

var isTTY = func() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}()

type ConsoleNotifier struct{}

func NewConsoleNotifier() *ConsoleNotifier { return &ConsoleNotifier{} }

func (c *ConsoleNotifier) Name() string { return "console" }

func (c *ConsoleNotifier) Forward(event *types.Event) bool {
	printEvent(event)
	return true
}

func (c *ConsoleNotifier) Comment(alertKey, comment string) bool {
	printComment(alertKey, comment)
	return true
}

func statusColor(status string) string {
	switch status {
	case types.EventStatusCritical:
		return colorRed
	case types.EventStatusWarning:
		return colorYellow
	case types.EventStatusInfo:
		return colorCyan
	case types.EventStatusOk:
		return colorGreen
	default:
		return colorReset
	}
}

func colorize(color, text string) string {
	if !isTTY {
		return text
	}
	return color + text + colorReset
}

func printEvent(event *types.Event) {
	ts := time.Unix(event.EventTime, 0).Format("2006-01-02 15:04:05")
	status := fmt.Sprintf("%-8s", event.EventStatus)

	var sb strings.Builder
	sb.WriteString(colorize(colorGray, ts))
	sb.WriteString("  ")
	sb.WriteString(colorize(colorBold+statusColor(event.EventStatus), status))
	sb.WriteString("  ")
	sb.WriteString(colorize(colorBold, event.AlertKey))
	sb.WriteString("\n")

	keys := sortedKeys(event.Labels)
	for _, k := range keys {
		sb.WriteString("    ")
		sb.WriteString(colorize(colorGray, k+"="))
		sb.WriteString(event.Labels[k])
		sb.WriteString("\n")
	}

	if len(event.Attrs) > 0 {
		for _, k := range sortedKeys(event.Attrs) {
			sb.WriteString("    ")
			sb.WriteString(colorize(colorGray, k+"="))
			sb.WriteString(event.Attrs[k])
			sb.WriteString("\n")
		}
	}

	desc := truncateHeadTail(event.Description, 500, 200)
	if desc != "" {
		sb.WriteString("    ")
		sb.WriteString(desc)
		sb.WriteString("\n")
	}

	fmt.Print(sb.String())
}

func printComment(alertKey, comment string) {
	ts := time.Now().Format("2006-01-02 15:04:05")

	var sb strings.Builder
	sb.WriteString(colorize(colorGray, ts))
	sb.WriteString("  ")
	sb.WriteString(colorize(colorBold+colorCyan, fmt.Sprintf("%-8s", "COMMENT")))
	sb.WriteString("  ")
	sb.WriteString(colorize(colorBold, alertKey))
	sb.WriteString("\n")
	if comment != "" {
		sb.WriteString("    ")
		sb.WriteString(comment)
		sb.WriteString("\n")
	}

	fmt.Print(sb.String())
}

// truncateHeadTail keeps the first headRunes and last tailRunes runes,
// replacing the middle with an ellipsis showing how many runes were omitted.
func truncateHeadTail(s string, headRunes, tailRunes int) string {
	runes := []rune(s)
	total := len(runes)
	if total <= headRunes+tailRunes {
		return s
	}
	omitted := total - headRunes - tailRunes
	pattern := "\n... [%d chars omitted] ...\n"
	if config.Config != nil && config.Config.AI.Language == "zh" {
		pattern = "\n... [省略 %d 字符] ...\n"
	}
	return string(runes[:headRunes]) +
		fmt.Sprintf(pattern, omitted) +
		string(runes[total-tailRunes:])
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
