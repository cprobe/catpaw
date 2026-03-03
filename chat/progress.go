package chat

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// --- spinner: animated progress for long-running phases (AI thinking) ---

var spinnerFrames = []string{"|", "/", "-", "\\"}

type spinner struct {
	done chan struct{}
	wg   sync.WaitGroup
}

func startSpinner(msg string) *spinner {
	s := &spinner{done: make(chan struct{})}
	s.wg.Add(1)
	go s.run(msg)
	return s
}

func (s *spinner) run(msg string) {
	defer s.wg.Done()
	start := time.Now()
	i := 0
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			fmt.Print("\r\033[K")
			return
		case <-ticker.C:
			elapsed := time.Since(start).Truncate(time.Second)
			fmt.Printf("\r\033[K  %s%s%s %s (%v)",
				colorCyan, spinnerFrames[i%len(spinnerFrames)], colorReset, msg, elapsed)
			i++
		}
	}
}

func (s *spinner) stop() {
	close(s.done)
	s.wg.Wait()
}

// --- ANSI color constants ---

const (
	colorReset  = "\033[0m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorGray   = "\033[90m"
)

// --- progress output for chat ---

func printThinkingDone(round int, elapsed time.Duration) {
	fmt.Printf("  %s[round %d]%s %s⟳ thinking%s %s(%s)%s\n",
		colorGray, round, colorReset,
		colorCyan, colorReset,
		colorGray, fmtDur(elapsed), colorReset)
}

func printToolStart(name, argsDisplay string) {
	if argsDisplay != "" {
		fmt.Printf("  %s▶ %s%s %s%s%s", colorYellow, name, colorReset, colorGray, argsDisplay, colorReset)
	} else {
		fmt.Printf("  %s▶ %s%s", colorYellow, name, colorReset)
	}
}

func printToolDone(name, argsDisplay string, elapsed time.Duration, resultLen int, isErr bool) {
	status := fmt.Sprintf("%s✓ %s%s", colorGreen, fmtBytes(resultLen), colorReset)
	if isErr {
		status = fmt.Sprintf("%s✗ error%s", colorRed, colorReset)
	}
	if argsDisplay != "" {
		fmt.Printf("\r\033[K  %s▶ %s%s %s%s%s %s(%s)%s %s\n",
			colorYellow, name, colorReset,
			colorGray, argsDisplay, colorReset,
			colorGray, fmtDur(elapsed), colorReset,
			status)
	} else {
		fmt.Printf("\r\033[K  %s▶ %s%s %s(%s)%s %s\n",
			colorYellow, name, colorReset,
			colorGray, fmtDur(elapsed), colorReset,
			status)
	}
}

func printToolOutput(result string, maxLines int) {
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	showing := len(lines)
	if showing > maxLines {
		showing = maxLines
	}
	for i := 0; i < showing; i++ {
		fmt.Printf("  %s│ %s%s\n", colorGray, truncLine(lines[i], 120), colorReset)
	}
	if len(lines) > showing {
		fmt.Printf("  %s│ ... (%d more lines)%s\n", colorGray, len(lines)-showing, colorReset)
	}
}

func printAIReasoning(content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	lines := strings.Split(content, "\n")
	showing := len(lines)
	if showing > 3 {
		showing = 3
	}
	for i := 0; i < showing; i++ {
		fmt.Printf("  %s💭 %s%s\n", colorGray, truncLine(lines[i], 120), colorReset)
	}
	if len(lines) > showing {
		fmt.Printf("  %s💭 ... (%d more lines)%s\n", colorGray, len(lines)-showing, colorReset)
	}
}

// truncLine truncates a string to maxRunes runes, appending "..." if truncated.
// Safe for multi-byte UTF-8 characters.
func truncLine(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes-3]) + "..."
}

func formatToolArgsDisplay(name, rawArgs string) string {
	args := parseArgs(rawArgs)
	switch name {
	case "call_tool":
		toolName := args["name"]
		toolArgs := args["tool_args"]
		if toolArgs != "" && toolArgs != "{}" {
			return toolName + " " + toolArgs
		}
		return toolName
	case "list_tools":
		return args["category"]
	case "exec_shell":
		cmd := args["command"]
		if utf8.RuneCountInString(cmd) > 80 {
			runes := []rune(cmd)
			cmd = string(runes[:77]) + "..."
		}
		return cmd
	default:
		return ""
	}
}

func fmtDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func fmtBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}
