package ntp

import (
	"bytes"
	"fmt"
	"math"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/cmdx"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

const pluginName = "ntp"

const (
	modeChrony     = "chrony"
	modeNtpd       = "ntpd"
	modeTimedatectl = "timedatectl"
	modeAuto       = "auto"
)

type SyncCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type OffsetCheck struct {
	WarnGe     config.Duration `toml:"warn_ge"`
	CriticalGe config.Duration `toml:"critical_ge"`
	TitleRule   string          `toml:"title_rule"`
}

type StratumCheck struct {
	WarnGe     int    `toml:"warn_ge"`
	CriticalGe int    `toml:"critical_ge"`
	TitleRule   string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	Mode          string          `toml:"mode"`
	Timeout       config.Duration `toml:"timeout"`
	ErrorSeverity string          `toml:"error_severity"`
	Sync          SyncCheck       `toml:"sync"`
	Offset        OffsetCheck     `toml:"offset"`
	Stratum       StratumCheck    `toml:"stratum"`

	detectedMode string
	bin          string
}

type NTPPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *NTPPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &NTPPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("ntp plugin only supports linux (current: %s)", runtime.GOOS)
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(10 * time.Second)
	}

	if ins.ErrorSeverity == "" {
		ins.ErrorSeverity = types.EventStatusCritical
	} else if !types.EventStatusValid(ins.ErrorSeverity) {
		return fmt.Errorf("invalid error_severity %q", ins.ErrorSeverity)
	}

	if ins.Sync.Severity == "" {
		ins.Sync.Severity = types.EventStatusCritical
	} else if !types.EventStatusValid(ins.Sync.Severity) {
		return fmt.Errorf("invalid sync.severity %q", ins.Sync.Severity)
	}

	if ins.Offset.WarnGe > 0 && ins.Offset.CriticalGe > 0 && ins.Offset.WarnGe >= ins.Offset.CriticalGe {
		return fmt.Errorf("offset.warn_ge(%s) must be less than offset.critical_ge(%s)",
			time.Duration(ins.Offset.WarnGe), time.Duration(ins.Offset.CriticalGe))
	}

	if ins.Stratum.WarnGe > 0 && ins.Stratum.CriticalGe > 0 && ins.Stratum.WarnGe >= ins.Stratum.CriticalGe {
		return fmt.Errorf("stratum.warn_ge(%d) must be less than stratum.critical_ge(%d)",
			ins.Stratum.WarnGe, ins.Stratum.CriticalGe)
	}

	mode := strings.TrimSpace(ins.Mode)
	if mode == "" {
		mode = modeAuto
	}

	switch mode {
	case modeAuto:
		detected, bin, err := autoDetect()
		if err != nil {
			return err
		}
		ins.detectedMode = detected
		ins.bin = bin
	case modeChrony:
		bin, err := exec.LookPath("chronyc")
		if err != nil {
			return fmt.Errorf("chronyc not found: %v", err)
		}
		ins.detectedMode = modeChrony
		ins.bin = bin
	case modeNtpd:
		bin, err := exec.LookPath("ntpq")
		if err != nil {
			return fmt.Errorf("ntpq not found: %v", err)
		}
		ins.detectedMode = modeNtpd
		ins.bin = bin
	case modeTimedatectl:
		bin, err := exec.LookPath("timedatectl")
		if err != nil {
			return fmt.Errorf("timedatectl not found: %v", err)
		}
		ins.detectedMode = modeTimedatectl
		ins.bin = bin
	default:
		return fmt.Errorf("invalid mode %q, must be one of: auto, chrony, ntpd, timedatectl", mode)
	}

	if ins.detectedMode == modeTimedatectl {
		if ins.Offset.WarnGe > 0 || ins.Offset.CriticalGe > 0 {
			logger.Logger.Warnw("ntp: timedatectl does not provide offset data, offset thresholds will be ignored")
		}
		if ins.Stratum.WarnGe > 0 || ins.Stratum.CriticalGe > 0 {
			logger.Logger.Warnw("ntp: timedatectl does not provide stratum data, stratum thresholds will be ignored")
		}
	}

	logger.Logger.Infow("ntp: initialized", "mode", ins.detectedMode, "bin", ins.bin)

	return nil
}

func autoDetect() (string, string, error) {
	if bin, err := exec.LookPath("chronyc"); err == nil {
		return modeChrony, bin, nil
	}
	if bin, err := exec.LookPath("ntpq"); err == nil {
		return modeNtpd, bin, nil
	}
	if bin, err := exec.LookPath("timedatectl"); err == nil {
		return modeTimedatectl, bin, nil
	}
	return "", "", fmt.Errorf("no NTP tool found (tried chronyc, ntpq, timedatectl)")
}

