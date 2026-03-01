package netif

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

const pluginName = "netif"

var (
	sysClassNet   = "/sys/class/net"
	runtimeGOOS   = runtime.GOOS
)

type DeltaCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type LinkSpec struct {
	Interface string `toml:"interface"`
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	Include []string   `toml:"include"`
	Exclude []string   `toml:"exclude"`

	Errors DeltaCheck `toml:"errors"`
	Drops  DeltaCheck `toml:"drops"`
	LinkUp []LinkSpec `toml:"link_up"`

	hasErrorCheck bool
	hasDropCheck  bool

	prevCounters map[string]*ifCounters
	initialized  bool
}

type NetifPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *NetifPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &NetifPlugin{}
	})
}

type ifCounters struct {
	rxErrors  uint64
	txErrors  uint64
	rxDropped uint64
	txDropped uint64
}

func (ins *Instance) Init() error {
	if runtimeGOOS != "linux" {
		return fmt.Errorf("netif plugin only supports linux (current: %s)", runtimeGOOS)
	}

	ins.hasErrorCheck = ins.Errors.WarnGe > 0 || ins.Errors.CriticalGe > 0
	ins.hasDropCheck = ins.Drops.WarnGe > 0 || ins.Drops.CriticalGe > 0
	hasLinkCheck := len(ins.LinkUp) > 0

	if !ins.hasErrorCheck && !ins.hasDropCheck && !hasLinkCheck {
		return fmt.Errorf("at least one check must be configured (errors, drops, or link_up)")
	}

	if err := validateDeltaCheck("errors", ins.Errors); err != nil {
		return err
	}
	if err := validateDeltaCheck("drops", ins.Drops); err != nil {
		return err
	}

	seen := make(map[string]bool)
	for i := range ins.LinkUp {
		ins.LinkUp[i].Interface = strings.TrimSpace(ins.LinkUp[i].Interface)
		if ins.LinkUp[i].Interface == "" {
			return fmt.Errorf("link_up[%d].interface must not be empty", i)
		}
		if seen[ins.LinkUp[i].Interface] {
			return fmt.Errorf("duplicate link_up interface: %s", ins.LinkUp[i].Interface)
		}
		seen[ins.LinkUp[i].Interface] = true

		if err := normalizeSeverity(&ins.LinkUp[i].Severity); err != nil {
			return fmt.Errorf("link_up[%d] (%s): %v", i, ins.LinkUp[i].Interface, err)
		}
	}

	for _, pat := range ins.Include {
		if _, err := filepath.Match(pat, "test"); err != nil {
			return fmt.Errorf("invalid include glob pattern %q: %v", pat, err)
		}
	}
	for _, pat := range ins.Exclude {
		if _, err := filepath.Match(pat, "test"); err != nil {
			return fmt.Errorf("invalid exclude glob pattern %q: %v", pat, err)
		}
	}

	ins.prevCounters = make(map[string]*ifCounters)

	return nil
}

func validateDeltaCheck(name string, dc DeltaCheck) error {
	if dc.WarnGe < 0 {
		return fmt.Errorf("%s.warn_ge must be >= 0 (got %.1f)", name, dc.WarnGe)
	}
	if dc.CriticalGe < 0 {
		return fmt.Errorf("%s.critical_ge must be >= 0 (got %.1f)", name, dc.CriticalGe)
	}
	if dc.WarnGe > 0 && dc.CriticalGe > 0 && dc.WarnGe >= dc.CriticalGe {
		return fmt.Errorf("%s.warn_ge(%.1f) must be less than %s.critical_ge(%.1f)", name, dc.WarnGe, name, dc.CriticalGe)
	}
	return nil
}

func normalizeSeverity(s *string) error {
	*s = strings.TrimSpace(*s)
	if *s == "" {
		*s = types.EventStatusCritical
	}
	switch *s {
	case types.EventStatusInfo, types.EventStatusWarning, types.EventStatusCritical:
		return nil
	default:
		return fmt.Errorf("invalid severity %q, must be Info, Warning or Critical", *s)
	}
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.hasErrorCheck || ins.hasDropCheck {
		ins.gatherErrorsAndDrops(q)
	}

	for i := range ins.LinkUp {
		ins.gatherLink(q, &ins.LinkUp[i])
	}
}

func (ins *Instance) gatherErrorsAndDrops(q *safe.Queue[*types.Event]) {
	allIfaces, err := listInterfaces()
	if err != nil {
		logger.Logger.Warnw("netif: failed to list interfaces", "error", err)
		return
	}

	matched := applyFilter(allIfaces, ins.Include, ins.Exclude)

	currentCounters := make(map[string]*ifCounters, len(matched))
	for _, iface := range matched {
		counters, err := readCounters(iface)
		if counters == nil {
			if err != nil {
				logger.Logger.Warnw("netif: failed to read counters", "interface", iface, "error", err)
			}
			continue
		}
		currentCounters[iface] = counters
	}

	if !ins.initialized {
		ins.prevCounters = currentCounters
		ins.initialized = true
		return
	}

	for iface, curr := range currentCounters {
		prev := ins.prevCounters[iface]
		if prev == nil {
			continue
		}

		if ins.hasErrorCheck {
			emitDeltaEvent(q, "netif::errors", "errors", iface, ins.Errors,
				curr.rxErrors, prev.rxErrors, curr.txErrors, prev.txErrors)
		}

		if ins.hasDropCheck {
			emitDeltaEvent(q, "netif::drops", "drops", iface, ins.Drops,
				curr.rxDropped, prev.rxDropped, curr.txDropped, prev.txDropped)
		}
	}

	ins.prevCounters = currentCounters
}

