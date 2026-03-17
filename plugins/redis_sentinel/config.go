package redis_sentinel

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/cprobe/catpaw/config"
	tlscfg "github.com/cprobe/catpaw/pkg/tls"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

func (p *RedisSentinelPlugin) ApplyPartials() error {
	partialByID := make(map[string]Partial, len(p.Partials))
	for _, partial := range p.Partials {
		if partial.ID == "" {
			return fmt.Errorf("redis_sentinel partial id must not be empty")
		}
		if _, exists := partialByID[partial.ID]; exists {
			return fmt.Errorf("duplicate redis_sentinel partial id %q", partial.ID)
		}
		partialByID[partial.ID] = partial
	}

	for i := 0; i < len(p.Instances); i++ {
		id := p.Instances[i].Partial
		if id == "" {
			continue
		}
		partial, ok := partialByID[id]
		if !ok {
			return fmt.Errorf("redis_sentinel partial %q not found", id)
		}
		ins := p.Instances[i]
		if ins.Concurrency == 0 {
			ins.Concurrency = partial.Concurrency
		}
		if ins.Timeout == 0 {
			ins.Timeout = partial.Timeout
		}
		if ins.ReadTimeout == 0 {
			ins.ReadTimeout = partial.ReadTimeout
		}
		if ins.Username == "" {
			ins.Username = partial.Username
		}
		if ins.Password == "" {
			ins.Password = partial.Password
		}
		mergeClientConfig(&ins.ClientConfig, partial.ClientConfig)
		mergeSeverityCheck(&ins.Connectivity, partial.Connectivity)
		mergeSeverityCheck(&ins.Role, partial.Role)
		mergeOverviewCheck(&ins.MastersOverview, partial.MastersOverview)
		mergeSeverityCheck(&ins.CKQuorum, partial.CKQuorum)
		mergeSeverityCheck(&ins.MasterSDown, partial.MasterSDown)
		mergeSeverityCheck(&ins.MasterODown, partial.MasterODown)
		mergeSeverityCheck(&ins.MasterAddrResolution, partial.MasterAddrResolution)
		mergeThresholdCheck(&ins.PeerCount, partial.PeerCount)
		mergeThresholdCheck(&ins.KnownReplicas, partial.KnownReplicas)
		mergeThresholdCheck(&ins.KnownSentinels, partial.KnownSentinels)
		mergeSeverityCheck(&ins.FailoverInProgress, partial.FailoverInProgress)
		mergeSeverityCheck(&ins.Tilt, partial.Tilt)
	}

	return nil
}

