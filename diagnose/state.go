package diagnose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
)

// DiagnoseState tracks daily token usage and per-target cooldowns.
// Persisted to state.d/diagnose_state.json for restart resilience.
type DiagnoseState struct {
	mu sync.Mutex

	Date         string         `json:"date"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	Cooldowns    map[string]int64 `json:"cooldowns"` // "plugin::target" → unix timestamp
}

// NewDiagnoseState creates a fresh state for today.
func NewDiagnoseState() *DiagnoseState {
	return &DiagnoseState{
		Date:      today(),
		Cooldowns: make(map[string]int64),
	}
}

func (s *DiagnoseState) AddTokens(input, output int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetIfNewDay()
	s.InputTokens += input
	s.OutputTokens += output
}

func (s *DiagnoseState) TotalTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetIfNewDay()
	return s.InputTokens + s.OutputTokens
}

func (s *DiagnoseState) UpdateCooldown(plugin, target string, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := plugin + "::" + target
	s.Cooldowns[key] = time.Now().Add(duration).Unix()
}

func (s *DiagnoseState) IsCooldownActive(plugin, target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := plugin + "::" + target
	expires, ok := s.Cooldowns[key]
	if !ok {
		return false
	}
	if time.Now().Unix() >= expires {
		delete(s.Cooldowns, key)
		return false
	}
	return true
}

func (s *DiagnoseState) resetIfNewDay() {
	d := today()
	if s.Date != d {
		s.Date = d
		s.InputTokens = 0
		s.OutputTokens = 0
	}
}

// Load reads state from state.d/diagnose_state.json. Missing file is not an error.
func (s *DiagnoseState) Load() {
	p := statePath()
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := json.Unmarshal(data, s); err != nil {
		logger.Logger.Warnw("failed to parse diagnose state, resetting", "path", p, "error", err)
		s.Date = today()
		s.InputTokens = 0
		s.OutputTokens = 0
		s.Cooldowns = make(map[string]int64)
	}
	s.resetIfNewDay()
	s.cleanExpiredCooldowns()
}

// Save atomically writes state to disk.
func (s *DiagnoseState) Save() {
	s.mu.Lock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil {
		logger.Logger.Warnw("failed to marshal diagnose state", "error", err)
		return
	}

	p := statePath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0755); err != nil {
		logger.Logger.Warnw("failed to create state dir", "error", err)
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		logger.Logger.Warnw("failed to write state file", "error", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		logger.Logger.Warnw("failed to rename state file", "error", err)
	}
}

func (s *DiagnoseState) cleanExpiredCooldowns() {
	now := time.Now().Unix()
	for k, v := range s.Cooldowns {
		if now >= v {
			delete(s.Cooldowns, k)
		}
	}
}

func statePath() string {
	return filepath.Join(config.Config.StateDir, "diagnose_state.json")
}

func today() string {
	return time.Now().Format("2006-01-02")
}

// IsDailyLimitReached checks if the daily token budget has been exhausted.
func (s *DiagnoseState) IsDailyLimitReached(limit int) bool {
	if limit <= 0 {
		return false
	}
	return s.TotalTokens() >= limit
}

// FormatUsage returns a human-readable summary of daily token usage.
func (s *DiagnoseState) FormatUsage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("date=%s input=%d output=%d total=%d",
		s.Date, s.InputTokens, s.OutputTokens, s.InputTokens+s.OutputTokens)
}