func emitDeltaEvent(q *safe.Queue[*types.Event], checkLabel, kind, iface string, dc DeltaCheck,
	currRx, prevRx, currTx, prevTx uint64) {
	rxDelta := safeDelta(currRx, prevRx)
	txDelta := safeDelta(currTx, prevTx)
	delta := rxDelta + txDelta
	total := currRx + currTx

	tr := dc.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check":                    checkLabel,
		"target":                   iface,
		types.AttrPrefix + "delta": strconv.FormatUint(delta, 10),
		types.AttrPrefix + "rx":    strconv.FormatUint(rxDelta, 10),
		types.AttrPrefix + "tx":    strconv.FormatUint(txDelta, 10),
		types.AttrPrefix + "total": strconv.FormatUint(total, 10),
	}).SetTitleRule(tr)

	status := types.EvaluateGeThreshold(float64(delta), dc.WarnGe, dc.CriticalGe)
	event.SetEventStatus(status)

	switch status {
	case types.EventStatusCritical:
		event.SetDescription(fmt.Sprintf("%s has %d new %s since last check (rx: %d, tx: %d, total: %d), above critical threshold %.0f",
			iface, delta, kind, rxDelta, txDelta, total, dc.CriticalGe))
	case types.EventStatusWarning:
		event.SetDescription(fmt.Sprintf("%s has %d new %s since last check (rx: %d, tx: %d, total: %d), above warning threshold %.0f",
			iface, delta, kind, rxDelta, txDelta, total, dc.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("%s no new %s (total: %d)", iface, kind, total))
	}

	q.PushFront(event)
}

func (ins *Instance) gatherLink(q *safe.Queue[*types.Event], spec *LinkSpec) {
	tr := spec.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	operstate, err := readOperstate(spec.Interface)
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "netif::link",
			"target": spec.Interface,
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read %s operstate: %v", spec.Interface, err)))
		return
	}

	event := types.BuildEvent(map[string]string{
		"check":                          "netif::link",
		"target":                         spec.Interface,
		types.AttrPrefix + "operstate":   operstate,
		types.AttrPrefix + "expect":      "up",
	}).SetTitleRule(tr)

	if operstate == "not_found" {
		q.PushFront(event.SetEventStatus(spec.Severity).
			SetDescription(fmt.Sprintf("%s interface not found", spec.Interface)))
		return
	}

	if operstate == "up" || operstate == "unknown" {
		q.PushFront(event.SetEventStatus(types.EventStatusOk).
			SetDescription(fmt.Sprintf("%s link is up", spec.Interface)))
		return
	}

	q.PushFront(event.SetEventStatus(spec.Severity).
		SetDescription(fmt.Sprintf("%s link is not ready (operstate: %s)", spec.Interface, operstate)))
}

// --- I/O helpers (package-level vars for testing) ---

var listInterfaces = defaultListInterfaces

func defaultListInterfaces() ([]string, error) {
	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return nil, err
	}
	ifaces := make([]string, 0, len(entries))
	for _, e := range entries {
		ifaces = append(ifaces, e.Name())
	}
	return ifaces, nil
}

func applyFilter(ifaces, include, exclude []string) []string {
	var result []string
	for _, iface := range ifaces {
		if len(include) > 0 && !matchAny(iface, include) {
			continue
		}
		if matchAny(iface, exclude) {
			continue
		}
		result = append(result, iface)
	}
	return result
}

func matchAny(name string, patterns []string) bool {
	for _, pat := range patterns {
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
	}
	return false
}

var readCounters = defaultReadCounters

func defaultReadCounters(iface string) (*ifCounters, error) {
	base := filepath.Join(sysClassNet, iface, "statistics")
	rxErr, err := readUint64File(filepath.Join(base, "rx_errors"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	txErr, err := readUint64File(filepath.Join(base, "tx_errors"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	rxDrop, err := readUint64File(filepath.Join(base, "rx_dropped"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	txDrop, err := readUint64File(filepath.Join(base, "tx_dropped"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &ifCounters{
		rxErrors:  rxErr,
		txErrors:  txErr,
		rxDropped: rxDrop,
		txDropped: txDrop,
	}, nil
}

var readOperstate = defaultReadOperstate

func defaultReadOperstate(iface string) (string, error) {
	data, err := os.ReadFile(filepath.Join(sysClassNet, iface, "operstate"))
	if err != nil {
		if os.IsNotExist(err) {
			return "not_found", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readUint64File(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

func safeDelta(current, prev uint64) uint64 {
	if current >= prev {
		return current - prev
	}
	return 0
}
