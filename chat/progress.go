package chat

import (
	"fmt"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/diagnose/aiclient"
	"github.com/cprobe/catpaw/pkg/term"
)

func startSpinner(msg string) *term.Spinner { return term.StartSpinner(msg) }

func printThinkingDone(round int, elapsed time.Duration) {
	term.PrintThinkingDone(round, elapsed)
}

func printToolStart(name, argsDisplay string) { term.PrintToolStart(name, argsDisplay) }

func printToolDone(name, argsDisplay string, elapsed time.Duration, resultLen int, isErr bool) {
	term.PrintToolDone(name, argsDisplay, elapsed, resultLen, isErr)
}

func printToolOutput(result string, maxLines int) { term.PrintToolOutput(result, maxLines) }

func printAIReasoning(content string) { term.PrintAIReasoning(content) }

func formatToolArgsDisplay(name, rawArgs string) string {
	return diagnose.FormatToolArgsDisplay(name, rawArgs)
}

const (
	colorReset  = term.ColorReset
	colorCyan   = term.ColorCyan
	colorYellow = term.ColorYellow
	colorGreen  = term.ColorGreen
	colorRed    = term.ColorRed
	colorGray   = term.ColorGray
)

func printTokenUsage(usage aiclient.Usage, inputPrice, outputPrice float64) {
	if usage.TotalTokens == 0 {
		return
	}
	fmt.Printf("\n  %s── token: in=%d out=%d total=%d",
		colorGray, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	if inputPrice > 0 || outputPrice > 0 {
		cost := float64(usage.PromptTokens)*inputPrice/1e6 + float64(usage.CompletionTokens)*outputPrice/1e6
		fmt.Printf(" | cost=$%.4f", cost)
	}
	fmt.Printf(" ──%s\n", colorReset)
}
