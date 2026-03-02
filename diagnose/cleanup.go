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

	var files []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e)
		}
	}

	if len(files) == 0 {
		return
	}

	sort.Slice(files, func(i, j int) bool {
		fi, _ := files[i].Info()
		fj, _ := files[j].Info()
		if fi == nil || fj == nil {
			return files[i].Name() < files[j].Name()
		}
		return fi.ModTime().After(fj.ModTime())
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

		if !shouldRemove && retention > 0 {
			if info, err := f.Info(); err == nil && now.Sub(info.ModTime()) > retention {
				shouldRemove = true
			}
		}

		if shouldRemove {
			path := filepath.Join(dir, f.Name())
			if err := os.Remove(path); err != nil {
				logger.Logger.Warnw("cleanup: remove file failed", "file", f.Name(), "error", err)
			} else {
				removed++
			}
		}
	}

	if removed > 0 {
		logger.Logger.Infow("diagnose records cleaned up", "removed", removed, "remaining", len(files)-removed)
	}
}