func (p *RedisSentinelPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func (ins *Instance) Init() error {
	if ins.Concurrency == 0 {
		ins.Concurrency = 10
	}
	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(3 * time.Second)
	}
	if ins.ReadTimeout == 0 {
		ins.ReadTimeout = config.Duration(2 * time.Second)
	}
	if ins.Username != "" && ins.Password == "" {
		return fmt.Errorf("password must not be empty when username is set")
	}

	applySeverityDefaults(&ins.Connectivity, true, types.EventStatusCritical)
	applySeverityDefaults(&ins.Role, true, types.EventStatusCritical)
	applyOverviewDefaults(&ins.MastersOverview, true, types.EventStatusWarning)
	applySeverityDefaults(&ins.CKQuorum, true, types.EventStatusCritical)
	applySeverityDefaults(&ins.MasterSDown, true, types.EventStatusWarning)
	applySeverityDefaults(&ins.MasterODown, true, types.EventStatusCritical)
	applySeverityDefaults(&ins.MasterAddrResolution, true, types.EventStatusCritical)
	applyThresholdDefaults(&ins.PeerCount, false)
	applyThresholdDefaults(&ins.KnownReplicas, false)
	applyThresholdDefaults(&ins.KnownSentinels, false)
	applySeverityDefaults(&ins.FailoverInProgress, false, types.EventStatusWarning)
	applySeverityDefaults(&ins.Tilt, false, types.EventStatusWarning)

	for _, check := range []struct {
		name string
		cfg  SeverityCheck
	}{
		{"connectivity", ins.Connectivity},
		{"role", ins.Role},
		{"ckquorum", ins.CKQuorum},
		{"master_sdown", ins.MasterSDown},
		{"master_odown", ins.MasterODown},
		{"master_addr_resolution", ins.MasterAddrResolution},
		{"failover_in_progress", ins.FailoverInProgress},
		{"tilt", ins.Tilt},
	} {
		if check.cfg.Severity != "" && !types.EventStatusValid(check.cfg.Severity) {
			return fmt.Errorf("invalid %s.severity %q", check.name, check.cfg.Severity)
		}
	}
	if ins.MastersOverview.EmptySeverity != "" && !types.EventStatusValid(ins.MastersOverview.EmptySeverity) {
		return fmt.Errorf("invalid masters_overview.empty_severity %q", ins.MastersOverview.EmptySeverity)
	}

	if err := validateThresholdCheck("peer_count", ins.PeerCount); err != nil {
		return err
	}
	if err := validateThresholdCheck("known_replicas", ins.KnownReplicas); err != nil {
		return err
	}
	if err := validateThresholdCheck("known_sentinels", ins.KnownSentinels); err != nil {
		return err
	}

	seenTargets := make(map[string]struct{}, len(ins.Targets))
	for i := 0; i < len(ins.Targets); i++ {
		target, err := normalizeTarget(ins.Targets[i])
		if err != nil {
			return err
		}
		if _, exists := seenTargets[target]; exists {
			return fmt.Errorf("duplicate redis_sentinel target %q", target)
		}
		seenTargets[target] = struct{}{}
		ins.Targets[i] = target
	}

	seenMasters := make(map[string]struct{}, len(ins.Masters))
	for i := 0; i < len(ins.Masters); i++ {
		name := strings.TrimSpace(ins.Masters[i].Name)
		if name == "" {
			return fmt.Errorf("redis_sentinel master name must not be empty")
		}
		if _, exists := seenMasters[name]; exists {
			return fmt.Errorf("duplicate redis_sentinel master %q", name)
		}
		seenMasters[name] = struct{}{}
		ins.Masters[i].Name = name
	}

	tlsConfig, err := ins.ClientConfig.TLSConfig()
	if err != nil {
		return fmt.Errorf("failed to build redis_sentinel TLS config: %v", err)
	}
	ins.tlsConfig = tlsConfig

	return nil
}

func severityCheckEnabled(c SeverityCheck, defaultEnabled bool) bool {
	if c.Enabled == nil {
		return defaultEnabled
	}
	return *c.Enabled
}

func overviewCheckEnabled(c OverviewCheck, defaultEnabled bool) bool {
	if c.Enabled == nil {
		return defaultEnabled
	}
	return *c.Enabled
}

func thresholdCheckEnabled(c ThresholdCheck, defaultEnabled bool) bool {
	if c.Enabled == nil {
		return defaultEnabled
	}
	return *c.Enabled
}

func (ins *Instance) roleEnabled() bool { return severityCheckEnabled(ins.Role, true) }
func (ins *Instance) mastersOverviewEnabled() bool {
	return overviewCheckEnabled(ins.MastersOverview, true)
}
func (ins *Instance) ckquorumEnabled() bool    { return severityCheckEnabled(ins.CKQuorum, true) }
func (ins *Instance) masterSDownEnabled() bool { return severityCheckEnabled(ins.MasterSDown, true) }
func (ins *Instance) masterODownEnabled() bool { return severityCheckEnabled(ins.MasterODown, true) }
func (ins *Instance) masterAddrResolutionEnabled() bool {
	return severityCheckEnabled(ins.MasterAddrResolution, true)
}
func (ins *Instance) peerCountEnabled() bool { return thresholdCheckEnabled(ins.PeerCount, false) }
func (ins *Instance) knownReplicasEnabled() bool {
	return thresholdCheckEnabled(ins.KnownReplicas, false)
}
func (ins *Instance) knownSentinelsEnabled() bool {
	return thresholdCheckEnabled(ins.KnownSentinels, false)
}
func (ins *Instance) failoverInProgressEnabled() bool {
	return severityCheckEnabled(ins.FailoverInProgress, false)
}
func (ins *Instance) tiltEnabled() bool { return severityCheckEnabled(ins.Tilt, false) }

func applySeverityDefaults(c *SeverityCheck, enabled bool, severity string) {
	if c.Enabled == nil {
		c.Enabled = cloneBoolPtr(&enabled)
	}
	if c.Severity == "" {
		c.Severity = severity
	}
}

