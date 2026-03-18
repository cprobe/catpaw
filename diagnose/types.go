package diagnose

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/cprobe/digcore/logger"
	"github.com/cprobe/digcore/types"
)

// ToolScope distinguishes where a diagnostic tool executes.
type ToolScope int

const (
	ToolScopeLocal  ToolScope = iota // Executes on the catpaw host (disk, cpu, mem)
	ToolScopeRemote                  // Needs a connection to the remote target (redis, mysql)
)

func (s ToolScope) String() string {
	switch s {
	case ToolScopeLocal:
		return "local"
	case ToolScopeRemote:
		return "remote"
	default:
		return "unknown"
	}
}

// ToolParam describes a single parameter accepted by a DiagnoseTool.
type ToolParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // "string", "int"
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// DiagnoseTool defines a diagnostic tool that the AI can invoke.
type DiagnoseTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  []ToolParam `json:"parameters,omitempty"`
	Scope       ToolScope   `json:"-"`
	SupportedOS []string    `json:"-"`

	Execute       func(ctx context.Context, args map[string]string) (string, error)                    `json:"-"`
	RemoteExecute func(ctx context.Context, session *DiagnoseSession, args map[string]string) (string, error) `json:"-"`
}

// SupportsOS reports whether the tool should be exposed on the given OS.
// Empty SupportedOS means all operating systems are allowed.
func (t DiagnoseTool) SupportsOS(goos string) bool {
	if len(t.SupportedOS) == 0 {
		return true
	}
	for _, os := range t.SupportedOS {
		if os == goos {
			return true
		}
	}
	return false
}

// ToolCategory groups related diagnostic tools under a plugin.
type ToolCategory struct {
	Name        string         // "redis", "disk", "cpu"
	Plugin      string         // source plugin name
	Description string         // one-line description for AI
	Scope       ToolScope      // local or remote
	Tools       []DiagnoseTool // tools in this category
}

// CheckSnapshot captures the current state of one alerting check at the moment
// the diagnosis is triggered. Produced by Gather(), consumed by the DiagnoseEngine.
type CheckSnapshot struct {
	Check         string `json:"check"`
	Status        string `json:"status"`
	CurrentValue  string `json:"current_value"`
	ThresholdDesc string `json:"threshold_desc,omitempty"`
	Description   string `json:"description"`
}

const (
	ModeAlert   = "alert"
	ModeInspect = "inspect"
)

// ProgressEventType identifies the kind of progress event fired by the engine.
type ProgressEventType int

const (
	ProgressAIStart   ProgressEventType = iota // AI call starting (spinner opportunity)
	ProgressAIDone                              // AI call completed
	ProgressToolStart                           // Tool invocation starting
	ProgressToolDone                            // Tool invocation completed
)

// ProgressEvent carries details about one progress milestone in a diagnosis run.
type ProgressEvent struct {
	Type      ProgressEventType
	Round     int
	ToolName  string
	ToolArgs  string
	Reasoning string        // non-empty on AIDone when the model emits reasoning
	Duration  time.Duration // set on AIDone / ToolDone
	ResultLen int           // set on ToolDone
	IsError   bool          // set on ToolDone
}

// ProgressCallback receives progress events during a diagnosis run.
// A nil callback is safe; the engine simply skips the call.
type ProgressCallback func(event ProgressEvent)

// StreamCallback receives streaming output during a remote diagnosis run.
// Called with incremental deltas; done=true on the final call.
// A nil callback is safe; the engine simply skips the call.
type StreamCallback func(delta, stage string, done bool, metadata map[string]any)

// DiagnoseRequest is produced by the DiagnoseAggregator after collecting
// alerts for the same target within the aggregation window.
// For inspect mode, Events and Checks are nil.
type DiagnoseRequest struct {
	Mode        string // "alert" (default) or "inspect"
	Events      []*types.Event
	Plugin      string
	Target      string
	RuntimeOS   string
	Checks      []CheckSnapshot
	InstanceRef any
	Timeout     time.Duration
	Cooldown    time.Duration
	Descriptions string           // remote diagnose: textual alert descriptions for AI context
	OnProgress   ProgressCallback // optional; nil means no progress output
}

// DiagnoseSession manages the lifecycle of a single diagnosis run.
// All remote tool calls within the same diagnosis share one Accessor (TCP connection).
type DiagnoseSession struct {
	Accessor  any             // shared remote Accessor, created by the plugin's factory
	Record    *DiagnoseRecord
	StartTime time.Time
	mu        sync.Mutex
}

// Close releases the shared Accessor if it implements io.Closer.
func (s *DiagnoseSession) Close() {
	if s.Accessor == nil {
		return
	}
	if closer, ok := s.Accessor.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			logger.Logger.Debugw("session accessor close error", "error", err)
		}
	}
}

// DiagnoseRecord stores the full trace of a single diagnosis run,
// written as a JSON file under state.d/diagnoses/.
type DiagnoseRecord struct {
	ID         string        `json:"id"`
	Mode       string        `json:"mode"` // "alert" or "inspect"
	Status     string        `json:"status"` // success, failed, cancelled, timeout
	Error      string        `json:"error,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	DurationMs int64         `json:"duration_ms"`
	Alert      AlertRecord   `json:"alert"`
	AI         AIRecord      `json:"ai"`
	Rounds     []RoundRecord `json:"rounds"`
	Report     string        `json:"report,omitempty"`
}

// AlertRecord stores the alert context that triggered the diagnosis.
type AlertRecord struct {
	Plugin string          `json:"plugin"`
	Target string          `json:"target"`
	Checks []CheckSnapshot `json:"checks"`
}

// AIRecord stores AI model usage info for this diagnosis.
type AIRecord struct {
	Model        string `json:"model"`
	TotalRounds  int    `json:"total_rounds"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// RoundRecord stores one round of AI interaction.
type RoundRecord struct {
	Round       int              `json:"round"`
	ToolCalls   []ToolCallRecord `json:"tool_calls,omitempty"`
	AIReasoning string           `json:"ai_reasoning,omitempty"`
}

// ToolCallRecord stores one tool invocation within a round.
type ToolCallRecord struct {
	Name       string            `json:"name"`
	Args       map[string]string `json:"args,omitempty"`
	Result     string            `json:"result"`
	DurationMs int64             `json:"duration_ms"`
}

// AccessorFactory creates a shared Accessor for a remote plugin.
// The engine calls this once per DiagnoseSession.
type AccessorFactory func(ctx context.Context, instanceRef any) (any, error)

// SeverityRank returns a numeric rank for severity comparison.
// Higher rank = more severe.
func SeverityRank(status string) int {
	switch status {
	case types.EventStatusCritical:
		return 3
	case types.EventStatusWarning:
		return 2
	case types.EventStatusInfo:
		return 1
	default:
		return 0
	}
}
