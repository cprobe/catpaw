package diagnose

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/cprobe/catpaw/types"
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

	Execute       func(ctx context.Context, args map[string]string) (string, error)                    `json:"-"`
	RemoteExecute func(ctx context.Context, session *DiagnoseSession, args map[string]string) (string, error) `json:"-"`
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
	Check             string `json:"check"`
	Status            string `json:"status"`
	CurrentValue      string `json:"current_value"`
	WarningThreshold  string `json:"warning_threshold,omitempty"`
	CriticalThreshold string `json:"critical_threshold,omitempty"`
	Description       string `json:"description"`
}

// DiagnoseRequest is produced by the DiagnoseAggregator after collecting
// alerts for the same target within the aggregation window.
type DiagnoseRequest struct {
	Events      []*types.Event
	Plugin      string
	Target      string
	Checks      []CheckSnapshot
	InstanceRef any
	Session     *DiagnoseSession
	Timeout     time.Duration
	Cooldown    time.Duration
}

// DiagnoseSession manages the lifecycle of a single diagnosis run.
// All remote tool calls within the same diagnosis share one Accessor (TCP connection).
type DiagnoseSession struct {
	Request   *DiagnoseRequest
	Accessor  any              // shared remote Accessor, created by the plugin's factory
	Record    *DiagnoseRecord
	StartTime time.Time
	mu        sync.Mutex
}

// LockAccessor acquires the accessor mutex for concurrent tool execution safety.
func (s *DiagnoseSession) LockAccessor() {
	s.mu.Lock()
}

// UnlockAccessor releases the accessor mutex.
func (s *DiagnoseSession) UnlockAccessor() {
	s.mu.Unlock()
}

// Close releases the shared Accessor if it implements io.Closer.
func (s *DiagnoseSession) Close() {
	if s.Accessor == nil {
		return
	}
	if closer, ok := s.Accessor.(io.Closer); ok {
		closer.Close()
	}
}

// DiagnoseRecord stores the full trace of a single diagnosis run,
// written as a JSON file under state.d/diagnoses/.
type DiagnoseRecord struct {
	ID         string       `json:"id"`
	Status     string       `json:"status"` // success, failed, cancelled, timeout
	Error      string       `json:"error,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	DurationMs int64        `json:"duration_ms"`
	Alert      AlertRecord  `json:"alert"`
	AI         AIRecord     `json:"ai"`
	Rounds     []RoundRecord `json:"rounds"`
	Report     string       `json:"report,omitempty"`
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
