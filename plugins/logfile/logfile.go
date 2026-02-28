package logfile

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/conv"
	"github.com/cprobe/catpaw/pkg/filter"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

const (
	pluginName      = "logfile"
	fingerprintSize = 256
)

type fileState struct {
	Offset      int64  `json:"offset"`
	Inode       uint64 `json:"inode"`
	Fingerprint string `json:"fingerprint"`
}

type MatchCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	Targets         []string        `toml:"targets"`
	InitialPosition string          `toml:"initial_position"`
	FilterInclude   []string        `toml:"filter_include"`
	FilterExclude   []string        `toml:"filter_exclude"`
	MaxLines        int             `toml:"max_lines"`
	MaxReadBytes    config.Size     `toml:"max_read_bytes"`
	MaxLineLength   int             `toml:"max_line_length"`
	MaxTargets      int             `toml:"max_targets"`
	ContextBefore   int             `toml:"context_before"`
	ContextAfter    int             `toml:"context_after"`
	Encoding        string          `toml:"encoding"`
	StateFile       string          `toml:"state_file"`
	GatherTimeout   config.Duration `toml:"gather_timeout"`
	Match           MatchCheck      `toml:"match"`

	mu              sync.Mutex
	includeFilter   filter.Filter
	excludeFilter   filter.Filter
	fileStates      map[string]*fileState
	explicitTargets map[string]bool
	enc             encoding.Encoding
	stateDirty      bool
}

type LogfilePlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *LogfilePlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &LogfilePlugin{}
	})
}

func (ins *Instance) Init() error {
	if len(ins.Targets) == 0 {
		return fmt.Errorf("targets must not be empty")
	}

	if len(ins.FilterInclude) == 0 {
		return fmt.Errorf("filter_include must not be empty (logfile monitoring without match rules is meaningless)")
	}

	incFilter, err := filter.Compile(ins.FilterInclude)
	if err != nil {
		return fmt.Errorf("failed to compile filter_include: %v", err)
	}
	ins.includeFilter = incFilter

	if len(ins.FilterExclude) > 0 {
		excFilter, err := filter.Compile(ins.FilterExclude)
		if err != nil {
			return fmt.Errorf("failed to compile filter_exclude: %v", err)
		}
		ins.excludeFilter = excFilter
	}

	if ins.Match.Severity == "" {
		ins.Match.Severity = types.EventStatusWarning
	}
	if !types.EventStatusValid(ins.Match.Severity) {
		return fmt.Errorf("match.severity %q is invalid (use Critical, Warning, Info, Ok)", ins.Match.Severity)
	}

	if ins.InitialPosition == "" {
		ins.InitialPosition = "end"
	}
	if ins.InitialPosition != "end" && ins.InitialPosition != "beginning" {
		return fmt.Errorf("initial_position must be \"end\" or \"beginning\" (got %q)", ins.InitialPosition)
	}

	if ins.MaxReadBytes <= 0 {
		ins.MaxReadBytes = config.MB
	}
	if ins.MaxLines <= 0 {
		ins.MaxLines = 10
	}
	if ins.MaxLineLength <= 0 {
		ins.MaxLineLength = 8192
	}
	if ins.MaxTargets <= 0 {
		ins.MaxTargets = 100
	}
	if ins.GatherTimeout <= 0 {
		ins.GatherTimeout = config.Duration(10 * time.Second)
	}

	if ins.ContextBefore < 0 {
		return fmt.Errorf("context_before must be >= 0 (got %d)", ins.ContextBefore)
	}
	if ins.ContextAfter < 0 {
		return fmt.Errorf("context_after must be >= 0 (got %d)", ins.ContextAfter)
	}
	if ins.ContextBefore > 10 {
		return fmt.Errorf("context_before must be <= 10 (got %d)", ins.ContextBefore)
	}
	if ins.ContextAfter > 10 {
		return fmt.Errorf("context_after must be <= 10 (got %d)", ins.ContextAfter)
	}

	enc, err := lookupEncoding(ins.Encoding)
	if err != nil {
		return err
	}
	ins.enc = enc

	if ins.StateFile == "" {
		h := fnv.New32a()
		for _, t := range ins.Targets {
			h.Write([]byte(t))
			h.Write([]byte{0})
		}
		ins.StateFile = filepath.Join(config.Config.StateDir, "p.logfile",
			fmt.Sprintf(".logfile_state_%08x.json", h.Sum32()))
	}

	ins.explicitTargets = make(map[string]bool)
	for _, t := range ins.Targets {
		if !filter.HasMeta(t) {
			ins.explicitTargets[t] = true
		}
	}

	ins.fileStates = make(map[string]*fileState)
	ins.loadState()

	return nil
}

