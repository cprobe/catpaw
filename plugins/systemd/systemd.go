package systemd

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/cmdx"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "systemd"

// suffixes that systemctl recognizes as explicit unit types
var knownUnitSuffixes = []string{
	".service", ".socket", ".timer", ".mount", ".automount",
	".swap", ".target", ".path", ".slice", ".scope",
}

type StateCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	Units               []string        `toml:"units"`
	ExpectedActiveState string          `toml:"expected_active_state"`
	Timeout             config.Duration `toml:"timeout"`
	Concurrency         int             `toml:"concurrency"`
	State               StateCheck      `toml:"state"`

	bin string
}

type SystemdPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *SystemdPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &SystemdPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd plugin only supports linux (current: %s)", runtime.GOOS)
	}

	if len(ins.Units) == 0 {
		return fmt.Errorf("units must not be empty")
	}

	for _, u := range ins.Units {
		if strings.TrimSpace(u) == "" {
			return fmt.Errorf("unit name must not be empty or blank")
		}
		if strings.ContainsAny(u, " \t") {
			return fmt.Errorf("unit name %q contains whitespace", u)
		}
	}

	bin, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found: %v", err)
	}
	ins.bin = bin

	if ins.ExpectedActiveState == "" {
		ins.ExpectedActiveState = "active"
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(5 * time.Second)
	}

	if ins.Concurrency == 0 {
		ins.Concurrency = 5
	}

	if ins.State.Severity == "" {
		ins.State.Severity = types.EventStatusCritical
	} else if !types.EventStatusValid(ins.State.Severity) {
		return fmt.Errorf("invalid state.severity %q, must be one of: Critical, Warning, Info, Ok", ins.State.Severity)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if len(ins.Units) == 0 {
		return
	}

	wg := new(sync.WaitGroup)
	se := semaphore.NewSemaphore(ins.Concurrency)

	for _, unit := range ins.Units {
		wg.Add(1)
		go func(unit string) {
			se.Acquire()
			defer func() {
				if r := recover(); r != nil {
					logger.Logger.Errorw("panic in systemd gather goroutine", "unit", unit, "recover", r)
					q.PushFront(types.BuildEvent(map[string]string{
						"check":  "systemd::state",
						"target": unit,
					}).SetTitleRule("[check] [target]").
						SetEventStatus(types.EventStatusCritical).
						SetDescription(fmt.Sprintf("panic during check: %v", r)))
				}
				se.Release()
				wg.Done()
			}()
			ins.gatherUnit(q, unit)
		}(unit)
	}
	wg.Wait()
}

