package diagnose

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/diagnose/aiclient"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/notify"
	"github.com/cprobe/catpaw/types"
)

// DiagnoseEngine is the central coordinator for AI-powered diagnosis.
type DiagnoseEngine struct {
	registry *ToolRegistry
	fc       *aiclient.FailoverClient
	state    *DiagnoseState
	cfg      config.AIConfig

	maxRounds          int
	contextWindowLimit int
	toolTimeout        time.Duration

	// in-flight tracking for graceful shutdown
	mu       sync.Mutex
	inFlight map[string]context.CancelFunc // "plugin::target" → cancel
	sem      chan struct{}                 // concurrency limiter
	stopped  atomic.Bool
}

// NewDiagnoseEngine creates a new engine from global config.
func NewDiagnoseEngine(registry *ToolRegistry, cfg config.AIConfig) *DiagnoseEngine {
	fc := aiclient.NewFailoverClient(cfg)

	state := NewDiagnoseState()
	state.Load()

	primary := cfg.PrimaryModel()
	contextWindow := primary.ContextWindow
	if contextWindow <= 0 {
		contextWindow = 128000
	}

	return &DiagnoseEngine{
		registry:           registry,
		fc:                 fc,
		state:              state,
		cfg:                cfg,
		maxRounds:          cfg.MaxRounds,
		contextWindowLimit: contextWindow * 80 / 100,
		toolTimeout:        time.Duration(cfg.ToolTimeout),
		inFlight:           make(map[string]context.CancelFunc),
		sem:                make(chan struct{}, cfg.MaxConcurrentDiagnoses),
	}
}

// Submit attempts to schedule a diagnosis. It respects cooldown, daily token
// limits, and concurrency bounds. Returns immediately; actual diagnosis runs
// in a goroutine.
func (e *DiagnoseEngine) Submit(req *DiagnoseRequest) {
	if e.stopped.Load() {
		return
	}

	key := req.Plugin + "::" + req.Target

	if e.state.IsCooldownActive(req.Plugin, req.Target) {
		logger.Logger.Debugw("diagnose skipped: cooldown active", "key", key)
		return
	}

	if e.state.IsDailyLimitReached(e.cfg.DailyTokenLimit) {
		logger.Logger.Warnw("diagnose skipped: daily token limit reached",
			"usage", e.state.FormatUsage(), "limit", e.cfg.DailyTokenLimit)
		return
	}

	select {
	case e.sem <- struct{}{}:
		go func() {
			defer func() { <-e.sem }()
			e.RunDiagnose(req)
		}()
	default:
		logger.Logger.Warnw("diagnose skipped: concurrency limit reached",
			"key", key, "limit", e.cfg.MaxConcurrentDiagnoses)
	}
}

