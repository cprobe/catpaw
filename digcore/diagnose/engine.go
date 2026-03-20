package diagnose

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/diagnose/aiclient"
	"github.com/cprobe/catpaw/digcore/logger"
	"github.com/cprobe/catpaw/digcore/notify"
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
	fc := aiclient.NewFailoverClientForScene(cfg, "diagnose")

	state := NewDiagnoseState()
	state.Load()

	cwLimit := cfg.ContextWindowLimit()

	if cwLimit > 0 && cwLimit < 12800 {
		logger.Logger.Warnw("context_window is very small; system prompt with 70+ tools may consume most of the budget, leaving little room for diagnosis",
			"context_window_limit", cwLimit,
			"minimum_recommended", 16000)
	}

	return &DiagnoseEngine{
		registry:           registry,
		fc:                 fc,
		state:              state,
		cfg:                cfg,
		maxRounds:          cfg.MaxRounds,
		contextWindowLimit: cwLimit,
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
		logger.Logger.Infow("diagnose skipped: cooldown active", "key", key)
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

// RunDiagnoseStreaming runs a diagnosis with streaming output via cb.
// Unlike RunDiagnose, it uses the caller-provided ctx (can be cancelled externally),
// does not manage sem/cooldown/state, and does not save records to disk.
// Intended for remote sessions where the server manages lifecycle.
func (e *DiagnoseEngine) RunDiagnoseStreaming(ctx context.Context, req *DiagnoseRequest, cb StreamCallback) (report string, err error) {
	session := &DiagnoseSession{
		Record:    NewDiagnoseRecord(req),
		StartTime: time.Now(),
	}
	defer session.Close()

	defer func() {
		if r := recover(); r != nil {
			logger.Logger.Errorw("streaming_diagnose_panic",
				"plugin", req.Plugin, "target", req.Target,
				"panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	emitStream := func(delta, stage string, done bool, metadata map[string]any) {
		if cb != nil {
			cb(delta, stage, done, metadata)
		}
	}

	req.OnProgress = func(event ProgressEvent) {
		switch event.Type {
		case ProgressAIStart:
			emitStream(fmt.Sprintf("[Round %d] AI thinking...", event.Round), "thinking", false, nil)
		case ProgressAIDone:
			if event.Reasoning != "" {
				emitStream(event.Reasoning, "answer", false, nil)
			}
		case ProgressToolStart:
			emitStream(fmt.Sprintf("[Tool] %s %s", event.ToolName, event.ToolArgs), "tool_call", false, nil)
		case ProgressToolDone:
			status := "ok"
			if event.IsError {
				status = "error"
			}
			emitStream(fmt.Sprintf("[Tool done] %s (%s, %dB, %s)", event.ToolName, status, event.ResultLen, event.Duration), "tool_result", false, nil)
		}
	}

	logger.Logger.Infow("streaming_diagnose_started",
		"plugin", req.Plugin, "target", req.Target, "mode", req.Mode)

	report, err = e.diagnose(ctx, req, session)
	if err != nil {
		logger.Logger.Warnw("streaming_diagnose_failed",
			"plugin", req.Plugin, "target", req.Target, "error", err)
		return "", err
	}

	logger.Logger.Infow("streaming_diagnose_completed",
		"plugin", req.Plugin, "target", req.Target,
		"rounds", session.Record.AI.TotalRounds,
		"tokens", session.Record.AI.InputTokens+session.Record.AI.OutputTokens)

	return report, nil
}

func (e *DiagnoseEngine) diagnose(ctx context.Context, req *DiagnoseRequest, session *DiagnoseSession) (string, error) {
	if err := e.initSessionAccessor(ctx, req, session); err != nil {
		return "", fmt.Errorf("create accessor: %w", err)
	}

	if req.RuntimeOS == "" {
		req.RuntimeOS = runtime.GOOS
	}
	aiToolDefs, directTools := buildToolSet(e.registry, req)

	hostname, _ := os.Hostname()
	isRemote := isRemoteTarget(req.Target)
	directToolsStr := formatDirectTools(directTools)
	toolCatalog := e.registry.ListToolCatalogSmartForOS(req.RuntimeOS)
	var prompt string
	if req.Mode == ModeInspect {
		prompt = buildInspectPrompt(req, directToolsStr, toolCatalog, hostname, isRemote, e.cfg.Language)
	} else {
		prompt = buildSystemPrompt(req, directToolsStr, toolCatalog, hostname, isRemote, e.cfg.Language)
	}

	messages := make([]aiclient.Message, 0, e.maxRounds*4)
	messages = append(messages, aiclient.Message{Role: "system", Content: prompt})
	// Many OpenAI-compatible gateways (e.g. some vLLM/Qwen stacks) reject requests
	// that contain only a system message ("No user query found"). Chat REPL always
	// adds a user turn; diagnosis must do the same for the first API round.
	messages = append(messages, aiclient.Message{
		Role:    "user",
		Content: diagnoseUserKickoffMessage(req.Mode, e.cfg.Language),
	})
	ctx = aiclient.WithGatewayMetadata(ctx, aiclient.GatewayMetadata{
		DiagnoseID:    session.Record.ID,
		Plugin:        req.Plugin,
		Target:        req.Target,
		RequestSource: "diagnose",
	})

	estimatedTokens := aiclient.EstimateMessagesTokens(messages[:2])
	session.Record.AI.Model = e.cfg.PrimaryModelName()
	contextWarned := false

	progress := req.OnProgress // nil-safe: emitProgress checks

	for round := 0; round < e.maxRounds; round++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		if e.contextWindowLimit > 0 && estimatedTokens > e.contextWindowLimit {
			messages = aiclient.CompactMessages(messages, e.contextWindowLimit)
			estimatedTokens = aiclient.EstimateMessagesTokens(messages)
		}

		if e.contextWindowLimit > 0 && !contextWarned && estimatedTokens > e.contextWindowLimit*90/100 {
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

		emitProgress(progress, ProgressEvent{Type: ProgressAIStart, Round: round + 1})

		aiStart := time.Now()
		resp, modelName, err := e.fc.Chat(ctx, messages, aiToolDefs)
		aiElapsed := time.Since(aiStart)
		if err != nil {
			emitProgress(progress, ProgressEvent{Type: ProgressAIDone, Round: round + 1, Duration: aiElapsed, IsError: true})
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

		emitProgress(progress, ProgressEvent{
			Type:      ProgressAIDone,
			Round:     round + 1,
			Reasoning: content,
			Duration:  aiElapsed,
		})

		if len(toolCalls) == 0 {
			session.Record.AI.TotalRounds = round + 1
			return content, nil
		}

		messages = append(messages, aiclient.Message{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		})
		estimatedTokens += aiclient.EstimateMessageTokens(messages[len(messages)-1])

		roundRecord := RoundRecord{Round: round + 1}

		for _, tc := range toolCalls {
			argsDisplay := FormatToolArgsDisplay(tc.Function.Name, tc.Function.Arguments)

			emitProgress(progress, ProgressEvent{
				Type:     ProgressToolStart,
				Round:    round + 1,
				ToolName: tc.Function.Name,
				ToolArgs: argsDisplay,
			})

			toolCtx, toolCancel := context.WithTimeout(ctx, e.toolTimeout)
			toolStart := time.Now()

			result, toolErr := executeTool(toolCtx, e.registry, session, tc.Function.Name, tc.Function.Arguments)
			toolCancel()
			toolElapsed := time.Since(toolStart)

			isErr := toolErr != nil
			if isErr {
				result = "error: " + toolErr.Error()
			}
			truncated := TruncateOutput(result)

			emitProgress(progress, ProgressEvent{
				Type:       ProgressToolDone,
				Round:      round + 1,
				ToolName:   tc.Function.Name,
				ToolArgs:   argsDisplay,
				Duration:   toolElapsed,
				ResultLen:  len(result),
				IsError:    isErr,
				ToolOutput: truncated,
			})

			messages = append(messages, aiclient.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    truncated,
			})
			estimatedTokens += aiclient.EstimateMessageTokens(messages[len(messages)-1])

			roundRecord.ToolCalls = append(roundRecord.ToolCalls, ToolCallRecord{
				Name:       tc.Function.Name,
				Args:       ParseArgs(tc.Function.Arguments),
				Result:     TruncateForRecord(result),
				DurationMs: toolElapsed.Milliseconds(),
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

// forwardReport sends the diagnosis report as a comment attached to the
// original alert key when the notifier backend supports that capability.
func (e *DiagnoseEngine) forwardReport(req *DiagnoseRequest, record *DiagnoseRecord, report string) {
	comment := FormatReportComment(record, report, e.cfg.Language)
	now := time.Now().Unix()

	seen := make(map[string]bool, len(req.Events))
	for _, original := range req.Events {
		if seen[original.AlertKey] {
			continue
		}
		seen[original.AlertKey] = true
		if notify.ForwardComment(original.AlertKey, comment) {
			logger.Logger.Infow("diagnose report commented",
				"alert_key", original.AlertKey, "plugin", req.Plugin, "target", req.Target, "ts", now)
		} else {
			logger.Logger.Warnw("diagnose report comment failed",
				"alert_key", original.AlertKey, "plugin", req.Plugin, "target", req.Target, "ts", now)
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

// TrySem attempts to acquire a concurrency slot (shared with local diagnoses).
// Returns true if acquired, false if all slots are occupied.
func (e *DiagnoseEngine) TrySem() bool {
	select {
	case e.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// ReleaseSem releases one concurrency slot previously acquired via TrySem.
func (e *DiagnoseEngine) ReleaseSem() {
	<-e.sem
}

func emitProgress(cb ProgressCallback, event ProgressEvent) {
	if cb != nil {
		cb(event)
	}
}

// diagnoseUserKickoffMessage is the first user turn for alert/inspect diagnosis.
// The system prompt already holds full context; this line satisfies backends that
// require at least one role=user message in /v1/chat/completions.
func diagnoseUserKickoffMessage(mode string, lang string) string {
	isZH := lang == "" || lang == "zh"
	if mode == ModeInspect {
		if isZH {
			return "请根据上述巡检任务说明开始检查（可调用工具）。"
		}
		return "Begin the inspection as described in the system message above (you may use tools)."
	}
	if isZH {
		return "请根据上述告警信息开始根因诊断（可调用工具）。"
	}
	return "Begin root-cause diagnosis for the alert described in the system message above (you may use tools)."
}
