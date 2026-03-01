package sysctl

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

const pluginName = "sysctl"

var validOps = map[string]bool{
	"eq": true, "ne": true,
	"ge": true, "le": true,
	"gt": true, "lt": true,
}

type ParamSpec struct {
	Key      string `toml:"key"`
	Op       string `toml:"op"`
	Value    string `toml:"value"`
	Severity string `toml:"severity"`
}

type ParamCheck struct {
	Params    []ParamSpec `toml:"params"`
	TitleRule string      `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	ParamCheck ParamCheck `toml:"param_check"`
}

type SysctlPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *SysctlPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &SysctlPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("sysctl plugin only supports linux (current: %s)", runtime.GOOS)
	}

	if len(ins.ParamCheck.Params) == 0 {
		return fmt.Errorf("param_check.params must not be empty")
	}

	for i := range ins.ParamCheck.Params {
		p := &ins.ParamCheck.Params[i]

		p.Key = strings.TrimSpace(p.Key)
		if p.Key == "" {
			return fmt.Errorf("param_check.params[%d]: key must not be empty", i)
		}

		if err := validateKey(p.Key); err != nil {
			return fmt.Errorf("param_check.params[%d]: %v", i, err)
		}

		p.Op = strings.TrimSpace(strings.ToLower(p.Op))
		if p.Op == "" {
			p.Op = "eq"
		}
		if !validOps[p.Op] {
			return fmt.Errorf("param_check.params[%d] (%s): invalid op %q, must be one of: eq, ne, ge, le, gt, lt", i, p.Key, p.Op)
		}

		p.Value = strings.TrimSpace(p.Value)
		if p.Value == "" {
			return fmt.Errorf("param_check.params[%d] (%s): value must not be empty", i, p.Key)
		}

		if isNumericOp(p.Op) {
			if _, err := strconv.ParseFloat(p.Value, 64); err != nil {
				return fmt.Errorf("param_check.params[%d] (%s): value %q must be numeric for op %q", i, p.Key, p.Value, p.Op)
			}
		}

		p.Severity = strings.TrimSpace(p.Severity)
		if p.Severity == "" {
			p.Severity = types.EventStatusWarning
		}
		if !isAlertSeverity(p.Severity) {
			return fmt.Errorf("param_check.params[%d] (%s): invalid severity %q, must be Info, Warning or Critical", i, p.Key, p.Severity)
		}
	}

	return nil
}

func validateKey(key string) error {
	if strings.Contains(key, "/") {
		return fmt.Errorf("key %q must not contain '/' (use '.' as separator)", key)
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("key %q must not contain '..'", key)
	}
	if strings.HasPrefix(key, ".") {
		return fmt.Errorf("key %q must not start with '.'", key)
	}
	if strings.HasSuffix(key, ".") {
		return fmt.Errorf("key %q must not end with '.'", key)
	}
	return nil
}

func isNumericOp(op string) bool {
	return op == "ge" || op == "le" || op == "gt" || op == "lt"
}

func isAlertSeverity(s string) bool {
	return s == types.EventStatusInfo || s == types.EventStatusWarning || s == types.EventStatusCritical
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	tr := ins.ParamCheck.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	for i := range ins.ParamCheck.Params {
		p := &ins.ParamCheck.Params[i]
		ins.checkParam(q, p, tr)
	}
}

func (ins *Instance) checkParam(q *safe.Queue[*types.Event], p *ParamSpec, titleRule string) {
	path := keyToPath(p.Key)

	data, err := os.ReadFile(path)
	if err != nil {
		event := buildEvent(p, titleRule)
		if errors.Is(err, os.ErrNotExist) {
			event.SetEventStatus(p.Severity).
				SetDescription(fmt.Sprintf("%s: parameter not found (file %s does not exist)", p.Key, path))
		} else {
			event.SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("%s: failed to read parameter: %v", p.Key, err))
		}
		q.PushFront(event)
		return
	}

	actual := strings.TrimSpace(string(data))

	matched, err := compareValues(actual, p.Value, p.Op)
	if err != nil {
		event := buildEvent(p, titleRule)
		event.Labels[types.AttrPrefix+"actual"] = actual
		event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("%s: comparison error: %v", p.Key, err))
		q.PushFront(event)
		return
	}

	event := buildEvent(p, titleRule)
	event.Labels[types.AttrPrefix+"actual"] = actual

	if matched {
		event.SetEventStatus(types.EventStatusOk).
			SetDescription(fmt.Sprintf("%s = %s, matches expectation (%s %s)", p.Key, actual, p.Op, p.Value))
	} else {
		event.SetEventStatus(p.Severity).
			SetDescription(fmt.Sprintf("%s = %s, expected %s %s", p.Key, actual, p.Op, p.Value))
	}

	q.PushFront(event)
}

func buildEvent(p *ParamSpec, titleRule string) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":                     "sysctl::param_check",
		"target":                    p.Key,
		types.AttrPrefix + "expect": p.Op + " " + p.Value,
	}).SetTitleRule(titleRule)
}

func keyToPath(key string) string {
	return "/proc/sys/" + strings.ReplaceAll(key, ".", "/")
}

// normalizeWhitespace collapses consecutive whitespace into single spaces.
// This handles multi-value sysctl params like net.ipv4.tcp_rmem = "4096\t131072\t6291456".
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func compareValues(actual, expect, op string) (bool, error) {
	switch op {
	case "eq":
		return normalizeWhitespace(actual) == normalizeWhitespace(expect), nil
	case "ne":
		return normalizeWhitespace(actual) != normalizeWhitespace(expect), nil
	default:
		cmp, err := compareNumeric(actual, expect)
		if err != nil {
			return false, err
		}
		switch op {
		case "ge":
			return cmp >= 0, nil
		case "le":
			return cmp <= 0, nil
		case "gt":
			return cmp > 0, nil
		case "lt":
			return cmp < 0, nil
		default:
			return false, fmt.Errorf("unknown op %q", op)
		}
	}
}

// compareNumeric compares two numeric strings. Returns -1, 0, or 1.
// Prefers integer parsing to avoid float64 precision loss on large values.
func compareNumeric(actual, expect string) (int, error) {
	ai, err1 := strconv.ParseInt(actual, 10, 64)
	bi, err2 := strconv.ParseInt(expect, 10, 64)
	if err1 == nil && err2 == nil {
		switch {
		case ai < bi:
			return -1, nil
		case ai > bi:
			return 1, nil
		default:
			return 0, nil
		}
	}

	af, err1 := strconv.ParseFloat(actual, 64)
	if err1 != nil {
		return 0, fmt.Errorf("actual value %q is not numeric", actual)
	}
	bf, err2 := strconv.ParseFloat(expect, 64)
	if err2 != nil {
		return 0, fmt.Errorf("value %q is not numeric", expect)
	}
	switch {
	case af < bf:
		return -1, nil
	case af > bf:
		return 1, nil
	default:
		return 0, nil
	}
}
