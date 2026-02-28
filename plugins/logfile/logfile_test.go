package logfile

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cprobe/catpaw/config"
	clogger "github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
	"go.uber.org/zap"
)

func initTestConfig(t *testing.T) {
	t.Helper()
	if config.Config == nil {
		tmpDir := t.TempDir()
		config.Config = &config.ConfigType{
			ConfigDir: tmpDir,
			StateDir:  tmpDir,
		}
	}
	if clogger.Logger == nil {
		l, _ := zap.NewDevelopment()
		clogger.Logger = l.Sugar()
	}
}

func newTestInstance(t *testing.T) *Instance {
	t.Helper()
	initTestConfig(t)
	return &Instance{
		Targets:         []string{"/tmp/test.log"},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "end",
		StateFile:       filepath.Join(t.TempDir(), "state.json"),
	}
}

// --- Init tests ---

func TestInitValidation(t *testing.T) {
	initTestConfig(t)

	tests := []struct {
		name    string
		ins     *Instance
		wantErr string
	}{
		{
			name:    "empty targets",
			ins:     &Instance{FilterInclude: []string{"*ERROR*"}, StateFile: "/tmp/s.json"},
			wantErr: "targets must not be empty",
		},
		{
			name:    "empty filter_include",
			ins:     &Instance{Targets: []string{"/tmp/test.log"}, StateFile: "/tmp/s.json"},
			wantErr: "filter_include must not be empty",
		},
		{
			name: "invalid severity",
			ins: &Instance{
				Targets:       []string{"/tmp/test.log"},
				FilterInclude: []string{"*ERROR*"},
				Match:         MatchCheck{Severity: "BadSeverity"},
				StateFile:     "/tmp/s.json",
			},
			wantErr: "match.severity",
		},
		{
			name: "invalid initial_position",
			ins: &Instance{
				Targets:         []string{"/tmp/test.log"},
				FilterInclude:   []string{"*ERROR*"},
				InitialPosition: "middle",
				StateFile:       "/tmp/s.json",
			},
			wantErr: "initial_position",
		},
		{
			name: "invalid encoding",
			ins: &Instance{
				Targets:       []string{"/tmp/test.log"},
				FilterInclude: []string{"*ERROR*"},
				Encoding:      "bogus-encoding",
				StateFile:     "/tmp/s.json",
			},
			wantErr: "unsupported encoding",
		},
		{
			name: "negative context_before",
			ins: &Instance{
				Targets:       []string{"/tmp/test.log"},
				FilterInclude: []string{"*ERROR*"},
				ContextBefore: -1,
				StateFile:     "/tmp/s.json",
			},
			wantErr: "context_before must be >= 0",
		},
		{
			name: "context_before too large",
			ins: &Instance{
				Targets:       []string{"/tmp/test.log"},
				FilterInclude: []string{"*ERROR*"},
				ContextBefore: 11,
				StateFile:     "/tmp/s.json",
			},
			wantErr: "context_before must be <= 10",
		},
		{
			name: "valid config with defaults",
			ins: &Instance{
				Targets:       []string{"/tmp/test.log"},
				FilterInclude: []string{"*ERROR*"},
				StateFile:     "/tmp/s.json",
			},
			wantErr: "",
		},
		{
			name: "valid config with GBK encoding",
			ins: &Instance{
				Targets:       []string{"/tmp/test.log"},
				FilterInclude: []string{"*ERROR*"},
				Encoding:      "gbk",
				StateFile:     "/tmp/s.json",
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.ins.Init()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
				}
			}
		})
	}
}

