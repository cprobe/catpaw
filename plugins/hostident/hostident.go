package hostident

import (
	"fmt"
	"os"
	"strings"

	"github.com/cprobe/digcore/config"
	"github.com/cprobe/digcore/pkg/safe"
	"github.com/cprobe/digcore/plugins"
	"github.com/cprobe/digcore/types"
)

const pluginName = "hostident"

// CheckConfig defines a single identity-change check with a configurable
// severity level. Enabled controls whether the check runs; Severity
// defaults to "Warning" if left empty.
type CheckConfig struct {
	Enabled  bool   `toml:"enabled"`
	Severity string `toml:"severity"`
}

type Instance struct {
	config.InternalConfig

	HostnameChanged CheckConfig `toml:"hostname_changed"`
	IPChanged       CheckConfig `toml:"ip_changed"`

	baseHostname string
	baseIP       string
	hostSeverity string
	ipSeverity   string
}

type HostidentPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &HostidentPlugin{}
	})
}

func (p *HostidentPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := range p.Instances {
		ret[i] = p.Instances[i]
	}
	return ret
}

func (ins *Instance) Init() error {
	if ins.HostnameChanged.Enabled {
		var err error
		if ins.hostSeverity, err = parseSeverity(ins.HostnameChanged.Severity); err != nil {
			return fmt.Errorf("hostident: hostname_changed.severity: %w", err)
		}
		h, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("hostident: failed to get baseline hostname: %w", err)
		}
		ins.baseHostname = h
	}

	if ins.IPChanged.Enabled {
		var err error
		if ins.ipSeverity, err = parseSeverity(ins.IPChanged.Severity); err != nil {
			return fmt.Errorf("hostident: ip_changed.severity: %w", err)
		}
		ins.baseIP = config.DetectIP()
		if ins.baseIP == "" {
			return fmt.Errorf("hostident: failed to detect baseline IP address")
		}
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.HostnameChanged.Enabled {
		ins.checkHostname(q)
	}
	if ins.IPChanged.Enabled {
		ins.checkIP(q)
	}
}

func (ins *Instance) checkHostname(q *safe.Queue[*types.Event]) {
	event := types.BuildEvent(map[string]string{
		"check":  "hostident::hostname_changed",
		"target": "system",
	})

	current, err := os.Hostname()
	if err != nil {
		q.PushFront(event.SetEventStatus(ins.hostSeverity).
			SetDescription(fmt.Sprintf("failed to get hostname: %v", err)).
			SetAttrs(map[string]string{"baseline": ins.baseHostname}))
		return
	}

	attrs := map[string]string{
		"baseline": ins.baseHostname,
		"current":  current,
	}

	if current != ins.baseHostname {
		q.PushFront(event.SetEventStatus(ins.hostSeverity).
			SetDescription(fmt.Sprintf("hostname changed from %q to %q since catpaw started, consider restarting catpaw",
				ins.baseHostname, current)).
			SetAttrs(attrs))
		return
	}

	q.PushFront(event.
		SetDescription(fmt.Sprintf("hostname %q unchanged", current)).
		SetAttrs(attrs))
}

func (ins *Instance) checkIP(q *safe.Queue[*types.Event]) {
	event := types.BuildEvent(map[string]string{
		"check":  "hostident::ip_changed",
		"target": "system",
	})

	current := config.DetectIP()

	attrs := map[string]string{
		"baseline": ins.baseIP,
		"current":  current,
	}

	if current == "" {
		q.PushFront(event.SetEventStatus(ins.ipSeverity).
			SetDescription("failed to detect current IP address").
			SetAttrs(attrs))
		return
	}

	if current != ins.baseIP {
		q.PushFront(event.SetEventStatus(ins.ipSeverity).
			SetDescription(fmt.Sprintf("IP changed from %s to %s since catpaw started, consider restarting catpaw",
				ins.baseIP, current)).
			SetAttrs(attrs))
		return
	}

	q.PushFront(event.
		SetDescription(fmt.Sprintf("IP %s unchanged", current)).
		SetAttrs(attrs))
}

// parseSeverity normalizes and validates a severity string.
// Returns "Warning" when input is empty (default).
func parseSeverity(raw string) (string, error) {
	if raw == "" {
		return types.EventStatusWarning, nil
	}
	switch strings.ToLower(raw) {
	case "critical":
		return types.EventStatusCritical, nil
	case "warning":
		return types.EventStatusWarning, nil
	case "info":
		return types.EventStatusInfo, nil
	default:
		return "", fmt.Errorf("unsupported severity %q, must be Critical, Warning, or Info", raw)
	}
}