func (ins *Instance) gatherUnit(q *safe.Queue[*types.Event], unit string) {
	canonicalUnit := normalizeUnitName(unit)

	logger.Logger.Debugw("systemd checking unit", "unit", canonicalUnit)

	props, err := ins.queryUnit(canonicalUnit)
	if err != nil {
		q.PushFront(ins.buildErrorEvent(unit, fmt.Sprintf("systemctl query failed: %v", err)))
		return
	}

	logger.Logger.Debugw("systemd unit properties",
		"unit", canonicalUnit,
		"ActiveState", props["ActiveState"],
		"SubState", props["SubState"],
		"LoadState", props["LoadState"],
		"UnitFileState", props["UnitFileState"],
		"Type", props["Type"],
	)

	loadState := props["LoadState"]
	activeState := props["ActiveState"]
	subState := props["SubState"]
	unitType := props["Type"]

	tr := ins.State.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	labels := map[string]string{
		"check":  "systemd::state",
		"target": unit,
	}
	attrLabels := map[string]string{
		types.AttrPrefix + "active_state":    activeState,
		types.AttrPrefix + "sub_state":       subState,
		types.AttrPrefix + "load_state":      loadState,
		types.AttrPrefix + "canonical_unit":  canonicalUnit,
	}
	if unitType != "" {
		attrLabels[types.AttrPrefix+"type"] = unitType
	}
	if v := props["MainPID"]; v != "" && v != "0" {
		attrLabels[types.AttrPrefix+"main_pid"] = v
	}
	if v := props["Description"]; v != "" {
		attrLabels[types.AttrPrefix+"description"] = v
	}
	if v := props["FragmentPath"]; v != "" {
		attrLabels[types.AttrPrefix+"fragment_path"] = v
	}
	if v := props["ActiveEnterTimestamp"]; v != "" {
		attrLabels[types.AttrPrefix+"active_enter_timestamp"] = v
	}
	if v := props["NRestarts"]; v != "" && v != "0" {
		attrLabels[types.AttrPrefix+"n_restarts"] = v
	}

	event := types.BuildEvent(labels, attrLabels).SetTitleRule(tr)

	// LoadState not-found: unit does not exist
	if loadState == "not-found" {
		q.PushFront(event.SetEventStatus(ins.State.Severity).
			SetDescription(fmt.Sprintf("unit %s not found (not installed or name is incorrect)", canonicalUnit)))
		return
	}

	// LoadState masked: unit is administratively disabled
	if loadState == "masked" {
		q.PushFront(event.SetEventStatus(ins.State.Severity).
			SetDescription(fmt.Sprintf("unit %s is masked (administratively disabled)", canonicalUnit)))
		return
	}

	// Low-noise: warn about oneshot/timer units when expected_active_state is "active"
	if ins.ExpectedActiveState == "active" && (unitType == "oneshot" || strings.HasSuffix(canonicalUnit, ".timer")) {
		logger.Logger.Warnw("systemd: unit type may not stay 'active' between runs, consider setting expected_active_state",
			"unit", canonicalUnit, "type", unitType)
	}

	if activeState == ins.ExpectedActiveState {
		q.PushFront(event.
			SetDescription(fmt.Sprintf("unit %s is %s(%s)", unit, activeState, subState)))
		return
	}

	// State mismatch
	desc := fmt.Sprintf("unit %s is %s(%s), expected %s", unit, activeState, subState, ins.ExpectedActiveState)
	if v := props["Result"]; v != "" && v != "success" {
		desc += fmt.Sprintf(", result: %s", v)
	}
	if v := props["ActiveEnterTimestamp"]; v != "" {
		desc += fmt.Sprintf(", last active: %s", v)
	}

	q.PushFront(event.SetEventStatus(ins.State.Severity).SetDescription(desc))
}

var queryProperties = []string{
	"LoadState",
	"ActiveState",
	"SubState",
	"Type",
	"MainPID",
	"NRestarts",
	"Result",
	"Description",
	"FragmentPath",
	"UnitFileState",
	"ActiveEnterTimestamp",
}

func (ins *Instance) queryUnit(unit string) (map[string]string, error) {
	args := []string{"show", unit, "--property=" + strings.Join(queryProperties, ",")}

	cmd := exec.Command(ins.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, time.Duration(ins.Timeout))
	if timedOut {
		return nil, fmt.Errorf("systemctl show timed out after %s", time.Duration(ins.Timeout))
	}
	if runErr != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return nil, fmt.Errorf("%v (stderr: %s)", runErr, errMsg)
		}
		return nil, runErr
	}

	return parseProperties(stdout.Bytes()), nil
}

func parseProperties(data []byte) map[string]string {
	props := make(map[string]string, len(queryProperties))
	for _, line := range bytes.Split(data, []byte("\n")) {
		s := string(line)
		idx := strings.IndexByte(s, '=')
		if idx < 0 {
			continue
		}
		props[s[:idx]] = s[idx+1:]
	}
	return props
}

func (ins *Instance) buildErrorEvent(unit, errMsg string) *types.Event {
	tr := ins.State.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	return types.BuildEvent(map[string]string{
		"check":  "systemd::state",
		"target": unit,
	}).SetTitleRule(tr).
		SetEventStatus(types.EventStatusCritical).
		SetDescription(errMsg)
}

// normalizeUnitName appends ".service" if the unit name has no known suffix.
func normalizeUnitName(unit string) string {
	for _, suffix := range knownUnitSuffixes {
		if strings.HasSuffix(unit, suffix) {
			return unit
		}
	}
	return unit + ".service"
}
