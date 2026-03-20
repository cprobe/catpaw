package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// LoadOrCreateAgentID reads the agent_id from state.d/agent_id. If the file
// does not exist, a new UUIDv4 is generated and persisted.
func LoadOrCreateAgentID() (uuid.UUID, error) {
	if Config == nil {
		return uuid.Nil, fmt.Errorf("agent config is not initialized")
	}
	if Config.StateDir == "" {
		return uuid.Nil, fmt.Errorf("agent state_dir is empty")
	}
	p := filepath.Join(Config.StateDir, "agent_id")

	data, err := os.ReadFile(p)
	if err == nil {
		id, err := uuid.Parse(strings.TrimSpace(string(data)))
		if err == nil {
			return id, nil
		}
	}

	id := uuid.New()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return uuid.Nil, fmt.Errorf("create state dir: %w", err)
	}
	if err := os.WriteFile(p, []byte(id.String()+"\n"), 0o644); err != nil {
		return uuid.Nil, fmt.Errorf("write agent_id: %w", err)
	}
	return id, nil
}
