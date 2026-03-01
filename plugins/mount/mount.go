package mount

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

const pluginName = "mount"

type MountSpec struct {
	Path      string   `toml:"path"`
	FSType    string   `toml:"fstype"`
	Options   []string `toml:"options"`
	Severity  string   `toml:"severity"`
	TitleRule string   `toml:"title_rule"`
}

type FstabCheck struct {
	Enabled        bool     `toml:"enabled"`
	Severity       string   `toml:"severity"`
	ExcludeFSTypes []string `toml:"exclude_fstype"`
	ExcludePaths   []string `toml:"exclude_paths"`
}

type Instance struct {
	config.InternalConfig

	Mounts []MountSpec `toml:"mounts"`
	Fstab  FstabCheck  `toml:"fstab"`
}

type MountPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *MountPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &MountPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("mount plugin only supports linux (current: %s)", runtime.GOOS)
	}

	// 用户没有配置任何检查项时不报错，Gather 时静默跳过

	seen := make(map[string]bool, len(ins.Mounts))
	for i := range ins.Mounts {
		m := &ins.Mounts[i]
		m.Path = strings.TrimSpace(m.Path)
		if m.Path == "" {
			return fmt.Errorf("mounts[%d]: path must not be empty", i)
		}
		if !strings.HasPrefix(m.Path, "/") {
			return fmt.Errorf("mounts[%d]: path must be absolute (start with /), got %q", i, m.Path)
		}
		if len(m.Path) > 1 {
			m.Path = strings.TrimRight(m.Path, "/")
		}
		if seen[m.Path] {
			return fmt.Errorf("mounts[%d]: duplicate mount path %q", i, m.Path)
		}
		seen[m.Path] = true

		m.FSType = strings.TrimSpace(strings.ToLower(m.FSType))

		for j := range m.Options {
			m.Options[j] = strings.TrimSpace(m.Options[j])
			if m.Options[j] == "" {
				return fmt.Errorf("mounts[%d].options[%d]: option must not be empty", i, j)
			}
		}

		if err := normalizeSeverity(&m.Severity); err != nil {
			return fmt.Errorf("mounts[%d] (path=%s): %v", i, m.Path, err)
		}
	}

	if ins.Fstab.Enabled {
		if err := normalizeSeverity(&ins.Fstab.Severity); err != nil {
			return fmt.Errorf("fstab.severity: %v", err)
		}
		if len(ins.Fstab.ExcludeFSTypes) == 0 {
			ins.Fstab.ExcludeFSTypes = []string{"tmpfs", "devtmpfs", "squashfs", "overlay"}
		}
		for i, p := range ins.Fstab.ExcludePaths {
			ins.Fstab.ExcludePaths[i] = strings.TrimSpace(p)
		}
	}

	return nil
}

func normalizeSeverity(s *string) error {
	*s = strings.TrimSpace(*s)
	if *s == "" {
		*s = types.EventStatusWarning
	}
	switch *s {
	case types.EventStatusInfo, types.EventStatusWarning, types.EventStatusCritical:
		return nil
	default:
		return fmt.Errorf("invalid severity %q, must be Info, Warning or Critical", *s)
	}
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	mountMap, err := parseProcMounts()
	if err != nil {
		logger.Logger.Errorw("failed to parse /proc/mounts", "error", err)
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "mount::compliance",
			"target": "mounts",
		}).SetTitleRule("[check]").
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse /proc/mounts: %v", err)))
		return
	}

	for i := range ins.Mounts {
		ins.checkMount(q, &ins.Mounts[i], mountMap)
	}

	if ins.Fstab.Enabled {
		ins.checkFstabMounts(q, mountMap)
	}
}

func (ins *Instance) checkMount(q *safe.Queue[*types.Event], spec *MountSpec, mountMap map[string]mountEntry) {
	tr := spec.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	entry, exists := mountMap[spec.Path]

	if !exists {
		expect := formatExpect(spec)
		q.PushFront(types.BuildEvent(map[string]string{
			"check":                     "mount::compliance",
			"target":                    spec.Path,
			types.AttrPrefix + "actual": "not mounted",
			types.AttrPrefix + "expect": expect,
		}).SetTitleRule(tr).
			SetEventStatus(spec.Severity).
			SetDescription(formatNotMounted(spec)))
		return
	}

	actual := entry.fsType + ", " + entry.rawOptions
	expect := formatExpect(spec)

	if spec.FSType != "" && entry.fsType != spec.FSType {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":                     "mount::compliance",
			"target":                    spec.Path,
			types.AttrPrefix + "actual": actual,
			types.AttrPrefix + "expect": expect,
		}).SetTitleRule(tr).
			SetEventStatus(spec.Severity).
			SetDescription(fmt.Sprintf("%s is mounted as %s, expected %s", spec.Path, entry.fsType, spec.FSType)))
		return
	}

	if len(spec.Options) > 0 {
		var missing []string
		for _, opt := range spec.Options {
			if !entry.options[opt] {
				missing = append(missing, opt)
			}
		}
		if len(missing) > 0 {
			q.PushFront(types.BuildEvent(map[string]string{
				"check":                     "mount::compliance",
				"target":                    spec.Path,
				types.AttrPrefix + "actual": actual,
				types.AttrPrefix + "expect": expect,
			}).SetTitleRule(tr).
				SetEventStatus(spec.Severity).
				SetDescription(fmt.Sprintf("%s is missing mount options: %s (actual: %s)",
					spec.Path, strings.Join(missing, ", "), entry.rawOptions)))
			return
		}
	}

	desc := fmt.Sprintf("%s is mounted as %s", spec.Path, entry.fsType)
	if len(spec.Options) > 0 {
		desc += " with expected options (" + strings.Join(spec.Options, ", ") + ")"
	}
	q.PushFront(types.BuildEvent(map[string]string{
		"check":                     "mount::compliance",
		"target":                    spec.Path,
		types.AttrPrefix + "actual": actual,
		types.AttrPrefix + "expect": expect,
	}).SetTitleRule(tr).
		SetEventStatus(types.EventStatusOk).
		SetDescription(desc))
}