func TestInitDefaults(t *testing.T) {
	ins := newTestInstance(t)
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	if ins.MaxReadBytes != config.MB {
		t.Errorf("MaxReadBytes = %d, want %d", ins.MaxReadBytes, config.MB)
	}
	if ins.MaxLines != 10 {
		t.Errorf("MaxLines = %d, want 10", ins.MaxLines)
	}
	if ins.MaxLineLength != 8192 {
		t.Errorf("MaxLineLength = %d, want 8192", ins.MaxLineLength)
	}
	if ins.MaxTargets != 100 {
		t.Errorf("MaxTargets = %d, want 100", ins.MaxTargets)
	}
	if ins.Match.Severity != types.EventStatusWarning {
		t.Errorf("Match.Severity = %q, want %q", ins.Match.Severity, types.EventStatusWarning)
	}
	if ins.InitialPosition != "end" {
		t.Errorf("InitialPosition = %q, want \"end\"", ins.InitialPosition)
	}
}

func TestInitNegativeValuesFallbackToDefaults(t *testing.T) {
	initTestConfig(t)
	ins := &Instance{
		Targets:       []string{"/tmp/test.log"},
		FilterInclude: []string{"*ERROR*"},
		MaxLines:      -5,
		MaxLineLength: -1,
		MaxTargets:    -10,
		MaxReadBytes:  -1024,
		StateFile:     filepath.Join(t.TempDir(), "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("negative values should not cause Init error: %v", err)
	}
	if ins.MaxLines != 10 {
		t.Errorf("MaxLines = %d, want default 10", ins.MaxLines)
	}
	if ins.MaxLineLength != 8192 {
		t.Errorf("MaxLineLength = %d, want default 8192", ins.MaxLineLength)
	}
	if ins.MaxTargets != 100 {
		t.Errorf("MaxTargets = %d, want default 100", ins.MaxTargets)
	}
	if ins.MaxReadBytes != config.MB {
		t.Errorf("MaxReadBytes = %d, want default %d", ins.MaxReadBytes, config.MB)
	}
}

// --- Gather tests ---

func TestGatherNewFileFromEnd(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")
	os.WriteFile(logFile, []byte("line1\nline2 ERROR something\nline3\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "end",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("first gather from end should be Ok, got %s: %s", event.EventStatus, event.Description)
	}
}

func TestGatherNewFileFromBeginning(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")
	os.WriteFile(logFile, []byte("line1\nERROR something bad\nline3\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "beginning",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning, got %s: %s", event.EventStatus, event.Description)
	}
	if !strings.Contains(event.Description, "ERROR something bad") {
		t.Errorf("description should contain matched line, got: %s", event.Description)
	}
}

func TestGatherIncrementalRead(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")
	os.WriteFile(logFile, []byte("initial content\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "end",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	q.RemoveAll()

	// Append new content with a matching line
	f, _ := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("new line\nERROR disk full\nanother line\n")
	f.Close()

	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning, got %s", event.EventStatus)
	}
	if !strings.Contains(event.Description, "ERROR disk full") {
		t.Errorf("description should contain matched line, got: %s", event.Description)
	}
}

func TestGatherNoNewContent(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")
	os.WriteFile(logFile, []byte("initial\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "end",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	q.RemoveAll()

	// Gather again without any change
	ins.Gather(q)
	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok (no new content), got %s", event.EventStatus)
	}
}

func TestGatherFilterExclude(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")
	os.WriteFile(logFile, []byte("ERROR expected test error\nERROR real problem\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		FilterExclude:   []string{"*expected*"},
		InitialPosition: "beginning",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning, got %s", event.EventStatus)
	}
	if strings.Contains(event.Description, "expected test error") {
		t.Errorf("excluded line should not appear in description: %s", event.Description)
	}
	if !strings.Contains(event.Description, "real problem") {
		t.Errorf("non-excluded match should appear: %s", event.Description)
	}
}

// --- Fingerprint tests ---

func TestReadFingerprint(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	content := "Hello World fingerprint test data\n"
	os.WriteFile(logFile, []byte(content), 0644)

	fp := readFingerprint(logFile)
	if fp == "" {
		t.Fatal("fingerprint should not be empty")
	}

	expected := hex.EncodeToString([]byte(content))
	if fp != expected {
		t.Errorf("fingerprint mismatch: got %s, want %s", fp, expected)
	}
}

func TestFingerprintDetectsInodeReuse(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	os.WriteFile(logFile, []byte("original content line 1\noriginal ERROR first\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "beginning",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	q.RemoveAll()

	state := ins.fileStates[logFile]
	if state == nil {
		t.Fatal("fileState should exist after first gather")
	}
	oldOffset := state.Offset
	if oldOffset == 0 {
		t.Fatal("offset should be > 0 after reading from beginning")
	}
	oldFP := state.Fingerprint

	// Simulate inode reuse: replace file content entirely
	os.WriteFile(logFile, []byte("completely different content\nERROR new file error\n"), 0644)

	newFP := readFingerprint(logFile)
	if newFP == oldFP {
		t.Skip("fingerprint same after rewrite (unlikely), skip")
	}

	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event after inode reuse")
	}
	event := *ep
	if !strings.Contains(event.Description, "ERROR new file error") {
		t.Errorf("should read new file from beginning, got: %s", event.Description)
	}
}

func TestFingerprintsMatchDirectional(t *testing.T) {
	tests := []struct {
		name    string
		current string
		stored  string
		want    bool
	}{
		{"both empty", "", "", true},
		{"current empty stored not", "", "abc", false},
		{"stored empty current not", "abc", "", false},
		{"exact match", "aabbcc", "aabbcc", true},
		{"current longer prefix match", "aabbccdd", "aabbcc", true},
		{"current longer no match", "aabbccdd", "zzyyxx", false},
		{"current shorter - always false", "aabb", "aabbcc", false},
		{"current shorter even if prefix matches", "aabb", "aabbccdd", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fingerprintsMatch(tt.current, tt.stored)
			if got != tt.want {
				t.Errorf("fingerprintsMatch(%q, %q) = %v, want %v", tt.current, tt.stored, got, tt.want)
			}
		})
	}
}

// --- Context lines tests ---

func TestGatherWithContextLines(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	var lines []string
	lines = append(lines, "2026-01-01 INFO Starting app")
	lines = append(lines, "2026-01-01 INFO Loading config")
	lines = append(lines, "2026-01-01 ERROR OutOfMemoryError")
	lines = append(lines, "2026-01-01 WARN Cleanup started")
	lines = append(lines, "2026-01-01 INFO App recovered")
	content := strings.Join(lines, "\n") + "\n"
	os.WriteFile(logFile, []byte(content), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "beginning",
		ContextBefore:   2,
		ContextAfter:    1,
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep

	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning, got %s", event.EventStatus)
	}

	// Should contain context markers
	if !strings.Contains(event.Description, "> ") {
		t.Errorf("description should contain '> ' marker for matched lines, got: %s", event.Description)
	}
	if !strings.Contains(event.Description, "context: -") {
		t.Errorf("description should mention context, got: %s", event.Description)
	}

	// Before context: INFO Starting app, INFO Loading config
	if !strings.Contains(event.Description, "Starting app") {
		t.Errorf("description should contain before-context line: %s", event.Description)
	}
	if !strings.Contains(event.Description, "Loading config") {
		t.Errorf("description should contain before-context line: %s", event.Description)
	}

	// Matched line
	if !strings.Contains(event.Description, "OutOfMemoryError") {
		t.Errorf("description should contain matched line: %s", event.Description)
	}

	// After context: WARN Cleanup started
	if !strings.Contains(event.Description, "Cleanup started") {
		t.Errorf("description should contain after-context line: %s", event.Description)
	}
}

func TestBuildDescriptionNoContext(t *testing.T) {
	ins := &Instance{MaxLines: 5}

	lines := []string{"line0", "ERROR one", "line2", "ERROR two", "line4"}
	indices := []int{1, 3}

	desc := ins.buildDescription(lines, indices)
	if !strings.Contains(desc, "matched 2 lines:") {
		t.Errorf("should have header, got: %s", desc)
	}
	if !strings.Contains(desc, "ERROR one") {
		t.Errorf("should contain match: %s", desc)
	}
	if !strings.Contains(desc, "ERROR two") {
		t.Errorf("should contain match: %s", desc)
	}
}

func TestBuildDescriptionWithOverflow(t *testing.T) {
	ins := &Instance{MaxLines: 2}

	lines := []string{"ERROR a", "ERROR b", "ERROR c", "ERROR d"}
	indices := []int{0, 1, 2, 3}

	desc := ins.buildDescription(lines, indices)
	if !strings.Contains(desc, "matched 4 lines:") {
		t.Errorf("should show total match count: %s", desc)
	}
	if !strings.Contains(desc, "... and 2 more lines") {
		t.Errorf("should indicate truncation: %s", desc)
	}
}

// --- State persistence tests ---

func TestStatePersistence(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")
	stateFile := filepath.Join(tmpDir, "state.json")

	os.WriteFile(logFile, []byte("initial\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "end",
		StateFile:       stateFile,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	// State file should exist now
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("state file should exist: %v", err)
	}

	var loaded map[string]*fileState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("state file should be valid JSON: %v", err)
	}

	state, ok := loaded[logFile]
	if !ok {
		t.Fatalf("state should contain entry for %s", logFile)
	}
	if state.Offset == 0 {
		t.Error("offset should be > 0")
	}
	if state.Fingerprint == "" {
		t.Error("fingerprint should not be empty")
	}

	// Simulate restart: create new instance, load from state file
	ins2 := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "end",
		StateFile:       stateFile,
	}
	if err := ins2.Init(); err != nil {
		t.Fatal(err)
	}

	state2, ok := ins2.fileStates[logFile]
	if !ok {
		t.Fatalf("reloaded instance should have state for %s", logFile)
	}
	if state2.Offset != state.Offset {
		t.Errorf("reloaded offset = %d, want %d", state2.Offset, state.Offset)
	}
}

// --- File disappeared tests ---

func TestExplicitFileDisappeared(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	os.WriteFile(logFile, []byte("content\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "end",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	q.RemoveAll()

	os.Remove(logFile)

	// First Gather after disappearance: should emit Critical
	ins.Gather(q)
	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event for disappeared file")
	}
	event := *ep
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical for disappeared file, got %s", event.EventStatus)
	}
	if !strings.Contains(event.Description, "disappeared") {
		t.Errorf("description should mention 'disappeared', got: %s", event.Description)
	}
	q.RemoveAll()

	// Second Gather: should STILL emit Critical (not go silent)
	ins.Gather(q)
	ep = q.PopBack()
	if ep == nil {
		t.Fatal("expected Critical on second gather after disappearance")
	}
	event = *ep
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("second gather should still be Critical, got %s", event.EventStatus)
	}
	q.RemoveAll()

	// File reappears with new content
	os.WriteFile(logFile, []byte("ERROR new content after reappear\n"), 0644)

	ins.Gather(q)
	ep = q.PopBack()
	if ep == nil {
		t.Fatal("expected event after file reappearance")
	}
	event = *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("reappeared file should be read from beginning, got %s: %s", event.EventStatus, event.Description)
	}
	if !strings.Contains(event.Description, "ERROR new content after reappear") {
		t.Errorf("should contain new file content (read from offset 0), got: %s", event.Description)
	}
}

func TestExplicitFileInaccessible(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()

	logDir := filepath.Join(tmpDir, "logs")
	os.Mkdir(logDir, 0755)
	logFile := filepath.Join(logDir, "app.log")

	os.WriteFile(logFile, []byte("content\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "end",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	q.RemoveAll()

	// Remove execute permission from parent directory.
	// This makes os.Stat(logFile) fail with EACCES (not ENOENT),
	// testing the "inaccessible" code path.
	if err := os.Chmod(logDir, 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(logDir, 0755) })

	ins.Gather(q)
	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event for inaccessible file")
	}
	event := *ep
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical for inaccessible file, got %s", event.EventStatus)
	}
	if !strings.Contains(event.Description, "inaccessible") {
		t.Errorf("description should mention 'inaccessible', got: %s", event.Description)
	}
}

// --- Log rotation tests ---

func TestCopytruncateRotation(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	os.WriteFile(logFile, []byte("line1\nline2\nline3\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "end",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	q.RemoveAll()

	// Simulate copytruncate: truncate to smaller size, write new content
	os.WriteFile(logFile, []byte("ERROR new after truncate\n"), 0644)

	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event after copytruncate")
	}
	event := *ep

	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning, got %s: %s", event.EventStatus, event.Description)
	}
	if !strings.Contains(event.Description, "ERROR new after truncate") {
		t.Errorf("should read from beginning after truncate, got: %s", event.Description)
	}
}

// --- Encoding tests ---

func TestLookupEncoding(t *testing.T) {
	tests := []struct {
		name    string
		wantNil bool
		wantErr bool
	}{
		{"", true, false},
		{"utf-8", true, false},
		{"UTF-8", true, false},
		{"gbk", false, false},
		{"GBK", false, false},
		{"gb2312", false, false},
		{"gb18030", false, false},
		{"big5", false, false},
		{"shift_jis", false, false},
		{"euc-jp", false, false},
		{"euc-kr", false, false},
		{"latin1", false, false},
		{"windows-1252", false, false},
		{"bogus", false, true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("encoding=%q", tt.name), func(t *testing.T) {
			enc, err := lookupEncoding(tt.name)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil && enc != nil {
				t.Error("expected nil encoding for UTF-8")
			}
			if !tt.wantNil && enc == nil {
				t.Error("expected non-nil encoding")
			}
		})
	}
}

// --- Glob tests ---

func TestResolveTargetsGlob(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, "app.log"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "error.log"), []byte("y"), 0644)
	os.Mkdir(filepath.Join(tmpDir, "subdir"), 0755)

	ins := &Instance{
		Targets: []string{filepath.Join(tmpDir, "*.log")},
	}

	files := ins.resolveTargets()
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestResolveTargetsExplicit(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")
	os.WriteFile(logFile, []byte("x"), 0644)

	ins := &Instance{
		Targets: []string{logFile},
	}

	files := ins.resolveTargets()
	if len(files) != 1 || files[0] != logFile {
		t.Errorf("expected [%s], got %v", logFile, files)
	}
}

