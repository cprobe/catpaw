package filecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

const pluginName = "filecheck"

const defaultMaxFileSize = 50 * config.MB

type MtimeCheck struct {
	Severity  string          `toml:"severity"`
	TimeSpan  config.Duration `toml:"time_span"`
	TitleRule string          `toml:"title_rule"`
}

type ChecksumCheck struct {
	Severity    string      `toml:"severity"`
	TitleRule   string      `toml:"title_rule"`
	MaxFileSize config.Size `toml:"max_file_size"`
}

type ExistenceCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	Targets   []string       `toml:"targets"`
	Mtime     MtimeCheck     `toml:"mtime"`
	Checksum  ChecksumCheck  `toml:"checksum"`
	Existence ExistenceCheck `toml:"existence"`

	prevChecksums sync.Map
}

type FileCheckPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *FileCheckPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &FileCheckPlugin{}
	})
}

func (ins *Instance) Init() error {
	if len(ins.Targets) == 0 {
		return fmt.Errorf("targets is empty")
	}

	if ins.Mtime.Severity == "" && ins.Checksum.Severity == "" && ins.Existence.Severity == "" {
		return fmt.Errorf("at least one check dimension (mtime/checksum/existence) must be configured")
	}

	if ins.Mtime.Severity != "" && ins.Mtime.TimeSpan == 0 {
		ins.Mtime.TimeSpan = config.Duration(3 * time.Minute)
	}

	if ins.Checksum.Severity != "" && ins.Checksum.MaxFileSize == 0 {
		ins.Checksum.MaxFileSize = defaultMaxFileSize
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	resolvedFiles, missingTargets := ins.resolveTargets()

	if ins.Existence.Severity != "" {
		ins.checkExistence(q, missingTargets)
	}

	if ins.Mtime.Severity != "" {
		ins.checkMtime(q, resolvedFiles)
	}

	if ins.Checksum.Severity != "" {
		ins.checkChecksum(q, resolvedFiles)
	}
}

// resolveTargets expands all targets to concrete file paths.
// Supports exact paths, glob patterns, and directories (recursive walk).
// Returns deduplicated resolved files and targets that could not be found.
func (ins *Instance) resolveTargets() (files []string, missing []string) {
	seen := make(map[string]struct{})

	for _, target := range ins.Targets {
		if strings.ContainsAny(target, "*?[") {
			matches, err := filepath.Glob(target)
			if err != nil {
				logger.Logger.Errorw("glob error", "target", target, "error", err)
				continue
			}
			if len(matches) == 0 {
				missing = append(missing, target)
				continue
			}
			for _, m := range matches {
				collectPath(m, &files, seen)
			}
		} else {
			fi, err := os.Stat(target)
			if err != nil {
				missing = append(missing, target)
				continue
			}
			if fi.IsDir() {
				walkDir(target, &files, seen)
			} else {
				addUnique(target, &files, seen)
			}
		}
	}
	return
}

func collectPath(path string, files *[]string, seen map[string]struct{}) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if fi.IsDir() {
		walkDir(path, files, seen)
	} else {
		addUnique(path, files, seen)
	}
}

func walkDir(dir string, files *[]string, seen map[string]struct{}) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		addUnique(path, files, seen)
		return nil
	})
}

func addUnique(path string, files *[]string, seen map[string]struct{}) {
	if _, ok := seen[path]; !ok {
		seen[path] = struct{}{}
		*files = append(*files, path)
	}
}

func (ins *Instance) targetLabel() string {
	return strings.Join(ins.Targets, ",")
}

// --- Existence dimension ---

func (ins *Instance) checkExistence(q *safe.Queue[*types.Event], missing []string) {
	target := ins.targetLabel()
	tr := ins.Existence.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	if len(missing) == 0 {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "filecheck::existence",
			"target": target,
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusOk).
			SetDescription("all targets exist"))
		return
	}

	var desc strings.Builder
	desc.WriteString("[MD]\n")
	desc.WriteString("Missing targets:\n\n")
	for _, m := range missing {
		desc.WriteString(fmt.Sprintf("- `%s`\n", m))
	}

	q.PushFront(types.BuildEvent(map[string]string{
		"check":  "filecheck::existence",
		"target": target,
	}).SetTitleRule(tr).
		SetEventStatus(ins.Existence.Severity).
		SetDescription(desc.String()))
}

// --- Mtime dimension ---

func (ins *Instance) checkMtime(q *safe.Queue[*types.Event], files []string) {
	now := time.Now()
	changed := make(map[string]time.Time)

	for _, fp := range files {
		fi, err := os.Stat(fp)
		if err != nil {
			continue
		}
		if now.Sub(fi.ModTime()) < time.Duration(ins.Mtime.TimeSpan) {
			changed[fp] = fi.ModTime()
		}
	}

	target := ins.targetLabel()
	tr := ins.Mtime.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	if len(changed) == 0 {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "filecheck::mtime",
			"target": target,
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusOk).
			SetDescription("files not changed"))
		return
	}

	var desc strings.Builder
	desc.WriteString("[MD]\n")
	desc.WriteString(fmt.Sprintf("files changed within %s\n\n", time.Duration(ins.Mtime.TimeSpan)))
	desc.WriteString("| File | MTime |\n")
	desc.WriteString("| :-- | --: |\n")
	for fp, mtime := range changed {
		desc.WriteString(fmt.Sprintf("| %s | %s |\n", fp, mtime.Format("2006-01-02 15:04:05")))
	}

	q.PushFront(types.BuildEvent(map[string]string{
		"check":  "filecheck::mtime",
		"target": target,
	}).SetTitleRule(tr).
		SetEventStatus(ins.Mtime.Severity).
		SetDescription(desc.String()))
}

// --- Checksum dimension ---

func (ins *Instance) checkChecksum(q *safe.Queue[*types.Event], files []string) {
	var changedFiles [][2]string

	for _, fp := range files {
		fi, err := os.Stat(fp)
		if err != nil {
			continue
		}
		if fi.Size() > int64(ins.Checksum.MaxFileSize) {
			logger.Logger.Warnw("file too large for checksum, skipping",
				"file", fp, "size", fi.Size(), "max", ins.Checksum.MaxFileSize.String())
			continue
		}

		hash, err := sha256File(fp)
		if err != nil {
			logger.Logger.Errorw("checksum error", "file", fp, "error", err)
			continue
		}

		prev, loaded := ins.prevChecksums.Load(fp)
		ins.prevChecksums.Store(fp, hash)
		if loaded && prev.(string) != hash {
			changedFiles = append(changedFiles, [2]string{fp, hash})
		}
	}

	target := ins.targetLabel()
	tr := ins.Checksum.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	if len(changedFiles) == 0 {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "filecheck::checksum",
			"target": target,
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusOk).
			SetDescription("file checksums unchanged"))
		return
	}

	var desc strings.Builder
	desc.WriteString("[MD]\n")
	desc.WriteString("File checksums changed:\n\n")
	desc.WriteString("| File | New SHA256 |\n")
	desc.WriteString("| :-- | :-- |\n")
	for _, cf := range changedFiles {
		desc.WriteString(fmt.Sprintf("| %s | `%s...` |\n", cf[0], cf[1][:16]))
	}

	q.PushFront(types.BuildEvent(map[string]string{
		"check":  "filecheck::checksum",
		"target": target,
	}).SetTitleRule(tr).
		SetEventStatus(ins.Checksum.Severity).
		SetDescription(desc.String()))
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
