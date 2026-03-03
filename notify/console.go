package notify

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cprobe/catpaw/types"
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

	desc := event.Description
	if len(desc) > 500 {
		desc = desc[:500]
		for len(desc) > 0 && !utf8.ValidString(desc) {
			desc = desc[:len(desc)-1]
		}
		desc += "..."
	}
	if desc != "" {
		sb.WriteString("    ")
		sb.WriteString(desc)
		sb.WriteString("\n")
	}

	fmt.Print(sb.String())
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
