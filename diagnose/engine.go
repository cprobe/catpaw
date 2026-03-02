package diagnose

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/diagnose/aiclient"
	"github.com/cprobe/catpaw/flashduty"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/types"
)

// DiagnoseEngine is the central coordinator for AI-powered diagnosis.
type DiagnoseEngine struct {
	registry *ToolRegistry
	client   *aiclient.Client
	state    *DiagnoseState
	cfg      config.AIConfig

	maxRounds          int
	contextWindowLimit int
	toolTimeout        time.Duration
	retryBackoff       time.Duration
	maxRetries         int

	// in-flight tracking for graceful shutdown
	mu       sync.Mutex
	inFlight map[string]context.CancelFunc // "plugin::target" → cancel
	sem      chan struct{}                 // concurrency limiter
	stopped  bool
}

// NewDiagnoseEngine creates a new engine from global config.
func NewDiagnoseEngine(registry *ToolRegistry, cfg config.AIConfig) *DiagnoseEngine {
	client := aiclient.NewClient(aiclient.ClientConfig{
		BaseURL:        cfg.BaseURL,
		APIKey:         cfg.APIKey,
		Model:          cfg.Model,
		MaxTokens:      cfg.MaxTokens,
		RequestTimeout: time.Duration(cfg.RequestTimeout),
	})

	state := NewDiagnoseState()
	state.Load()

	contextWindow := 128000
	if cfg.MaxTokens > 0 && cfg.MaxTokens < contextWindow {
		contextWindow = cfg.MaxTokens * 32
	}

	return &DiagnoseEngine{
		registry:           registry,
		client:             client,
		state:              state,
		cfg:                cfg,
		maxRounds:          cfg.MaxRounds,
		contextWindowLimit: contextWindow * 80 / 100,
		toolTimeout:        time.Duration(cfg.ToolTimeout),
		retryBackoff:       time.Duration(cfg.RetryBackoff),
		maxRetries:         cfg.MaxRetries,
		inFlight:           make(map[string]context.CancelFunc),
		sem:                make(chan struct{}, cfg.MaxConcurrentDiagnoses),
	}
}

