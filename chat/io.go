package chat

import "time"

// ChatIO abstracts the I/O layer for chat conversations.
// Terminal implementations provide interactive approval and live progress display.
// Remote implementations route output via streaming callbacks.
type ChatIO interface {
	OnThinkingStart(round int)
	OnThinkingDone(round int, elapsed time.Duration)
	OnReasoning(text string)
	OnToolStart(name, argsDisplay string)
	OnToolDone(name, argsDisplay string, elapsed time.Duration, resultLen int, isErr bool)
	OnToolOutput(result string)
	// ApproveShell is called when exec_shell is requested.
	// Returns (approved, editedCmd). editedCmd replaces the original if non-empty.
	ApproveShell(command string) (approved bool, editedCmd string)
}