// RunDiagnose is the goroutine entry point. It includes panic recovery,
// session lifecycle, cooldown update, and state persistence.
// Returns the DiagnoseRecord so callers (e.g. inspect CLI) can inspect results.
func (e *DiagnoseEngine) RunDiagnose(req *DiagnoseRequest) *DiagnoseRecord {
	session := &DiagnoseSession{
		Record:    NewDiagnoseRecord(req),
		StartTime: time.Now(),
	}

	defer func() {
		if r := recover(); r != nil {
			logger.Logger.Errorw("diagnose panic recovered",
				"target", req.Target, "panic", r, "stack", string(debug.Stack()))
			session.Record.Status = "failed"
			session.Record.Error = fmt.Sprintf("panic: %v", r)
			if err := session.Record.Save(); err != nil {
				logger.Logger.Warnw("failed to save panic record", "error", err)
			}
		}
	}()
	defer session.Close()

	timeout := req.Timeout
	if timeout == 0 {
		timeout = time.Duration(e.cfg.RequestTimeout) * time.Duration(e.maxRounds)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	e.registerInFlight(req, cancel)
	defer e.unregisterInFlight(req)

	logger.Logger.Infow("diagnose started",
		"plugin", req.Plugin, "target", req.Target, "checks", len(req.Checks))

	report, err := e.diagnose(ctx, req, session)
	if err != nil {
		session.Record.Status = "failed"
		session.Record.Error = err.Error()
		logger.Logger.Warnw("diagnose failed",
			"plugin", req.Plugin, "target", req.Target, "error", err)
	} else {
		session.Record.Status = "success"
		session.Record.Report = report
		logger.Logger.Infow("diagnose completed",
			"plugin", req.Plugin, "target", req.Target,
			"rounds", session.Record.AI.TotalRounds,
			"tokens", session.Record.AI.InputTokens+session.Record.AI.OutputTokens)
	}
	session.Record.DurationMs = time.Since(session.StartTime).Milliseconds()

	if err := session.Record.Save(); err != nil {
		logger.Logger.Warnw("failed to save diagnose record", "error", err)
	}

	if report != "" && len(req.Events) > 0 {
		e.forwardReport(req, session.Record, report)
	}

	e.state.AddTokens(session.Record.AI.InputTokens, session.Record.AI.OutputTokens)
	if req.Mode != ModeInspect {
		e.state.UpdateCooldown(req.Plugin, req.Target, req.Cooldown)
	}
	e.state.Save()

	return session.Record
}

func (e *DiagnoseEngine) diagnose(ctx context.Context, req *DiagnoseRequest, session *DiagnoseSession) (string, error) {
	if err := e.initSessionAccessor(ctx, req, session); err != nil {
		return "", fmt.Errorf("create accessor: %w", err)
	}

	aiToolDefs, directTools := buildToolSet(e.registry, req)

	hostname, _ := os.Hostname()
	isRemote := isRemoteTarget(req.Target)
	directToolsStr := formatDirectTools(directTools)
	toolCatalog := e.registry.ListToolCatalogSmart()
	var prompt string
	if req.Mode == ModeInspect {
		prompt = buildInspectPrompt(req, directToolsStr, toolCatalog, hostname, isRemote, e.cfg.Language)
	} else {
		prompt = buildSystemPrompt(req, directToolsStr, toolCatalog, hostname, isRemote, e.cfg.Language)
	}

	messages := []aiclient.Message{
		{Role: "system", Content: prompt},
	}

	estimatedTokens := aiclient.EstimateTokensChinese(prompt)
	session.Record.AI.Model = e.cfg.PrimaryModelName()
	contextWarned := false

	for round := 0; round < e.maxRounds; round++ {
		if !contextWarned && estimatedTokens > e.contextWindowLimit {
			contextWarned = true
			messages = append(messages, aiclient.Message{
				Role:    "user",
				Content: "上下文空间即将耗尽。请基于目前收集到的信息，立即输出最终诊断报告。不要再调用任何工具。",
			})
		} else if round == e.maxRounds-1 {
			messages = append(messages, aiclient.Message{
				Role:    "user",
				Content: "你已使用了所有可用的工具调用轮次。请基于目前收集到的信息，立即输出最终诊断报告。不要再调用任何工具。",
			})
		}

		resp, modelName, err := e.fc.Chat(ctx, messages, aiToolDefs)
		if err != nil {
			return "", fmt.Errorf("AI API error at round %d: %w", round+1, err)
		}
		session.Record.AI.Model = modelName

		if resp.Usage.TotalTokens > 0 {
			estimatedTokens = resp.Usage.TotalTokens
		}
		session.Record.AI.InputTokens += resp.Usage.PromptTokens
		session.Record.AI.OutputTokens += resp.Usage.CompletionTokens

		content := ""
		var toolCalls []aiclient.ToolCall
		if len(resp.Choices) > 0 {
			content = resp.Choices[0].Message.Content
			toolCalls = resp.Choices[0].Message.ToolCalls
		}

		if len(toolCalls) == 0 {
			session.Record.AI.TotalRounds = round + 1
			return content, nil
		}

		messages = append(messages, aiclient.Message{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		})
		estimatedTokens += aiclient.EstimateTokensChinese(content)

		roundRecord := RoundRecord{Round: round + 1}

		for _, tc := range toolCalls {
			toolCtx, toolCancel := context.WithTimeout(ctx, e.toolTimeout)
			toolStart := time.Now()

			result, toolErr := executeTool(toolCtx, e.registry, session, tc.Function.Name, tc.Function.Arguments)
			toolCancel()

			if toolErr != nil {
				result = "error: " + toolErr.Error()
			}
			truncated := TruncateOutput(result)

			messages = append(messages, aiclient.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    truncated,
			})
			estimatedTokens += aiclient.EstimateTokensChinese(truncated)

			roundRecord.ToolCalls = append(roundRecord.ToolCalls, ToolCallRecord{
				Name:       tc.Function.Name,
				Args:       ParseArgs(tc.Function.Arguments),
				Result:     TruncateForRecord(result),
				DurationMs: time.Since(toolStart).Milliseconds(),
			})
		}
		roundRecord.AIReasoning = content
		session.Record.Rounds = append(session.Record.Rounds, roundRecord)
	}

	session.Record.AI.TotalRounds = e.maxRounds
	if e.cfg.Language == "zh" {
		return "[诊断未完成] 已达到最大轮次限制，AI 未能在限定轮次内输出最终报告。", nil
	}
	return "[Incomplete] Max round limit reached, AI did not produce a final report.", nil
}

