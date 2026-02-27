package filecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

const (
	defaultMaxFileSize = 50 * config.MB
	maxDisplayFiles    = 20
	maxWalkFiles       = 10000
)

var errWalkLimitReached = errors.New("file limit reached")

type MtimeCheck struct {
	Severity  string          `toml:"severity"`
	Mode      string          `toml:"mode"`
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

	if ins.Mtime.Severity != "" {
		if ins.Mtime.TimeSpan == 0 {
			ins.Mtime.TimeSpan = config.Duration(3 * time.Minute)
		}
		if ins.Mtime.Mode == "" {
			ins.Mtime.Mode = "changed"
		}
		if ins.Mtime.Mode != "changed" && ins.Mtime.Mode != "stale" {
			return fmt.Errorf("mtime mode must be 'changed' or 'stale', got '%s'", ins.Mtime.Mode)
		}
	}

	if ins.Checksum.Severity != "" && ins.Checksum.MaxFileSize == 0 {
		ins.Checksum.MaxFileSize = defaultMaxFileSize
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	resolvedFiles, missingTargets, accessErrors := ins.resolveTargets()

	if ins.Existence.Severity != "" {
		ins.checkExistence(q, missingTargets, accessErrors)
	}

	if ins.Mtime.Severity != "" {
		ins.checkMtime(q, resolvedFiles)
	}

	if ins.Checksum.Severity != "" {
		ins.checkChecksum(q, resolvedFiles)
	}
}

// resolveTargets expands all targets to concrete file paths.
// Tries exact path first (handles Windows paths containing '['),
// then falls back to glob. Distinguishes missing targets from access errors.
func (ins *Instance) resolveTargets() (files []string, missing []string, accessErrors []string) {
	seen := make(map[string]struct{})

	for _, target := range ins.Targets {
		fi, statErr := os.Stat(target)
		if statErr == nil {
			if fi.IsDir() {
				walkDir(target, &files, seen)
			} else {
				addUnique(target, &files, seen)
			}
			continue
		}

		if !os.IsNotExist(statErr) {
			accessErrors = append(accessErrors, fmt.Sprintf("%s: %v", target, statErr))
			continue
		}

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
			missing = append(missing, target)
		}
	}

	if len(files) > maxWalkFiles {
		logger.Logger.Warnw("resolved file count exceeds limit, truncating",
			"total", len(files), "limit", maxWalkFiles)
		files = files[:maxWalkFiles]
	}

	return
}

