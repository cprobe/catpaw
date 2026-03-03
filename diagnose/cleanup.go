package diagnose

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
)

// CleanupRecords removes diagnosis records exceeding the retention period
// or the maximum count, whichever triggers first.
func CleanupRecords() {
	dir := filepath.Join(config.Config.StateDir, "diagnoses")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		logger.Logger.Warnw("cleanup: read diagnoses dir failed", "error", err)
		return
	}

	type fileEntry struct {
		name    string
		modTime time.Time
	}

	var files []fileEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{name: e.Name(), modTime: info.ModTime()})
	}

	if len(files) == 0 {
		return
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	retention := time.Duration(config.Config.AI.DiagnoseRetention)
	maxCount := config.Config.AI.DiagnoseMaxCount
	now := time.Now()
	var removed int

	for i, f := range files {
		shouldRemove := false

		if maxCount > 0 && i >= maxCount {
			shouldRemove = true
		}

		if !shouldRemove && retention > 0 && now.Sub(f.modTime) > retention {
			shouldRemove = true
		}

		if shouldRemove {
			path := filepath.Join(dir, f.name)
			if err := os.Remove(path); err != nil {
				logger.Logger.Warnw("cleanup: remove file failed", "file", f.name, "error", err)
			} else {
				removed++
			}
		}
	}

	if removed > 0 {
		logger.Logger.Infow("diagnose records cleaned up", "removed", removed, "remaining", len(files)-removed)
	}
}