func formatNotMounted(spec *MountSpec) string {
	if spec.FSType != "" {
		return fmt.Sprintf("%s is not mounted (expected %s)", spec.Path, spec.FSType)
	}
	return fmt.Sprintf("%s is not mounted", spec.Path)
}

func formatExpect(spec *MountSpec) string {
	var parts []string
	if spec.FSType != "" {
		parts = append(parts, spec.FSType)
	}
	if len(spec.Options) > 0 {
		parts = append(parts, strings.Join(spec.Options, ","))
	}
	if len(parts) == 0 {
		return "mounted"
	}
	return strings.Join(parts, ", ")
}

// --- /proc/mounts parsing ---

type mountEntry struct {
	device     string
	fsType     string
	options    map[string]bool
	rawOptions string
}

func parseProcMounts() (map[string]mountEntry, error) {
	return parseMountData(procMountsPath)
}

const procMountsPath = "/proc/mounts"

func parseMountData(path string) (map[string]mountEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseMountLines(string(data))
}

func parseMountLines(content string) (map[string]mountEntry, error) {
	result := make(map[string]mountEntry)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		mountPoint := unescapeOctal(fields[1])
		optList := strings.Split(fields[3], ",")
		optSet := make(map[string]bool, len(optList))
		for _, o := range optList {
			optSet[o] = true
		}
		result[mountPoint] = mountEntry{
			device:     fields[0],
			fsType:     fields[2],
			options:    optSet,
			rawOptions: fields[3],
		}
	}
	return result, nil
}

// --- /etc/fstab parsing & checking ---

type fstabEntry struct {
	device     string
	mountPoint string
	fsType     string
	options    []string
}

const fstabPath = "/etc/fstab"

func (ins *Instance) checkFstabMounts(q *safe.Queue[*types.Event], mountMap map[string]mountEntry) {
	data, err := os.ReadFile(fstabPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.Logger.Warnw("/etc/fstab not found, skipping fstab check")
			return
		}
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "mount::compliance",
			"target": "fstab",
		}).SetTitleRule("[check]").
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read /etc/fstab: %v", err)))
		return
	}

	entries := parseFstabLines(string(data))

	manualPaths := make(map[string]bool, len(ins.Mounts))
	for _, m := range ins.Mounts {
		manualPaths[m.Path] = true
	}

	excludeFSTypes := make(map[string]bool, len(ins.Fstab.ExcludeFSTypes))
	for _, ft := range ins.Fstab.ExcludeFSTypes {
		excludeFSTypes[strings.ToLower(ft)] = true
	}
	excludePaths := make(map[string]bool, len(ins.Fstab.ExcludePaths))
	for _, p := range ins.Fstab.ExcludePaths {
		excludePaths[p] = true
	}

	for _, entry := range entries {
		if entry.mountPoint == "none" || entry.fsType == "swap" {
			continue
		}
		if excludeFSTypes[entry.fsType] || excludePaths[entry.mountPoint] {
			continue
		}
		if hasOption(entry.options, "noauto") {
			continue
		}
		if manualPaths[entry.mountPoint] {
			continue
		}

		spec := &MountSpec{
			Path:     entry.mountPoint,
			FSType:   entry.fsType,
			Severity: ins.Fstab.Severity,
		}
		ins.checkMount(q, spec, mountMap)
	}
}

func parseFstabLines(content string) []fstabEntry {
	var entries []fstabEntry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		entries = append(entries, fstabEntry{
			device:     fields[0],
			mountPoint: unescapeOctal(fields[1]),
			fsType:     fields[2],
			options:    strings.Split(fields[3], ","),
		})
	}
	return entries
}

func hasOption(opts []string, target string) bool {
	for _, o := range opts {
		if o == target {
			return true
		}
	}
	return false
}

// unescapeOctal decodes octal escapes in /proc/mounts paths.
// e.g. \040 → space, \011 → tab, \012 → newline, \134 → backslash.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			oct := s[i+1 : i+4]
			if v, err := strconv.ParseUint(oct, 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
