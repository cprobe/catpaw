package secmod

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

const pluginName = "secmod"

var validSELinuxModes = map[string]bool{
	"enforcing":  true,
	"permissive": true,
	"disabled":   true,
}

var validAppArmorStates = map[string]bool{
	"yes": true,
	"no":  true,
}

type EnforceModeCheck struct {
	Expect    string `toml:"expect"`
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type AppArmorCheck struct {
	Expect    string `toml:"expect"`
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	EnforceMode EnforceModeCheck `toml:"enforce_mode"`
	AppArmor    AppArmorCheck    `toml:"apparmor_enabled"`
}

type SecmodPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *SecmodPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &SecmodPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("secmod plugin only supports linux (current: %s)", runtime.GOOS)
	}

	ins.EnforceMode.Expect = strings.TrimSpace(strings.ToLower(ins.EnforceMode.Expect))
	ins.AppArmor.Expect = strings.TrimSpace(strings.ToLower(ins.AppArmor.Expect))

	if ins.EnforceMode.Expect == "" && ins.AppArmor.Expect == "" {
		return fmt.Errorf("at least one check must be configured (enforce_mode.expect or apparmor_enabled.expect)")
	}

	if ins.EnforceMode.Expect != "" {
		if !validSELinuxModes[ins.EnforceMode.Expect] {
			return fmt.Errorf("enforce_mode.expect must be one of: enforcing, permissive, disabled (got %q)", ins.EnforceMode.Expect)
		}
		if err := normalizeSeverity(&ins.EnforceMode.Severity); err != nil {
			return fmt.Errorf("enforce_mode.severity: %v", err)
		}
	}

	if ins.AppArmor.Expect != "" {
		if !validAppArmorStates[ins.AppArmor.Expect] {
			return fmt.Errorf("apparmor_enabled.expect must be one of: yes, no (got %q)", ins.AppArmor.Expect)
		}
		if err := normalizeSeverity(&ins.AppArmor.Severity); err != nil {
			return fmt.Errorf("apparmor_enabled.severity: %v", err)
		}
	}

	return nil
}

func normalizeSeverity(s *string) error {
	*s = strings.TrimSpace(*s)
	if *s == "" {
		*s = types.EventStatusWarning
	}
	if !isAlertSeverity(*s) {
		return fmt.Errorf("invalid severity %q, must be Info, Warning or Critical", *s)
	}
	return nil
}

func isAlertSeverity(s string) bool {
	return s == types.EventStatusInfo || s == types.EventStatusWarning || s == types.EventStatusCritical
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.EnforceMode.Expect != "" {
		ins.checkEnforceMode(q)
	}
	if ins.AppArmor.Expect != "" {
		ins.checkAppArmor(q)
	}
}

func (ins *Instance) checkEnforceMode(q *safe.Queue[*types.Event]) {
	tr := ins.EnforceMode.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	actual, err := readSELinuxMode()
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "secmod::selinux_mode",
			"target": "selinux",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read SELinux status: %v", err)))
		return
	}

	event := types.BuildEvent(map[string]string{
		"check":                     "secmod::selinux_mode",
		"target":                    "selinux",
		types.AttrPrefix + "actual": actual,
		types.AttrPrefix + "expect": ins.EnforceMode.Expect,
	}).SetTitleRule(tr)

	if actual == ins.EnforceMode.Expect {
		event.SetEventStatus(types.EventStatusOk).
			SetDescription(fmt.Sprintf("SELinux mode is %s, matches expectation", actual))
	} else {
		event.SetEventStatus(ins.EnforceMode.Severity).
			SetDescription(fmt.Sprintf("SELinux mode is %s, expected %s", actual, ins.EnforceMode.Expect))
	}

	q.PushFront(event)
}

func (ins *Instance) checkAppArmor(q *safe.Queue[*types.Event]) {
	tr := ins.AppArmor.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	actual, err := readAppArmorStatus()
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "secmod::apparmor_enabled",
			"target": "apparmor",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read AppArmor status: %v", err)))
		return
	}

	event := types.BuildEvent(map[string]string{
		"check":                     "secmod::apparmor_enabled",
		"target":                    "apparmor",
		types.AttrPrefix + "actual": actual,
		types.AttrPrefix + "expect": ins.AppArmor.Expect,
	}).SetTitleRule(tr)

	if actual == ins.AppArmor.Expect {
		event.SetEventStatus(types.EventStatusOk).
			SetDescription(fmt.Sprintf("AppArmor is %s, matches expectation", actual))
	} else {
		event.SetEventStatus(ins.AppArmor.Severity).
			SetDescription(fmt.Sprintf("AppArmor is %s, expected %s", actual, ins.AppArmor.Expect))
	}

	q.PushFront(event)
}

// readSELinuxMode reads the runtime SELinux enforcement mode.
// Returns "disabled" when SELinux is not present (file not found is not an error).
func readSELinuxMode() (string, error) {
	data, err := os.ReadFile("/sys/fs/selinux/enforce")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "disabled", nil
		}
		return "", err
	}
	val := strings.TrimSpace(string(data))
	switch val {
	case "1":
		return "enforcing", nil
	case "0":
		return "permissive", nil
	default:
		return "", fmt.Errorf("unexpected value in /sys/fs/selinux/enforce: %q", val)
	}
}

// readAppArmorStatus reads whether AppArmor is enabled.
// Returns "no" when the module is not loaded (file not found is not an error).
func readAppArmorStatus() (string, error) {
	data, err := os.ReadFile("/sys/module/apparmor/parameters/enabled")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "no", nil
		}
		return "", err
	}
	val := strings.TrimSpace(string(data))
	switch val {
	case "Y":
		return "yes", nil
	case "N":
		return "no", nil
	default:
		return "", fmt.Errorf("unexpected value in /sys/module/apparmor/parameters/enabled: %q", val)
	}
}