func (ins *Instance) Drop() {
	ins.mu.Lock()
	defer ins.mu.Unlock()
	ins.saveState()
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	ins.mu.Lock()
	defer ins.mu.Unlock()

	deadline := time.Now().Add(time.Duration(ins.GatherTimeout))

	resolvedFiles := ins.resolveTargets()

	if len(resolvedFiles) > ins.MaxTargets {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "logfile::match",
			"target": "glob",
		}).SetTitleRule("[check]").
			SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("targets resolved to %d files, exceeding max_targets(%d), only monitoring the first %d",
				len(resolvedFiles), ins.MaxTargets, ins.MaxTargets)))
		resolvedFiles = resolvedFiles[:ins.MaxTargets]
	}

	resolvedSet := make(map[string]bool, len(resolvedFiles))
	for _, f := range resolvedFiles {
		resolvedSet[f] = true
	}

	// Clean stale glob-sourced fileStates
	for path := range ins.fileStates {
		if !resolvedSet[path] && !ins.explicitTargets[path] {
			delete(ins.fileStates, path)
			ins.stateDirty = true
		}
	}

	// Handle explicit targets that disappeared.
	// Keep fileState (don't delete) so that:
	//   1. Critical is emitted every Gather until the file reappears
	//   2. When the file reappears, rotation detection uses the old state
	//      to correctly reset offset=0 and read from beginning
	for path := range ins.explicitTargets {
		if resolvedSet[path] {
			continue
		}
		_, err := os.Stat(path)
		if err == nil {
			continue
		}
		if os.IsNotExist(err) {
			if _, had := ins.fileStates[path]; had {
				q.PushFront(ins.buildEvent(path).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("file %s disappeared (was previously monitored)", path)))
			} else {
				logger.Logger.Debugw("explicit target not found (may not exist yet)", "file", path)
			}
		} else {
			q.PushFront(ins.buildEvent(path).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("file %s inaccessible: %v", path, err)))
		}
	}

	for _, filePath := range resolvedFiles {
		if time.Now().After(deadline) {
			q.PushFront(types.BuildEvent(map[string]string{
				"check":  "logfile::match",
				"target": "gather_timeout",
			}).SetTitleRule("[check]").
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("gather_timeout (%s) exceeded, skipped remaining files", time.Duration(ins.GatherTimeout))))
			break
		}

		ins.processFile(q, filePath)
	}

	if ins.stateDirty {
		ins.saveState()
		ins.stateDirty = false
	}
}