// Submit attempts to schedule a diagnosis. It respects cooldown, daily token
// limits, and concurrency bounds. Returns immediately; actual diagnosis runs
// in a goroutine.
func (e *DiagnoseEngine) Submit(req *DiagnoseRequest) {
	if e.stopped {
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
func (e *DiagnoseEngine) RunDiagnose(req *DiagnoseRequest) {
	req.Session = &DiagnoseSession{
		Request:   req,
		Record:    NewDiagnoseRecord(req),
		StartTime: time.Now(),
	}

	defer func() {
		if r := recover(); r != nil {
			logger.Logger.Errorw("diagnose panic recovered",
				"target", req.Target, "panic", r, "stack", string(debug.Stack()))
			req.Session.Record.Status = "failed"
			req.Session.Record.Error = fmt.Sprintf("panic: %v", r)
			if err := req.Session.Record.Save(); err != nil {
				logger.Logger.Warnw("failed to save panic record", "error", err)
			}
		}
	}()
	defer req.Session.Close()

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

	report, err := e.diagnose(ctx, req)
	if err != nil {
		req.Session.Record.Status = "failed"
		req.Session.Record.Error = err.Error()
		logger.Logger.Warnw("diagnose failed",
			"plugin", req.Plugin, "target", req.Target, "error", err)
	} else {
		req.Session.Record.Status = "success"
		req.Session.Record.Report = report
		logger.Logger.Infow("diagnose completed",
			"plugin", req.Plugin, "target", req.Target,
			"rounds", req.Session.Record.AI.TotalRounds,
			"tokens", req.Session.Record.AI.InputTokens+req.Session.Record.AI.OutputTokens)
	}
	req.Session.Record.DurationMs = time.Since(req.Session.StartTime).Milliseconds()

	if err := req.Session.Record.Save(); err != nil {
		logger.Logger.Warnw("failed to save diagnose record", "error", err)
	}

	if report != "" && len(req.Events) > 0 {
		e.forwardReport(req, report)
	}

	e.state.AddTokens(req.Session.Record.AI.InputTokens, req.Session.Record.AI.OutputTokens)
	e.state.UpdateCooldown(req.Plugin, req.Target, req.Cooldown)
	e.state.Save()
}

func (e *DiagnoseEngine) diagnose(ctx context.Context, req *DiagnoseRequest) (string, error) {
	if err := e.initSessionAccessor(ctx, req); err != nil {
		return "", fmt.Errorf("create accessor: %w", err)
	}

	aiToolDefs, directTools := buildToolSet(e.registry, req)

	hostname, _ := os.Hostname()
	isRemote := isRemoteTarget(req.Target)
	prompt := buildSystemPrompt(req, formatDirectTools(directTools), hostname, isRemote)

	messages := []aiclient.Message{
		{Role: "system", Content: prompt},
	}

	estimatedTokens := aiclient.EstimateTokensChinese(prompt)
	retryCfg := aiclient.RetryConfig{
		MaxRetries:   e.maxRetries,
		RetryBackoff: e.retryBackoff,
	}

	for round := 0; round < e.maxRounds; round++ {
		if round == e.maxRounds-1 {
			messages = append(messages, aiclient.Message{
				Role:    "user",
				Content: "你已使用了所有可用的工具调用轮次。请基于目前收集到的信息，立即输出最终诊断报告。不要再调用任何工具。",
			})
		}

		if estimatedTokens > e.contextWindowLimit {
			messages = append(messages, aiclient.Message{
				Role:    "user",
				Content: "上下文空间即将耗尽。请基于目前收集到的信息，立即输出最终诊断报告。",
			})
		}

		resp, err := aiclient.ChatWithRetry(ctx, e.client, retryCfg, messages, aiToolDefs)
		if err != nil {
			return "", fmt.Errorf("AI API error at round %d: %w", round+1, err)
		}

		if resp.Usage.TotalTokens > 0 {
			estimatedTokens = resp.Usage.TotalTokens
		}
		req.Session.Record.AI.InputTokens += resp.Usage.PromptTokens
		req.Session.Record.AI.OutputTokens += resp.Usage.CompletionTokens
		req.Session.Record.AI.Model = e.cfg.Model

		content := ""
		var toolCalls []aiclient.ToolCall
		if len(resp.Choices) > 0 {
			content = resp.Choices[0].Message.Content
			toolCalls = resp.Choices[0].Message.ToolCalls
		}

		if len(toolCalls) == 0 {
			req.Session.Record.AI.TotalRounds = round + 1
			return content, nil
		}

		// Build assistant message with tool calls
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

			result, toolErr := executeTool(toolCtx, e.registry, req, tc.Function.Name, tc.Function.Arguments)
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
				Args:       parseArgs(tc.Function.Arguments),
				Result:     result,
				DurationMs: time.Since(toolStart).Milliseconds(),
			})
		}
		roundRecord.AIReasoning = content
		req.Session.Record.Rounds = append(req.Session.Record.Rounds, roundRecord)
	}

	req.Session.Record.AI.TotalRounds = e.maxRounds
	return "[诊断未完成] 已达到最大轮次限制，AI 未能在限定轮次内输出最终报告。", nil
}

func (e *DiagnoseEngine) initSessionAccessor(ctx context.Context, req *DiagnoseRequest) error {
	if req.InstanceRef == nil {
		return nil
	}
	accessor, err := e.registry.CreateAccessor(req.Plugin, ctx, req.InstanceRef)
	if err != nil {
		return fmt.Errorf("create accessor for %s::%s: %w", req.Plugin, req.Target, err)
	}
	req.Session.Accessor = accessor
	return nil
}

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
	e.mu.Lock()
	e.stopped = true
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

// forwardReport sends the diagnosis report to FlashDuty as a new Event
// with the same AlertKey but a fresh EventTime and Description.
func (e *DiagnoseEngine) forwardReport(req *DiagnoseRequest, report string) {
	original := req.Events[0]
	desc := FormatReportForFlashDuty(req.Session.Record, report)

	event := &types.Event{
		EventTime:   time.Now().Unix(),
		EventStatus: types.EventStatusInfo,
		AlertKey:    original.AlertKey,
		Labels:      original.Labels,
		TitleRule:   original.TitleRule,
		Description: desc,
	}

	if flashduty.Forward(event) {
		logger.Logger.Infow("diagnose report forwarded",
			"alert_key", event.AlertKey, "plugin", req.Plugin, "target", req.Target)
	} else {
		logger.Logger.Warnw("diagnose report forward failed",
			"alert_key", event.AlertKey, "plugin", req.Plugin, "target", req.Target)
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