func applyOverviewDefaults(c *OverviewCheck, enabled bool, emptySeverity string) {
	if c.Enabled == nil {
		c.Enabled = cloneBoolPtr(&enabled)
	}
	if c.EmptySeverity == "" {
		c.EmptySeverity = emptySeverity
	}
}

func applyThresholdDefaults(c *ThresholdCheck, enabled bool) {
	if c.Enabled == nil {
		c.Enabled = cloneBoolPtr(&enabled)
	}
}

func mergeSeverityCheck(dst *SeverityCheck, src SeverityCheck) {
	if dst.Enabled == nil {
		dst.Enabled = cloneBoolPtr(src.Enabled)
	}
	if dst.Severity == "" {
		dst.Severity = src.Severity
	}
}

func mergeOverviewCheck(dst *OverviewCheck, src OverviewCheck) {
	if dst.Enabled == nil {
		dst.Enabled = cloneBoolPtr(src.Enabled)
	}
	if dst.EmptySeverity == "" {
		dst.EmptySeverity = src.EmptySeverity
	}
}

func mergeThresholdCheck(dst *ThresholdCheck, src ThresholdCheck) {
	if dst.Enabled == nil {
		dst.Enabled = cloneBoolPtr(src.Enabled)
	}
	if dst.WarnLt == 0 {
		dst.WarnLt = src.WarnLt
	}
	if dst.CriticalLt == 0 {
		dst.CriticalLt = src.CriticalLt
	}
}

func cloneBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func mergeClientConfig(dst *tlscfg.ClientConfig, src tlscfg.ClientConfig) {
	if dst.UseTLS == nil {
		dst.UseTLS = cloneBoolPtr(src.UseTLS)
	}
	if dst.TLSCA == "" {
		dst.TLSCA = src.TLSCA
	}
	if dst.TLSCert == "" {
		dst.TLSCert = src.TLSCert
	}
	if dst.TLSKey == "" {
		dst.TLSKey = src.TLSKey
	}
	if dst.TLSKeyPwd == "" {
		dst.TLSKeyPwd = src.TLSKeyPwd
	}
	if dst.InsecureSkipVerify == nil {
		dst.InsecureSkipVerify = cloneBoolPtr(src.InsecureSkipVerify)
	}
	if dst.ServerName == "" {
		dst.ServerName = src.ServerName
	}
	if dst.TLSMinVersion == "" {
		dst.TLSMinVersion = src.TLSMinVersion
	}
	if dst.TLSMaxVersion == "" {
		dst.TLSMaxVersion = src.TLSMaxVersion
	}
}

func validateThresholdCheck(name string, check ThresholdCheck) error {
	if !thresholdCheckEnabled(check, false) {
		return nil
	}
	if check.WarnLt < 0 || check.CriticalLt < 0 {
		return fmt.Errorf("%s thresholds must be >= 0", name)
	}
	if check.WarnLt == 0 && check.CriticalLt == 0 {
		return fmt.Errorf("%s requires warn_lt or critical_lt when enabled", name)
	}
	if check.WarnLt > 0 && check.CriticalLt > 0 && check.WarnLt <= check.CriticalLt {
		return fmt.Errorf("%s.warn_lt(%d) must be greater than %s.critical_lt(%d)",
			name, check.WarnLt, name, check.CriticalLt)
	}
	return nil
}

func normalizeTarget(raw string) (string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", fmt.Errorf("redis_sentinel target must not be empty")
	}
	host, port, err := net.SplitHostPort(target)
	if err == nil {
		if port == "" {
			return "", fmt.Errorf("bad port, target: %s", raw)
		}
		if host == "" {
			host = "localhost"
		}
		return net.JoinHostPort(host, port), nil
	}
	if strings.Contains(err.Error(), "missing port in address") {
		if strings.Count(target, ":") > 1 && !strings.HasPrefix(target, "[") {
			return "", fmt.Errorf("redis_sentinel IPv6 target must use [addr]:port format: %s", raw)
		}
		return net.JoinHostPort(target, defaultSentinelPort), nil
	}
	return "", fmt.Errorf("failed to parse redis_sentinel target %q: %v", raw, err)
}