func (ins *Instance) processFile(q *safe.Queue[*types.Event], filePath string) {
	info, err := os.Stat(filePath)
	if err != nil {
		q.PushFront(ins.buildEvent(filePath).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to stat %s: %v", filePath, err)))
		return
	}

	currentSize := info.Size()
	currentInode := getInode(info)
	currentFP := readFingerprint(filePath)

	state, exists := ins.fileStates[filePath]
	if !exists {
		state = &fileState{}
		ins.fileStates[filePath] = state

		if ins.InitialPosition == "end" {
			state.Offset = currentSize
		}
		state.Inode = currentInode
		state.Fingerprint = currentFP
		ins.stateDirty = true

		if ins.InitialPosition == "end" {
			q.PushFront(ins.buildEvent(filePath).SetDescription("everything is ok"))
			return
		}
	} else {
		rotated := false
		if currentInode != state.Inode && currentInode != 0 && state.Inode != 0 {
			rotated = true
		} else if !fingerprintsMatch(currentFP, state.Fingerprint) {
			rotated = true
		} else if currentSize < state.Offset {
			rotated = true
		}

		if rotated {
			logger.Logger.Infow("log rotation detected",
				"file", filePath,
				"old_inode", state.Inode, "new_inode", currentInode,
				"old_offset", state.Offset, "new_size", currentSize,
				"fingerprint_changed", currentFP != state.Fingerprint)
			state.Offset = 0
			state.Inode = currentInode
			state.Fingerprint = currentFP
			ins.stateDirty = true
		} else if len(currentFP) > len(state.Fingerprint) {
			state.Fingerprint = currentFP
			ins.stateDirty = true
		}
	}

	if currentSize == state.Offset {
		q.PushFront(ins.buildEvent(filePath).SetDescription("everything is ok"))
		return
	}

	bytesToRead := currentSize - state.Offset
	if bytesToRead > int64(ins.MaxReadBytes) {
		bytesToRead = int64(ins.MaxReadBytes)
	}

	f, err := os.Open(filePath)
	if err != nil {
		q.PushFront(ins.buildEvent(filePath).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to open %s: %v", filePath, err)))
		return
	}
	defer f.Close()

	if _, err := f.Seek(state.Offset, io.SeekStart); err != nil {
		q.PushFront(ins.buildEvent(filePath).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to seek %s to offset %d: %v", filePath, state.Offset, err)))
		return
	}

	rawBuf, err := io.ReadAll(io.LimitReader(f, bytesToRead))
	if err != nil {
		q.PushFront(ins.buildEvent(filePath).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read %s: %v", filePath, err)))
		return
	}

	if len(rawBuf) == 0 {
		q.PushFront(ins.buildEvent(filePath).SetDescription("everything is ok"))
		return
	}

	// Only process complete lines (ending with \n).
	// Offset tracks raw file bytes — correct regardless of encoding.
	lastNL := bytes.LastIndexByte(rawBuf, '\n')
	if lastNL < 0 {
		if int64(len(rawBuf)) < int64(ins.MaxReadBytes) {
			// Incomplete line at EOF — wait for more data
			q.PushFront(ins.buildEvent(filePath).SetDescription("everything is ok"))
			return
		}
		// Entire max_read_bytes block without \n — force advance to prevent stuck offset
		logger.Logger.Warnw("no newline found in max_read_bytes block, force advancing offset",
			"file", filePath, "bytes", len(rawBuf))
		lastNL = len(rawBuf) - 1
	}

	completeRaw := rawBuf[:lastNL+1]
	state.Offset += int64(len(completeRaw))
	ins.stateDirty = true
	bytesRead := int64(len(completeRaw))

	var text string
	if ins.enc != nil {
		decoded, _, err := transform.String(ins.enc.NewDecoder(), string(completeRaw))
		if err != nil {
			logger.Logger.Warnw("encoding decode error, falling back to raw bytes",
				"file", filePath, "encoding", ins.Encoding, "error", err)
		}
		if decoded == "" && len(completeRaw) > 0 {
			text = string(completeRaw)
		} else {
			text = decoded
		}
	} else {
		text = string(completeRaw)
	}

	rawLines := strings.Split(text, "\n")
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" {
		rawLines = rawLines[:len(rawLines)-1]
	}

	var lines []string
	for _, line := range rawLines {
		line = strings.TrimRight(line, "\r")
		line = strings.ToValidUTF8(line, "\uFFFD")
		lines = append(lines, line)
	}

	var matchedIndices []int
	for i, line := range lines {
		if ins.includeFilter.Match(line) {
			if ins.excludeFilter != nil && ins.excludeFilter.Match(line) {
				continue
			}
			matchedIndices = append(matchedIndices, i)
		}
	}

	// Truncate after matching — truncation is a display optimization, not a filtering decision.
	for i, line := range lines {
		if len(line) > ins.MaxLineLength {
			lines[i] = truncateUTF8(line, ins.MaxLineLength)
		}
	}

	event := ins.buildEvent(filePath)

	if len(matchedIndices) == 0 {
		q.PushFront(event.SetDescription("everything is ok"))
		return
	}

	event.Labels[types.AttrPrefix+"matched_count"] = fmt.Sprintf("%d", len(matchedIndices))
	event.Labels[types.AttrPrefix+"bytes_read"] = conv.HumanBytes(uint64(bytesRead))

	desc := ins.buildDescription(lines, matchedIndices)
	q.PushFront(event.SetEventStatus(ins.Match.Severity).SetDescription(desc))
}

func (ins *Instance) buildDescription(lines []string, matchedIndices []int) string {
	totalMatched := len(matchedIndices)
	hasContext := ins.ContextBefore > 0 || ins.ContextAfter > 0

	showIndices := matchedIndices
	if len(showIndices) > ins.MaxLines {
		showIndices = showIndices[:ins.MaxLines]
	}

	var sb strings.Builder

	if hasContext {
		sb.WriteString(fmt.Sprintf("matched %d lines (context: -%d/+%d):\n",
			totalMatched, ins.ContextBefore, ins.ContextAfter))

		type lineRange struct{ start, end int }
		var ranges []lineRange
		for _, idx := range showIndices {
			start := idx - ins.ContextBefore
			if start < 0 {
				start = 0
			}
			end := idx + ins.ContextAfter
			if end >= len(lines) {
				end = len(lines) - 1
			}
			ranges = append(ranges, lineRange{start, end})
		}

		// Merge overlapping/adjacent ranges
		merged := []lineRange{ranges[0]}
		for i := 1; i < len(ranges); i++ {
			last := &merged[len(merged)-1]
			if ranges[i].start <= last.end+1 {
				if ranges[i].end > last.end {
					last.end = ranges[i].end
				}
			} else {
				merged = append(merged, ranges[i])
			}
		}

		matchedSet := make(map[int]bool, len(showIndices))
		for _, idx := range showIndices {
			matchedSet[idx] = true
		}

		for ri, r := range merged {
			if ri > 0 {
				sb.WriteString("  ...\n")
			}
			for i := r.start; i <= r.end; i++ {
				if matchedSet[i] {
					sb.WriteString("> ")
				} else {
					sb.WriteString("  ")
				}
				sb.WriteString(lines[i])
				sb.WriteByte('\n')
			}
		}
	} else {
		sb.WriteString(fmt.Sprintf("matched %d lines:\n", totalMatched))
		for _, idx := range showIndices {
			sb.WriteString(lines[idx])
			sb.WriteByte('\n')
		}
	}

	if totalMatched > ins.MaxLines {
		sb.WriteString(fmt.Sprintf("... and %d more lines\n", totalMatched-ins.MaxLines))
	}

	return strings.TrimRight(sb.String(), "\n")
}

func (ins *Instance) buildEvent(filePath string) *types.Event {
	tr := ins.Match.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}
	return types.BuildEvent(map[string]string{
		"check":  "logfile::match",
		"target": filePath,
	}).SetTitleRule(tr)
}

func (ins *Instance) resolveTargets() []string {
	var files []string
	seen := make(map[string]bool)
	for _, target := range ins.Targets {
		if !filter.HasMeta(target) {
			if !seen[target] {
				if info, err := os.Stat(target); err == nil && !info.IsDir() {
					files = append(files, target)
				}
				seen[target] = true
			}
			continue
		}
		matches, err := filepath.Glob(target)
		if err != nil {
			logger.Logger.Warnw("glob pattern error", "pattern", target, "error", err)
			continue
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || info.IsDir() {
				continue
			}
			if !seen[m] {
				files = append(files, m)
				seen[m] = true
			}
		}
	}
	return files
}

func readFingerprint(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, fingerprintSize)
	n, err := f.Read(buf)
	if n == 0 {
		return ""
	}
	if err != nil && err != io.EOF {
		return ""
	}
	return hex.EncodeToString(buf[:n])
}

// fingerprintsMatch compares current and stored fingerprints with directional
// prefix matching. For append-only files, the fingerprint can only grow
// (file starts small, later captures more bytes up to fingerprintSize).
//
// - current longer than stored: prefix match (file grew, normal)
// - current equal to stored: exact match
// - current shorter than stored: return false (file shrunk or was replaced)
func fingerprintsMatch(current, stored string) bool {
	if current == "" || stored == "" {
		return current == stored
	}
	if len(current) < len(stored) {
		return false
	}
	return current[:len(stored)] == stored
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes] + "..."
}