// --- Long line tests (Bug 2 regression) ---

func TestGatherLongLineDoesNotDropSubsequentLines(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	longLine := strings.Repeat("X", 20000) + "\n"
	content := longLine + "ERROR real problem after long line\n"
	os.WriteFile(logFile, []byte(content), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "beginning",
		MaxLineLength:   8192,
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning (ERROR line should not be lost), got %s: %s", event.EventStatus, event.Description)
	}
	if !strings.Contains(event.Description, "ERROR real problem after long line") {
		t.Errorf("ERROR line after long line should be matched, got: %s", event.Description)
	}
}

func TestGatherMatchBeyondMaxLineLength(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	// ERROR keyword is at byte ~10000, beyond MaxLineLength=8192
	content := strings.Repeat("A", 10000) + "ERROR hidden deep in line\n"
	os.WriteFile(logFile, []byte(content), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "beginning",
		MaxLineLength:   8192,
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("ERROR beyond MaxLineLength should still be matched, got %s: %s", event.EventStatus, event.Description)
	}
	// The displayed description should be truncated but the match should happen
	if !strings.Contains(event.Description, "...") {
		t.Errorf("long line should be truncated in display, got: %s", event.Description)
	}
}

// --- Partial line tests (Bug 4 regression) ---

func TestGatherPartialLineNotProcessed(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	os.WriteFile(logFile, []byte("complete line\nERROR partial no newline"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "beginning",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("partial line without \\n should not be matched, got %s: %s", event.EventStatus, event.Description)
	}

	state := ins.fileStates[logFile]
	if state.Offset != int64(len("complete line\n")) {
		t.Errorf("offset should stop at last complete line, got %d, want %d", state.Offset, len("complete line\n"))
	}

	// Now complete the partial line
	f, _ := os.OpenFile(logFile, os.O_WRONLY, 0644)
	f.Seek(0, 2)
	f.WriteString("\n")
	f.Close()

	q.RemoveAll()
	ins.Gather(q)

	ep = q.PopBack()
	if ep == nil {
		t.Fatal("expected an event after completing the line")
	}
	event = *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("completed ERROR line should now match, got %s: %s", event.EventStatus, event.Description)
	}
}

