package diagnose

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/diagnose/aiclient"
	clogger "github.com/cprobe/catpaw/logger"
	"go.uber.org/zap"
)

func initTestConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	config.Config = &config.ConfigType{
		StateDir: dir,
	}
	if clogger.Logger == nil {
		l, _ := zap.NewDevelopment()
		clogger.Logger = l.Sugar()
	}
}

func TestDiagnoseEndToEnd(t *testing.T) {
	initTestConfig(t)

	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)

		var req aiclient.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)

		var resp aiclient.ChatResponse
		if n == 1 {
			resp = aiclient.ChatResponse{
				ID: "test-1",
				Choices: []aiclient.Choice{{
					Index: 0,
					Message: aiclient.Message{
						Role: "assistant",
						ToolCalls: []aiclient.ToolCall{{
							ID:   "tc-1",
							Type: "function",
							Function: aiclient.FunctionCall{
								Name:      "test_local_tool",
								Arguments: `{}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: aiclient.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
			}
		} else {
			resp = aiclient.ChatResponse{
				ID: "test-2",
				Choices: []aiclient.Choice{{
					Index: 0,
					Message: aiclient.Message{
						Role:    "assistant",
						Content: "## 诊断摘要\nRedis 内存使用正常。\n## 根因分析\n无异常。\n## 建议操作\n无需操作。",
					},
					FinishReason: "stop",
				}},
				Usage: aiclient.Usage{PromptTokens: 200, CompletionTokens: 50, TotalTokens: 250},
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	registry := NewToolRegistry()
	registry.RegisterCategory("redis", "redis", "Redis diagnostic tools", ToolScopeRemote)
	registry.Register("redis", DiagnoseTool{
		Name:        "test_local_tool",
		Description: "A test tool",
		Scope:       ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			return "used_memory:1234567\nmaxmemory:0", nil
		},
	})

	aiCfg := config.AIConfig{
		Enabled:                true,
		BaseURL:                srv.URL,
		APIKey:                 "test-key",
		Model:                  "test-model",
		MaxTokens:              4000,
		MaxRounds:              8,
		RequestTimeout:         config.Duration(30 * time.Second),
		MaxRetries:             1,
		RetryBackoff:           config.Duration(100 * time.Millisecond),
		MaxConcurrentDiagnoses: 3,
		ToolTimeout:            config.Duration(5 * time.Second),
		AggregateWindow:        config.Duration(5 * time.Second),
		DiagnoseRetention:      config.Duration(7 * 24 * time.Hour),
		DiagnoseMaxCount:       1000,
	}

	engine := NewDiagnoseEngine(registry, aiCfg)

	req := &DiagnoseRequest{
		Plugin: "redis",
		Target: "10.0.0.1:6379",
		Checks: []CheckSnapshot{{
			Check:         "redis::used_memory",
			Status:        "Warning",
			CurrentValue:  "1.2GB",
			ThresholdDesc: "Warning ≥ 1GB, Critical ≥ 2GB",
			Description:   "redis used memory 1.2GB >= warning threshold 1GB",
		}},
		Timeout:  60 * time.Second,
		Cooldown: 10 * time.Minute,
	}

	record := engine.RunDiagnose(req)

	if record.Status != "success" {
		t.Fatalf("expected success, got %s (error: %s)", record.Status, record.Error)
	}
	if !strings.Contains(record.Report, "诊断摘要") {
		t.Fatalf("report missing expected content: %s", record.Report)
	}
	if record.AI.TotalRounds != 2 {
		t.Fatalf("expected 2 rounds, got %d", record.AI.TotalRounds)
	}
	if len(record.Rounds) != 1 {
		t.Fatalf("expected 1 round record, got %d", len(record.Rounds))
	}
	if len(record.Rounds[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(record.Rounds[0].ToolCalls))
	}
	tc := record.Rounds[0].ToolCalls[0]
	if tc.Name != "test_local_tool" {
		t.Fatalf("expected tool name test_local_tool, got %s", tc.Name)
	}
	if !strings.Contains(tc.Result, "used_memory:1234567") {
		t.Fatalf("tool result missing expected content: %s", tc.Result)
	}

	// Verify record was saved to disk
	recordPath := record.FilePath()
	if _, err := os.Stat(recordPath); os.IsNotExist(err) {
		t.Fatalf("record file not written: %s", recordPath)
	}

	// Verify cooldown is active
	if !engine.state.IsCooldownActive("redis", "10.0.0.1:6379") {
		t.Fatal("expected cooldown to be active after diagnosis")
	}

	// Verify token usage was tracked
	if engine.state.TotalTokens() == 0 {
		t.Fatal("expected token usage to be tracked")
	}
}

func TestDiagnoseMetaTools(t *testing.T) {
	initTestConfig(t)

	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		var resp aiclient.ChatResponse
		switch n {
		case 1:
			resp = aiclient.ChatResponse{
				ID: "test-1",
				Choices: []aiclient.Choice{{
					Message: aiclient.Message{
						Role: "assistant",
						ToolCalls: []aiclient.ToolCall{{
							ID: "tc-1", Type: "function",
							Function: aiclient.FunctionCall{Name: "list_tool_categories", Arguments: "{}"},
						}},
					},
				}},
				Usage: aiclient.Usage{TotalTokens: 50},
			}
		case 2:
			resp = aiclient.ChatResponse{
				ID: "test-2",
				Choices: []aiclient.Choice{{
					Message: aiclient.Message{
						Role: "assistant",
						ToolCalls: []aiclient.ToolCall{{
							ID: "tc-2", Type: "function",
							Function: aiclient.FunctionCall{Name: "list_tools", Arguments: `{"category":"disk"}`},
						}},
					},
				}},
				Usage: aiclient.Usage{TotalTokens: 100},
			}
		default:
			resp = aiclient.ChatResponse{
				ID: "test-3",
				Choices: []aiclient.Choice{{
					Message: aiclient.Message{Role: "assistant", Content: "诊断完成。"},
				}},
				Usage: aiclient.Usage{TotalTokens: 150},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	registry := NewToolRegistry()
	registry.RegisterCategory("disk", "disk", "Disk diagnostic tools", ToolScopeLocal)
	registry.Register("disk", DiagnoseTool{
		Name:        "disk_usage",
		Description: "Show disk usage",
		Scope:       ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			return "/dev/sda1: 80% used", nil
		},
	})

	engine := NewDiagnoseEngine(registry, config.AIConfig{
		Enabled:                true,
		BaseURL:                srv.URL,
		APIKey:                 "test",
		Model:                  "test",
		MaxTokens:              4000,
		MaxRounds:              8,
		RequestTimeout:         config.Duration(30 * time.Second),
		MaxRetries:             0,
		MaxConcurrentDiagnoses: 3,
		ToolTimeout:            config.Duration(5 * time.Second),
	})

	req := &DiagnoseRequest{
		Plugin: "redis",
		Target: "localhost:6379",
		Checks: []CheckSnapshot{{
			Check: "redis::connectivity", Status: "Critical",
			Description: "redis ping failed",
		}},
		Timeout:  30 * time.Second,
		Cooldown: time.Minute,
	}

	record := engine.RunDiagnose(req)

	if record.Status != "success" {
		t.Fatalf("expected success, got %s (error: %s)", record.Status, record.Error)
	}
	if atomic.LoadInt32(&callCount) != 3 {
		t.Fatalf("expected 3 AI calls, got %d", callCount)
	}
}

func TestDiagnoseShutdown(t *testing.T) {
	initTestConfig(t)

	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked // block until test signals
		resp := aiclient.ChatResponse{
			Choices: []aiclient.Choice{{
				Message: aiclient.Message{Role: "assistant", Content: "done"},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	registry := NewToolRegistry()
	engine := NewDiagnoseEngine(registry, config.AIConfig{
		Enabled:                true,
		BaseURL:                srv.URL,
		APIKey:                 "test",
		Model:                  "test",
		MaxRounds:              2,
		RequestTimeout:         config.Duration(10 * time.Second),
		MaxConcurrentDiagnoses: 3,
		ToolTimeout:            config.Duration(5 * time.Second),
	})

	req := &DiagnoseRequest{
		Plugin:  "redis",
		Target:  "10.0.0.1:6379",
		Checks:  []CheckSnapshot{{Check: "test", Status: "Warning"}},
		Timeout: 10 * time.Second,
	}

	var record *DiagnoseRecord
	done := make(chan struct{})
	go func() {
		record = engine.RunDiagnose(req)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	engine.Shutdown()
	close(blocked)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunDiagnose did not complete after Shutdown")
	}

	if record.Status == "running" {
		t.Fatal("record should not remain in 'running' status after shutdown")
	}
}

func TestTruncateOutput(t *testing.T) {
	short := "hello world"
	if TruncateOutput(short) != short {
		t.Fatal("should not truncate short output")
	}

	long := strings.Repeat("x", maxToolOutputBytes+100)
	result := TruncateOutput(long)
	if len(result) > maxToolOutputBytes+30 {
		t.Fatalf("truncated output too long: %d", len(result))
	}
	if !strings.HasSuffix(result, "...[output truncated]") {
		t.Fatal("truncated output missing suffix")
	}
}

func TestPromptSingleCheck(t *testing.T) {
	req := &DiagnoseRequest{
		Plugin: "redis",
		Target: "10.0.0.1:6379",
		Checks: []CheckSnapshot{{
			Check:         "redis::used_memory",
			Status:        "Warning",
			CurrentValue:  "1.5GB",
			ThresholdDesc: "Warning ≥ 1GB, Critical ≥ 2GB",
			Description:   "memory high",
		}},
	}
	prompt := buildSystemPrompt(req, "- redis_info: show info\n", "", "myhost", true, "zh")
	if !strings.Contains(prompt, "redis::used_memory") {
		t.Fatal("prompt should contain check name")
	}
	if !strings.Contains(prompt, "远端主机") {
		t.Fatal("prompt should mention remote target")
	}
	if strings.Contains(prompt, "个异常检查项") {
		t.Fatal("single-check prompt should not use multi-check template")
	}
}

func TestPromptMultipleChecks(t *testing.T) {
	req := &DiagnoseRequest{
		Plugin: "redis",
		Target: "10.0.0.1:6379",
		Checks: []CheckSnapshot{
			{Check: "redis::used_memory", Status: "Warning", Description: "mem high"},
			{Check: "redis::connected_clients", Status: "Critical", Description: "too many"},
		},
	}
	prompt := buildSystemPrompt(req, "", "", "myhost", false, "zh")
	if !strings.Contains(prompt, "2 个异常检查项") {
		t.Fatal("multi-check prompt should mention count")
	}
	if !strings.Contains(prompt, "共同根因") {
		t.Fatal("multi-check prompt should mention common root cause")
	}
}

func TestPromptInspectMode(t *testing.T) {
	req := &DiagnoseRequest{
		Mode:   ModeInspect,
		Plugin: "redis",
		Target: "10.0.0.1:6379",
	}
	prompt := buildInspectPrompt(req, "- redis_info: show info\n", "", "myhost", true, "zh")
	if !strings.Contains(prompt, "主动健康巡检") {
		t.Fatal("inspect prompt should mention health inspection")
	}
	if !strings.Contains(prompt, "[OK]") {
		t.Fatal("inspect prompt should contain status markers")
	}
	if strings.Contains(prompt, "告警详情") {
		t.Fatal("inspect prompt should not contain alert details")
	}
}

func TestPromptLanguage(t *testing.T) {
	req := &DiagnoseRequest{
		Plugin: "redis",
		Target: "10.0.0.1:6379",
		Checks: []CheckSnapshot{{Check: "redis::mem", Status: "Warning"}},
	}

	zhPrompt := buildSystemPrompt(req, "", "", "myhost", false, "zh")
	if strings.Contains(zhPrompt, "You MUST respond in") {
		t.Fatal("zh prompt should not contain language override")
	}

	enPrompt := buildSystemPrompt(req, "", "", "myhost", false, "en")
	if !strings.Contains(enPrompt, "You MUST respond in en") {
		t.Fatal("en prompt should contain language instruction")
	}

	jaPrompt := buildInspectPrompt(req, "", "", "myhost", false, "ja")
	if !strings.Contains(jaPrompt, "You MUST respond in ja") {
		t.Fatal("ja prompt should contain language instruction")
	}
}

func TestRecordSaveAndLoad(t *testing.T) {
	initTestConfig(t)

	record := &DiagnoseRecord{
		ID:        "test_record_123",
		Status:    "success",
		CreatedAt: time.Now(),
		Alert: AlertRecord{
			Plugin: "redis",
			Target: "10.0.0.1:6379",
		},
		Report: "test report",
	}

	if err := record.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	path := record.FilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	var loaded DiagnoseRecord
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if loaded.ID != record.ID || loaded.Status != "success" || loaded.Report != "test report" {
		t.Fatalf("loaded record mismatch: %+v", loaded)
	}
}

func TestStateCooldownAndTokens(t *testing.T) {
	initTestConfig(t)

	state := NewDiagnoseState()

	state.AddTokens(100, 50)
	if state.TotalTokens() != 150 {
		t.Fatalf("expected 150 tokens, got %d", state.TotalTokens())
	}

	state.UpdateCooldown("redis", "10.0.0.1:6379", 5*time.Minute)
	if !state.IsCooldownActive("redis", "10.0.0.1:6379") {
		t.Fatal("cooldown should be active")
	}
	if state.IsCooldownActive("redis", "10.0.0.2:6379") {
		t.Fatal("cooldown should not be active for different target")
	}

	if !state.IsDailyLimitReached(100) {
		t.Fatal("daily limit should be reached at 100")
	}
	if state.IsDailyLimitReached(200) {
		t.Fatal("daily limit should not be reached at 200")
	}

	// Persistence round-trip
	state.Save()
	state2 := NewDiagnoseState()
	state2.Load()
	if state2.TotalTokens() != 150 {
		t.Fatalf("loaded tokens: expected 150, got %d", state2.TotalTokens())
	}
	if !state2.IsCooldownActive("redis", "10.0.0.1:6379") {
		t.Fatal("loaded cooldown should still be active")
	}
}

func TestParseArgs(t *testing.T) {
	m := ParseArgs(`{"name":"redis_info","section":"memory"}`)
	if m["name"] != "redis_info" || m["section"] != "memory" {
		t.Fatalf("unexpected: %v", m)
	}

	m2 := ParseArgs(`{"count": 10, "verbose": true}`)
	if m2["count"] != "10" || m2["verbose"] != "true" {
		t.Fatalf("numeric/bool coercion failed: %v", m2)
	}

	m3 := ParseArgs("")
	if len(m3) != 0 {
		t.Fatalf("empty should return empty map: %v", m3)
	}

	m4 := ParseArgs("not-json")
	if m4["_raw"] != "not-json" {
		t.Fatalf("invalid json should fallback: %v", m4)
	}

	// Nested object in tool_args should be re-serialized as JSON, not Go map format
	m5 := ParseArgs(`{"name":"process_detail","tool_args":{"pid":3846}}`)
	if m5["name"] != "process_detail" {
		t.Fatalf("nested object: name mismatch: %v", m5)
	}
	toolArgs := ParseToolArgs(m5["tool_args"])
	if toolArgs["pid"] != "3846" {
		t.Fatalf("nested object: tool_args pid should be '3846', got %q (raw tool_args=%q)", toolArgs["pid"], m5["tool_args"])
	}

	// ParseToolArgs should handle mixed-type values directly
	m6 := ParseToolArgs(`{"pid": 3846}`)
	if m6["pid"] != "3846" {
		t.Fatalf("ParseToolArgs numeric: pid should be '3846', got %q", m6["pid"])
	}

	// null value should produce empty string so "required" checks fire
	m7 := ParseArgs(`{"pid": null}`)
	if m7["pid"] != "" {
		t.Fatalf("null should become empty string, got %q", m7["pid"])
	}

	// Large float64 should use decimal notation, not scientific notation
	m8 := ParseArgs(`{"big": 100000000000}`)
	if m8["big"] != "100000000000" {
		t.Fatalf("large number should be decimal, got %q", m8["big"])
	}

	// ParseToolArgs empty returns nil
	if ParseToolArgs("") != nil {
		t.Fatal("ParseToolArgs empty should return nil")
	}
}

func TestBuildToolSet(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterCategory("redis", "redis", "Redis tools", ToolScopeRemote)
	registry.Register("redis", DiagnoseTool{
		Name: "redis_info", Description: "Redis INFO",
		Scope: ToolScopeRemote,
	})
	registry.Register("redis", DiagnoseTool{
		Name: "redis_slowlog", Description: "Redis SLOWLOG",
		Scope: ToolScopeRemote,
		Parameters: []ToolParam{{Name: "count", Type: "int", Description: "entries", Required: false}},
	})

	req := &DiagnoseRequest{Plugin: "redis", Target: "10.0.0.1:6379"}
	aiTools, directTools := buildToolSet(registry, req)

	if len(directTools) != 2 {
		t.Fatalf("expected 2 direct tools, got %d", len(directTools))
	}

	// 2 direct + 2 meta tools = 4
	if len(aiTools) != 4 {
		t.Fatalf("expected 4 AI tools, got %d", len(aiTools))
	}

	names := make(map[string]bool)
	for _, at := range aiTools {
		names[at.Function.Name] = true
	}
	for _, expected := range []string{"redis_info", "redis_slowlog", "list_tools", "call_tool"} {
		if !names[expected] {
			t.Fatalf("missing expected tool: %s", expected)
		}
	}
}

func TestDiagnoseRecordDir(t *testing.T) {
	initTestConfig(t)

	req := &DiagnoseRequest{
		Plugin: "redis",
		Target: "10.0.0.1:6379",
		Checks: []CheckSnapshot{{Check: "test"}},
	}
	rec := NewDiagnoseRecord(req)
	if rec.Status != "running" {
		t.Fatalf("expected running, got %s", rec.Status)
	}
	if rec.Alert.Plugin != "redis" {
		t.Fatalf("alert plugin mismatch")
	}

	expected := filepath.Join(config.Config.StateDir, "diagnoses", rec.ID+".json")
	if rec.FilePath() != expected {
		t.Fatalf("filepath mismatch: %s != %s", rec.FilePath(), expected)
	}
}

func TestSanitizeTarget(t *testing.T) {
	tests := []struct{ in, want string }{
		{"10.0.0.1:6379", "10_0_0_1_6379"},
		{"localhost", "localhost"},
		{"[::1]:6379", "___1__6379"},
	}
	for _, tt := range tests {
		got := sanitizeTarget(tt.in)
		if got != tt.want {
			t.Errorf("sanitizeTarget(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Ensure linter is happy about unused imports
var _ = fmt.Sprint
