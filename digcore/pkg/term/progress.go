// TODO(V2): detect non-TTY (e.g. stdout redirected to file) and degrade
// Spinner to plain newline-based output to avoid \r\033[K noise in logs.
package term

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	ColorReset  = "\033[0m"
	ColorCyan   = "\033[36m"
	ColorYellow = "\033[33m"
	ColorGreen  = "\033[32m"
	ColorRed    = "\033[31m"
	ColorGray   = "\033[90m"
)

var spinnerFrames = []string{"|", "/", "-", "\\"}

type Spinner struct {
	done chan struct{}
	wg   sync.WaitGroup
}

func StartSpinner(msg string) *Spinner {
	s := &Spinner{done: make(chan struct{})}
	s.wg.Add(1)
	go s.run(msg)
	return s
}

func (s *Spinner) run(msg string) {
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
				ColorCyan, spinnerFrames[i%len(spinnerFrames)], ColorReset, msg, elapsed)
			i++
		}
	}
}

func (s *Spinner) Stop() {
	close(s.done)
	s.wg.Wait()
}

func PrintThinkingDone(round int, elapsed time.Duration) {
	fmt.Printf("  %s[round %d]%s %s⟳ thinking%s %s(%s)%s\n",
		ColorGray, round, ColorReset,
		ColorCyan, ColorReset,
		ColorGray, FmtDur(elapsed), ColorReset)
}

func PrintToolStart(name, argsDisplay string) {
	if argsDisplay != "" {
		fmt.Printf("  %s▶ %s%s %s%s%s", ColorYellow, name, ColorReset, ColorGray, argsDisplay, ColorReset)
	} else {
		fmt.Printf("  %s▶ %s%s", ColorYellow, name, ColorReset)
	}
}

func PrintToolDone(name, argsDisplay string, elapsed time.Duration, resultLen int, isErr bool) {
	status := fmt.Sprintf("%s✓ %s%s", ColorGreen, FmtBytes(resultLen), ColorReset)
	if isErr {
		status = fmt.Sprintf("%s✗ error%s", ColorRed, ColorReset)
	}
	if argsDisplay != "" {
		fmt.Printf("\r\033[K  %s▶ %s%s %s%s%s %s(%s)%s %s\n",
			ColorYellow, name, ColorReset,
			ColorGray, argsDisplay, ColorReset,
			ColorGray, FmtDur(elapsed), ColorReset,
			status)
	} else {
		fmt.Printf("\r\033[K  %s▶ %s%s %s(%s)%s %s\n",
			ColorYellow, name, ColorReset,
			ColorGray, FmtDur(elapsed), ColorReset,
			status)
	}
}

func PrintToolOutput(result string, maxLines int) {
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	showing := len(lines)
	if showing > maxLines {
		showing = maxLines
	}
	for i := 0; i < showing; i++ {
		fmt.Printf("  %s│ %s%s\n", ColorGray, TruncLine(lines[i], 120), ColorReset)
	}
	if len(lines) > showing {
		fmt.Printf("  %s│ ... (%d more lines)%s\n", ColorGray, len(lines)-showing, ColorReset)
	}
}

func PrintAIReasoning(content string) {
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
		fmt.Printf("  %s💭 %s%s\n", ColorGray, TruncLine(lines[i], 120), ColorReset)
	}
	if len(lines) > showing {
		fmt.Printf("  %s💭 ... (%d more lines)%s\n", ColorGray, len(lines)-showing, ColorReset)
	}
}

// TruncLine truncates a string to maxRunes runes, appending "..." if truncated.
func TruncLine(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes-3]) + "..."
}

func FmtDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func FmtBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}