// --- UTF-8 truncation tests (Bug 5 regression) ---

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxBytes  int
		wantTrunc bool
	}{
		{"ascii no truncation", "hello world", 20, false},
		{"ascii truncation", "hello world", 5, true},
		{"cjk boundary", "abc中文def", 5, true},
		{"cjk mid-char", "abc中文def", 4, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateUTF8(tt.input, tt.maxBytes)
			if tt.wantTrunc && !strings.HasSuffix(result, "...") {
				t.Errorf("truncated string should end with ..., got: %q", result)
			}
			clean := strings.TrimSuffix(result, "...")
			if !utf8.ValidString(clean) {
				t.Errorf("truncated result is not valid UTF-8: %q", result)
			}
		})
	}
}

func TestTruncateUTF8ValidOutput(t *testing.T) {
	input := "Hello 你好世界 こんにちは"
	for maxBytes := 1; maxBytes <= len(input); maxBytes++ {
		result := truncateUTF8(input, maxBytes)
		clean := strings.TrimSuffix(result, "...")
		if !utf8.ValidString(clean) {
			t.Errorf("truncateUTF8(%q, %d) produced invalid UTF-8: %q", input, maxBytes, result)
		}
	}
}

// --- Multi-instance state file isolation (Bug 3 regression) ---