// ntpResult holds the parsed NTP state from any backend.
type ntpResult struct {
	synced  bool
	offset  time.Duration
	stratum int
	source  string
	extra   map[string]string // additional key-value pairs for attr labels
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	logger.Logger.Debugw("ntp gather", "mode", ins.detectedMode)

	result, err := ins.query()
	if err != nil {
		q.PushFront(ins.buildErrorEvent(fmt.Sprintf("NTP query failed (%s): %v", ins.detectedMode, err)))
		return
	}

	logger.Logger.Debugw("ntp query result",
		"mode", ins.detectedMode,
		"synced", result.synced,
		"offset", result.offset,
		"stratum", result.stratum,
		"source", result.source,
	)

	ins.checkSync(q, result)

	if ins.detectedMode != modeTimedatectl {
		ins.checkOffset(q, result)
		ins.checkStratum(q, result)
	}
}

func (ins *Instance) query() (*ntpResult, error) {
	switch ins.detectedMode {
	case modeChrony:
		return ins.queryChrony()
	case modeNtpd:
		return ins.queryNtpd()
	case modeTimedatectl:
		return ins.queryTimedatectl()
	default:
		return nil, fmt.Errorf("unknown mode: %s", ins.detectedMode)
	}
}

// --- chrony backend ---

func (ins *Instance) queryChrony() (*ntpResult, error) {
	cmd := exec.Command(ins.bin, "-n", "tracking")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, time.Duration(ins.Timeout))
	if timedOut {
		return nil, fmt.Errorf("chronyc tracking timed out after %s", time.Duration(ins.Timeout))
	}
	if runErr != nil {
		return nil, fmt.Errorf("chronyc tracking failed: %v (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))
	}

	return parseChronyTracking(stdout.Bytes())
}

func parseChronyTracking(data []byte) (*ntpResult, error) {
	r := &ntpResult{extra: make(map[string]string)}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "Reference ID":
			// "0A000001 (10.0.0.1)" → extract address in parentheses
			if idx := strings.Index(val, "("); idx >= 0 {
				end := strings.Index(val, ")")
				if end > idx {
					r.source = val[idx+1 : end]
				}
			}
			r.extra["reference_id"] = val
		case "Stratum":
			if n, err := strconv.Atoi(val); err == nil {
				r.stratum = n
			}
		case "System time":
			// "0.000012345 seconds slow of NTP time" or "... fast of NTP time"
			r.offset = parseChronyOffset(val)
			r.extra["system_time"] = val
		case "Last offset":
			r.extra["last_offset"] = val
		case "RMS offset":
			r.extra["rms_offset"] = val
		case "Leap status":
			r.extra["leap_status"] = val
			r.synced = (val == "Normal")
		}
	}

	return r, nil
}

func parseChronyOffset(val string) time.Duration {
	// "0.000012345 seconds slow of NTP time"
	// "0.000012345 seconds fast of NTP time"
	fields := strings.Fields(val)
	if len(fields) < 3 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	if strings.Contains(val, "slow") {
		f = -f
	}
	return time.Duration(f * float64(time.Second))
}

// --- ntpd backend ---

func (ins *Instance) queryNtpd() (*ntpResult, error) {
	cmd := exec.Command(ins.bin, "-pn")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, time.Duration(ins.Timeout))
	if timedOut {
		return nil, fmt.Errorf("ntpq -pn timed out after %s", time.Duration(ins.Timeout))
	}
	if runErr != nil {
		return nil, fmt.Errorf("ntpq -pn failed: %v (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))
	}

	return parseNtpqOutput(stdout.Bytes())
}

func parseNtpqOutput(data []byte) (*ntpResult, error) {
	r := &ntpResult{extra: make(map[string]string)}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		// The active sync peer line starts with '*'
		if line[0] != '*' {
			continue
		}

		r.synced = true

		// *remote refid st t when poll reach delay offset jitter
		fields := strings.Fields(line[1:]) // strip the '*' prefix
		if len(fields) < 10 {
			continue
		}

		r.source = fields[0]

		if n, err := strconv.Atoi(fields[2]); err == nil {
			r.stratum = n
		}

		// offset is in milliseconds
		if f, err := strconv.ParseFloat(fields[8], 64); err == nil {
			r.offset = time.Duration(f * float64(time.Millisecond))
		}

		r.extra["delay"] = fields[7] + "ms"
		r.extra["jitter"] = fields[9] + "ms"
		r.extra["refid"] = fields[1]

		break
	}

	return r, nil
}

// --- timedatectl backend ---

func (ins *Instance) queryTimedatectl() (*ntpResult, error) {
	cmd := exec.Command(ins.bin, "show")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, time.Duration(ins.Timeout))
	if timedOut {
		return nil, fmt.Errorf("timedatectl show timed out after %s", time.Duration(ins.Timeout))
	}
	if runErr != nil {
		return nil, fmt.Errorf("timedatectl show failed: %v (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))
	}

	return parseTimedatectl(stdout.Bytes())
}