func collectPath(path string, files *[]string, seen map[string]struct{}) {
	if len(*files) >= maxWalkFiles {
		return
	}
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
		if len(*files) >= maxWalkFiles {
			return errWalkLimitReached
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

func (ins *Instance) checkExistence(q *safe.Queue[*types.Event], missing []string, accessErrors []string) {
	target := ins.targetLabel()
	tr := ins.Existence.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	if len(missing) == 0 && len(accessErrors) == 0 {
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
	if len(missing) > 0 {
		desc.WriteString("Missing targets:\n\n")
		for _, m := range missing {
			desc.WriteString(fmt.Sprintf("- `%s`\n", m))
		}
	}
	appendAccessErrors(&desc, accessErrors)

	q.PushFront(types.BuildEvent(map[string]string{
		"check":  "filecheck::existence",
		"target": target,
	}).SetTitleRule(tr).
		SetEventStatus(ins.Existence.Severity).
		SetDescription(desc.String()))
}

// --- Mtime dimension ---

type mtimeEntry struct {
	path  string
	mtime time.Time
}

func (ins *Instance) checkMtime(q *safe.Queue[*types.Event], files []string) {
	now := time.Now()
	timeSpan := time.Duration(ins.Mtime.TimeSpan)
	var matched []mtimeEntry
	var errs []string

	for _, fp := range files {
		fi, err := os.Stat(fp)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", fp, err))
			continue
		}
		age := now.Sub(fi.ModTime())

		switch ins.Mtime.Mode {
		case "stale":
			if age > timeSpan {
				matched = append(matched, mtimeEntry{fp, fi.ModTime()})
			}
		default:
			if age < timeSpan {
				matched = append(matched, mtimeEntry{fp, fi.ModTime()})
			}
		}
	}

	target := ins.targetLabel()
	tr := ins.Mtime.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	if len(matched) == 0 {
		var okDesc strings.Builder
		if ins.Mtime.Mode == "stale" {
			okDesc.WriteString(fmt.Sprintf("all files updated within %s", timeSpan))
		} else {
			okDesc.WriteString("files not changed")
		}
		appendAccessErrors(&okDesc, errs)
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "filecheck::mtime",
			"target": target,
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusOk).
			SetDescription(okDesc.String()))
		return
	}

	var desc strings.Builder
	desc.WriteString("[MD]\n")
	if ins.Mtime.Mode == "stale" {
		desc.WriteString(fmt.Sprintf("%d files not updated for more than %s\n\n", len(matched), timeSpan))
	} else {
		desc.WriteString(fmt.Sprintf("%d files changed within %s\n\n", len(matched), timeSpan))
	}
	desc.WriteString("| File | MTime |\n")
	desc.WriteString("| :-- | --: |\n")
	display := len(matched)
	if display > maxDisplayFiles {
		display = maxDisplayFiles
	}
	for i := 0; i < display; i++ {
		desc.WriteString(fmt.Sprintf("| %s | %s |\n",
			matched[i].path, matched[i].mtime.Format("2006-01-02 15:04:05")))
	}
	if len(matched) > maxDisplayFiles {
		desc.WriteString(fmt.Sprintf("\n... and %d more files\n", len(matched)-maxDisplayFiles))
	}
	appendAccessErrors(&desc, errs)

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
	var errs []string

	for _, fp := range files {
		fi, err := os.Stat(fp)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", fp, err))
			continue
		}
		if fi.Size() > int64(ins.Checksum.MaxFileSize) {
			logger.Logger.Debugw("file too large for checksum, skipping",
				"file", fp, "size", fi.Size(), "max", ins.Checksum.MaxFileSize.String())
			continue
		}

		hash, err := sha256File(fp)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", fp, err))
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
		var okDesc strings.Builder
		okDesc.WriteString("file checksums unchanged")
		appendAccessErrors(&okDesc, errs)
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "filecheck::checksum",
			"target": target,
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusOk).
			SetDescription(okDesc.String()))
		return
	}

	var desc strings.Builder
	desc.WriteString("[MD]\n")
	desc.WriteString(fmt.Sprintf("%d file checksums changed:\n\n", len(changedFiles)))
	desc.WriteString("| File | New SHA256 |\n")
	desc.WriteString("| :-- | :-- |\n")
	display := len(changedFiles)
	if display > maxDisplayFiles {
		display = maxDisplayFiles
	}
	for i := 0; i < display; i++ {
		desc.WriteString(fmt.Sprintf("| %s | `%s...` |\n", changedFiles[i][0], changedFiles[i][1][:16]))
	}
	if len(changedFiles) > maxDisplayFiles {
		desc.WriteString(fmt.Sprintf("\n... and %d more files\n", len(changedFiles)-maxDisplayFiles))
	}
	appendAccessErrors(&desc, errs)

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

// --- helpers ---

func appendAccessErrors(desc *strings.Builder, errs []string) {
	if len(errs) == 0 {
		return
	}
	desc.WriteString(fmt.Sprintf("\n\n**%d files could not be accessed:**\n\n", len(errs)))
	display := len(errs)
	if display > 5 {
		display = 5
	}
	for i := 0; i < display; i++ {
		desc.WriteString(fmt.Sprintf("- `%s`\n", errs[i]))
	}
	if len(errs) > 5 {
		desc.WriteString(fmt.Sprintf("- ... and %d more\n", len(errs)-5))
	}
}
