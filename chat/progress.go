package chat

import (
	"fmt"

	"github.com/cprobe/digcore/diagnose/aiclient"
	"github.com/cprobe/digcore/pkg/term"
)

func printTokenUsage(usage aiclient.Usage, inputPrice, outputPrice float64) {
	if usage.TotalTokens == 0 {
		return
	}
	fmt.Printf("\n  %s── token: in=%d out=%d total=%d",
		term.ColorGray, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	if inputPrice > 0 || outputPrice > 0 {
		cost := float64(usage.PromptTokens)*inputPrice/1e6 + float64(usage.CompletionTokens)*outputPrice/1e6
		fmt.Printf(" | cost=$%.4f", cost)
	}
	fmt.Printf(" ──%s\n", term.ColorReset)
}