// --- State persistence ---

func (ins *Instance) loadState() {
	data, err := os.ReadFile(ins.StateFile)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Logger.Warnw("failed to load state file, starting fresh", "file", ins.StateFile, "error", err)
		}
		return
	}

	loaded := make(map[string]*fileState)
	if err := json.Unmarshal(data, &loaded); err != nil {
		logger.Logger.Warnw("failed to parse state file, starting fresh", "file", ins.StateFile, "error", err)
		return
	}

	ins.fileStates = loaded
	logger.Logger.Infow("loaded state file", "file", ins.StateFile, "entries", len(loaded))
}

func (ins *Instance) saveState() {
	data, err := json.MarshalIndent(ins.fileStates, "", "  ")
	if err != nil {
		logger.Logger.Errorw("failed to marshal state", "error", err)
		return
	}

	dir := filepath.Dir(ins.StateFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		logger.Logger.Errorw("failed to create state dir", "dir", dir, "error", err)
		return
	}

	tmpFile := ins.StateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		logger.Logger.Errorw("failed to write temp state file", "file", tmpFile, "error", err)
		return
	}

	if err := os.Rename(tmpFile, ins.StateFile); err != nil {
		logger.Logger.Errorw("failed to rename state file", "from", tmpFile, "to", ins.StateFile, "error", err)
		_ = os.Remove(tmpFile)
	}
}

// --- Encoding lookup ---

func lookupEncoding(name string) (encoding.Encoding, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "utf-8", "utf8":
		return nil, nil
	case "gbk", "gb2312":
		return simplifiedchinese.GBK, nil
	case "gb18030":
		return simplifiedchinese.GB18030, nil
	case "big5":
		return traditionalchinese.Big5, nil
	case "shift_jis", "shift-jis", "sjis":
		return japanese.ShiftJIS, nil
	case "euc-jp", "eucjp":
		return japanese.EUCJP, nil
	case "euc-kr", "euckr":
		return korean.EUCKR, nil
	case "latin1", "iso-8859-1":
		return charmap.ISO8859_1, nil
	case "windows-1252", "cp1252":
		return charmap.Windows1252, nil
	default:
		return nil, fmt.Errorf("unsupported encoding: %q (supported: utf-8, gbk, gb18030, big5, shift_jis, euc-jp, euc-kr, latin1, windows-1252)", name)
	}
}
