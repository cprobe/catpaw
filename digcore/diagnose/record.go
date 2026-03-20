package diagnose

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
)

// NewDiagnoseRecord creates a DiagnoseRecord from a DiagnoseRequest,
// pre-populating the alert context and timestamp.
func NewDiagnoseRecord(req *DiagnoseRequest) *DiagnoseRecord {
	mode := req.Mode
	if mode == "" {
		mode = ModeAlert
	}
	prefix := mode
	return &DiagnoseRecord{
		ID:        fmt.Sprintf("%s_%s_%s_%d_%s", prefix, req.Plugin, sanitizeTarget(req.Target), time.Now().UnixMilli(), randHex4()),
		Mode:      mode,
		Status:    "running",
		CreatedAt: time.Now(),
		Alert: AlertRecord{
			Plugin: req.Plugin,
			Target: req.Target,
			Checks: req.Checks,
		},
	}
}

// Save writes the DiagnoseRecord atomically (temp file + rename) to the diagnoses directory.
func (r *DiagnoseRecord) Save() error {
	dir := filepath.Join(config.Config.StateDir, "diagnoses")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create diagnoses dir: %w", err)
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}

	target := filepath.Join(dir, r.ID+".json")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// FilePath returns the absolute path where this record is (or will be) stored.
func (r *DiagnoseRecord) FilePath() string {
	return filepath.Join(config.Config.StateDir, "diagnoses", r.ID+".json")
}

func sanitizeTarget(target string) string {
	result := make([]byte, 0, len(target))
	for i := 0; i < len(target); i++ {
		c := target[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			result = append(result, c)
		} else {
			result = append(result, '_')
		}
	}
	return string(result)
}

func randHex4() string {
	var b [2]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