func parseTimedatectl(data []byte) (*ntpResult, error) {
	r := &ntpResult{extra: make(map[string]string)}

	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "NTP":
			r.extra["ntp_enabled"] = val
		case "NTPSynchronized":
			r.synced = (val == "yes")
			r.extra["ntp_synchronized"] = val
		}
	}

	return r, nil
}

// --- check dimensions ---

func (ins *Instance) checkSync(q *safe.Queue[*types.Event], r *ntpResult) {
	if ins.Sync.Severity == "" {
		return
	}

	tr := ins.Sync.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check":  "ntp::sync",
		"target": "ntp",
	}, ins.attrLabels(r)).SetTitleRule(tr)

	if r.synced {
		desc := fmt.Sprintf("NTP synchronized (mode: %s", ins.detectedMode)
		if r.source != "" {
			desc += fmt.Sprintf(", source: %s", r.source)
		}
		desc += ")"
		q.PushFront(event.SetDescription(desc))
		return
	}

	desc := fmt.Sprintf("NTP not synchronized (mode: %s", ins.detectedMode)
	if leap, ok := r.extra["leap_status"]; ok {
		desc += fmt.Sprintf(", leap status: %s", leap)
	}
	desc += "), clock may be drifting"

	q.PushFront(event.SetEventStatus(ins.Sync.Severity).SetDescription(desc))
}

func (ins *Instance) checkOffset(q *safe.Queue[*types.Event], r *ntpResult) {
	if ins.Offset.WarnGe == 0 && ins.Offset.CriticalGe == 0 {
		return
	}

	if !r.synced {
		return
	}

	tr := ins.Offset.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	absOffset := time.Duration(math.Abs(float64(r.offset)))

	event := types.BuildEvent(map[string]string{
		"check":                        "ntp::offset",
		"target":                       "ntp",
		types.AttrPrefix + "offset":    r.offset.String(),
		types.AttrPrefix + "abs_offset": absOffset.String(),
	}, ins.attrLabels(r)).SetTitleRule(tr)

	if ins.Offset.CriticalGe > 0 && absOffset >= time.Duration(ins.Offset.CriticalGe) {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("time offset %s >= critical threshold %s",
				absOffset, time.Duration(ins.Offset.CriticalGe))))
		return
	}

	if ins.Offset.WarnGe > 0 && absOffset >= time.Duration(ins.Offset.WarnGe) {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("time offset %s >= warning threshold %s",
				absOffset, time.Duration(ins.Offset.WarnGe))))
		return
	}

	q.PushFront(event.SetDescription(fmt.Sprintf("time offset %s, everything is ok", absOffset)))
}

func (ins *Instance) checkStratum(q *safe.Queue[*types.Event], r *ntpResult) {
	if ins.Stratum.WarnGe == 0 && ins.Stratum.CriticalGe == 0 {
		return
	}

	if !r.synced {
		return
	}

	tr := ins.Stratum.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check":                         "ntp::stratum",
		"target":                        "ntp",
		types.AttrPrefix + "stratum":    fmt.Sprintf("%d", r.stratum),
	}, ins.attrLabels(r)).SetTitleRule(tr)

	if ins.Stratum.CriticalGe > 0 && r.stratum >= ins.Stratum.CriticalGe {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("NTP stratum %d >= critical threshold %d (time source unreliable)",
				r.stratum, ins.Stratum.CriticalGe)))
		return
	}

	if ins.Stratum.WarnGe > 0 && r.stratum >= ins.Stratum.WarnGe {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("NTP stratum %d >= warning threshold %d (time source may be unreliable)",
				r.stratum, ins.Stratum.WarnGe)))
		return
	}

	q.PushFront(event.SetDescription(fmt.Sprintf("NTP stratum %d, everything is ok", r.stratum)))
}

// --- helpers ---

func (ins *Instance) attrLabels(r *ntpResult) map[string]string {
	m := map[string]string{
		types.AttrPrefix + "mode": ins.detectedMode,
	}
	if r.source != "" {
		m[types.AttrPrefix+"source"] = r.source
	}
	if r.stratum > 0 {
		m[types.AttrPrefix+"stratum"] = fmt.Sprintf("%d", r.stratum)
	}
	for k, v := range r.extra {
		m[types.AttrPrefix+k] = v
	}
	return m
}

func (ins *Instance) buildErrorEvent(errMsg string) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":                        "ntp::sync",
		"target":                       "ntp",
		types.AttrPrefix + "mode":      ins.detectedMode,
	}).SetTitleRule("[check] [target]").
		SetEventStatus(ins.ErrorSeverity).
		SetDescription(errMsg)
}