func TestMultiInstanceStateFileIsolation(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()

	logA := filepath.Join(tmpDir, "a.log")
	logB := filepath.Join(tmpDir, "b.log")
	os.WriteFile(logA, []byte("content A\n"), 0644)
	os.WriteFile(logB, []byte("content B\n"), 0644)

	insA := &Instance{
		Targets:       []string{logA},
		FilterInclude: []string{"*ERROR*"},
	}
	insB := &Instance{
		Targets:       []string{logB},
		FilterInclude: []string{"*ERROR*"},
	}

	if err := insA.Init(); err != nil {
		t.Fatal(err)
	}
	if err := insB.Init(); err != nil {
		t.Fatal(err)
	}

	if insA.StateFile == insB.StateFile {
		t.Errorf("different targets should produce different state files: A=%s, B=%s", insA.StateFile, insB.StateFile)
	}
}

// --- GBK encoding Gather test (Bug 1 regression) ---

func TestGatherGBKEncoding(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	gbkError := []byte{0xB4, 0xED, 0xCE, 0xF3} // "错误" in GBK
	var content []byte
	content = append(content, []byte("INFO normal line")...)
	content = append(content, '\n')
	content = append(content, []byte("ERROR ")...)
	content = append(content, gbkError...)
	content = append(content, '\n')

	os.WriteFile(logFile, content, 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "beginning",
		Encoding:        "gbk",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning for GBK ERROR line, got %s: %s", event.EventStatus, event.Description)
	}

	state := ins.fileStates[logFile]
	expectedOffset := int64(len(content))
	if state.Offset != expectedOffset {
		t.Errorf("offset should equal raw file size %d (not decoded size), got %d", expectedOffset, state.Offset)
	}
}

// --- CRLF line ending test ---

func TestGatherCRLFLineEndings(t *testing.T) {
	initTestConfig(t)
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	os.WriteFile(logFile, []byte("line1\r\nERROR bad thing\r\nline3\r\n"), 0644)

	ins := &Instance{
		Targets:         []string{logFile},
		FilterInclude:   []string{"*ERROR*"},
		InitialPosition: "beginning",
		StateFile:       filepath.Join(tmpDir, "state.json"),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning, got %s: %s", event.EventStatus, event.Description)
	}
	if strings.Contains(event.Description, "\r") {
		t.Errorf("\\r should be stripped from output, got: %q", event.Description)
	}
}