func (e *DiagnoseEngine) initSessionAccessor(ctx context.Context, req *DiagnoseRequest, session *DiagnoseSession) error {
	if req.InstanceRef == nil {
		return nil
	}
	if !e.registry.HasAccessorFactory(req.Plugin) {
		return nil
	}
	accessor, err := e.registry.CreateAccessor(ctx, req.Plugin, req.InstanceRef)
	if err != nil {
		return fmt.Errorf("create accessor for %s::%s: %w", req.Plugin, req.Target, err)
	}
	session.Accessor = accessor
	return nil
}

// registerInFlight tracks a running diagnosis for graceful shutdown.
// Overwrites any existing entry for the same key; this is safe because
// alert-mode diagnoses are protected by cooldown, and inspect-mode is
// CLI-driven (no concurrent runs for the same target).
func (e *DiagnoseEngine) registerInFlight(req *DiagnoseRequest, cancel context.CancelFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inFlight[req.Plugin+"::"+req.Target] = cancel
}

func (e *DiagnoseEngine) unregisterInFlight(req *DiagnoseRequest) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.inFlight, req.Plugin+"::"+req.Target)
}

// Shutdown cancels all in-flight diagnoses for graceful termination.
func (e *DiagnoseEngine) Shutdown() {
	e.stopped.Store(true)
	e.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(e.inFlight))
	for _, cancel := range e.inFlight {
		cancels = append(cancels, cancel)
	}
	e.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	logger.Logger.Infow("diagnose engine shutdown", "cancelled", len(cancels))
}

// forwardReport sends the diagnosis report to all configured notifiers
// as a new Event with the same AlertKey but a fresh EventTime and Description.
func (e *DiagnoseEngine) forwardReport(req *DiagnoseRequest, record *DiagnoseRecord, report string) {
	desc := FormatReportDescription(record, report, e.cfg.Language)
	now := time.Now().Unix()

	seen := make(map[string]bool, len(req.Events))
	for _, original := range req.Events {
		if seen[original.AlertKey] {
			continue
		}
		seen[original.AlertKey] = true

		labels := make(map[string]string, len(original.Labels))
		for k, v := range original.Labels {
			labels[k] = v
		}

		var attrs map[string]string
		if len(original.Attrs) > 0 {
			attrs = make(map[string]string, len(original.Attrs))
			for k, v := range original.Attrs {
				attrs[k] = v
			}
		}

		event := &types.Event{
			EventTime:         now,
			EventStatus:       original.EventStatus,
			AlertKey:          original.AlertKey,
			Labels:            labels,
			Attrs:             attrs,
			Description:       desc,
			DescriptionFormat: types.DescFormatMarkdown,
		}

		if notify.Forward(event) {
			logger.Logger.Infow("diagnose report forwarded",
				"alert_key", event.AlertKey, "plugin", req.Plugin, "target", req.Target)
		} else {
			logger.Logger.Warnw("diagnose report forward failed",
				"alert_key", event.AlertKey, "plugin", req.Plugin, "target", req.Target)
		}
	}
}

// State returns the engine's state for external inspection.
func (e *DiagnoseEngine) State() *DiagnoseState {
	return e.state
}

// Registry returns the engine's tool registry.
func (e *DiagnoseEngine) Registry() *ToolRegistry {
	return e.registry
}
