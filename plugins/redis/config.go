package redis

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

// This file owns Redis plugin configuration lifecycle:
// partial template merge, Init defaults, validation, and normalization helpers.

func (p *RedisPlugin) ApplyPartials() error {
	for i := 0; i < len(p.Instances); i++ {
		id := p.Instances[i].Partial
		if id == "" {
			continue
		}
		for _, partial := range p.Partials {
			if partial.ID != id {
				continue
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
			if ins.DB == 0 {
				ins.DB = partial.DB
			}
			if ins.Mode == "" {
				ins.Mode = partial.Mode
			}
			if ins.ClusterName == "" {
				ins.ClusterName = partial.ClusterName
			}
			mergeClientConfig(&ins.ClientConfig, partial.ClientConfig)
			mergeConnectivityCheck(&ins.Connectivity, partial.Connectivity)
			mergeResponseTimeCheck(&ins.ResponseTime, partial.ResponseTime)
			mergeRoleCheck(&ins.Role, partial.Role)
			mergeReplLagCheck(&ins.ReplLag, partial.ReplLag)
			mergeCountCheck(&ins.ConnectedClients, partial.ConnectedClients)
			mergeCountCheck(&ins.BlockedClients, partial.BlockedClients)
			mergeMemoryUsageCheck(&ins.UsedMemory, partial.UsedMemory)
			mergePercentCheck(&ins.UsedMemoryPct, partial.UsedMemoryPct)
			mergeCountCheck(&ins.RejectedConn, partial.RejectedConn)
			mergeMasterLinkCheck(&ins.MasterLink, partial.MasterLink)
			mergeMinCountCheck(&ins.ConnectedSlaves, partial.ConnectedSlaves)
			mergeCountCheck(&ins.EvictedKeys, partial.EvictedKeys)
			mergeCountCheck(&ins.ExpiredKeys, partial.ExpiredKeys)
			mergeOpsPerSecondCheck(&ins.OpsPerSecond, partial.OpsPerSecond)
			mergePersistenceCheck(&ins.Persistence, partial.Persistence)
			mergeClusterStateCheck(&ins.ClusterState, partial.ClusterState)
			mergeClusterTopologyCheck(&ins.ClusterTopology, partial.ClusterTopology)
			break
		}
	}
	return nil
}

func mergeConnectivityCheck(dst *ConnectivityCheck, src ConnectivityCheck) {
	if dst.Severity == "" {
		dst.Severity = src.Severity
	}
}

func mergeResponseTimeCheck(dst *ResponseTimeCheck, src ResponseTimeCheck) {
	if dst.WarnGe == 0 {
		dst.WarnGe = src.WarnGe
	}
	if dst.CriticalGe == 0 {
		dst.CriticalGe = src.CriticalGe
	}
}

func mergeRoleCheck(dst *RoleCheck, src RoleCheck) {
	if dst.Expect == "" {
		dst.Expect = src.Expect
	}
	if dst.Severity == "" {
		dst.Severity = src.Severity
	}
}

func mergeReplLagCheck(dst *ReplLagCheck, src ReplLagCheck) {
	if dst.WarnGe == 0 {
		dst.WarnGe = src.WarnGe
	}
	if dst.CriticalGe == 0 {
		dst.CriticalGe = src.CriticalGe
	}
}

func mergeCountCheck(dst *CountCheck, src CountCheck) {
	if dst.WarnGe == 0 {
		dst.WarnGe = src.WarnGe
	}
	if dst.CriticalGe == 0 {
		dst.CriticalGe = src.CriticalGe
	}
}

func mergeMinCountCheck(dst *MinCountCheck, src MinCountCheck) {
	if dst.WarnLt == 0 {
		dst.WarnLt = src.WarnLt
	}
	if dst.CriticalLt == 0 {
		dst.CriticalLt = src.CriticalLt
	}
}

func mergeMemoryUsageCheck(dst *MemoryUsageCheck, src MemoryUsageCheck) {
	if dst.WarnGe == 0 {
		dst.WarnGe = src.WarnGe
	}
	if dst.CriticalGe == 0 {
		dst.CriticalGe = src.CriticalGe
	}
}

func mergePercentCheck(dst *PercentCheck, src PercentCheck) {
	if dst.WarnGe == 0 {
		dst.WarnGe = src.WarnGe
	}
	if dst.CriticalGe == 0 {
		dst.CriticalGe = src.CriticalGe
	}
}

func mergeMasterLinkCheck(dst *MasterLinkCheck, src MasterLinkCheck) {
	if dst.Expect == "" {
		dst.Expect = src.Expect
	}
	if dst.Severity == "" {
		dst.Severity = src.Severity
	}
}

func mergeOpsPerSecondCheck(dst *OpsPerSecondCheck, src OpsPerSecondCheck) {
	if dst.WarnGe == 0 {
		dst.WarnGe = src.WarnGe
	}
	if dst.CriticalGe == 0 {
		dst.CriticalGe = src.CriticalGe
	}
}

func mergePersistenceCheck(dst *PersistenceCheck, src PersistenceCheck) {
	if !dst.Enabled {
		dst.Enabled = src.Enabled
	}
	if dst.Severity == "" {
		dst.Severity = src.Severity
	}
}

func mergeClusterStateCheck(dst *ClusterStateCheck, src ClusterStateCheck) {
	if dst.Disabled == nil {
		dst.Disabled = cloneBoolPtr(src.Disabled)
	}
	if dst.Severity == "" {
		dst.Severity = src.Severity
	}
}

func mergeClusterTopologyCheck(dst *ClusterTopologyCheck, src ClusterTopologyCheck) {
	if dst.Disabled == nil {
		dst.Disabled = cloneBoolPtr(src.Disabled)
	}
}

func cloneBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func (p *RedisPlugin) GetInstances() []plugins.Instance {
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
	mode, err := normalizeRedisMode(ins.Mode)
	if err != nil {
		return err
	}
	ins.Mode = mode
	if ins.Connectivity.Severity == "" {
		ins.Connectivity.Severity = types.EventStatusCritical
	} else if !types.EventStatusValid(ins.Connectivity.Severity) {
		return fmt.Errorf("invalid connectivity.severity %q", ins.Connectivity.Severity)
	}
	if ins.ResponseTime.WarnGe > 0 && ins.ResponseTime.CriticalGe > 0 && ins.ResponseTime.WarnGe >= ins.ResponseTime.CriticalGe {
		return fmt.Errorf("response_time.warn_ge(%s) must be less than response_time.critical_ge(%s)",
			time.Duration(ins.ResponseTime.WarnGe), time.Duration(ins.ResponseTime.CriticalGe))
	}

	roleExpect, err := normalizeRole(ins.Role.Expect)
	if err != nil {
		return err
	}
	ins.Role.Expect = roleExpect
	if ins.Role.Expect != "" {
		if ins.Role.Severity == "" {
			ins.Role.Severity = types.EventStatusWarning
		} else if !types.EventStatusValid(ins.Role.Severity) {
			return fmt.Errorf("invalid role.severity %q", ins.Role.Severity)
		}
	}
	if ins.ReplLag.WarnGe < 0 || ins.ReplLag.CriticalGe < 0 {
		return fmt.Errorf("repl_lag thresholds must be >= 0")
	}
	if ins.ReplLag.WarnGe > 0 && ins.ReplLag.CriticalGe > 0 && ins.ReplLag.WarnGe >= ins.ReplLag.CriticalGe {
		return fmt.Errorf("repl_lag.warn_ge(%s) must be less than repl_lag.critical_ge(%s)",
			ins.ReplLag.WarnGe.String(), ins.ReplLag.CriticalGe.String())
	}

	if err := validateCountCheck("connected_clients", ins.ConnectedClients); err != nil {
		return err
	}
	if err := validateCountCheck("blocked_clients", ins.BlockedClients); err != nil {
		return err
	}
	if err := validateCountCheck("rejected_connections", ins.RejectedConn); err != nil {
		return err
	}
	if err := validateMinCountCheck("connected_slaves", ins.ConnectedSlaves); err != nil {
		return err
	}
	if err := validateCountCheck("evicted_keys", ins.EvictedKeys); err != nil {
		return err
	}
	if err := validateCountCheck("expired_keys", ins.ExpiredKeys); err != nil {
		return err
	}
	if err := validateCountCheck("instantaneous_ops_per_sec", CountCheck{
		WarnGe:     ins.OpsPerSecond.WarnGe,
		CriticalGe: ins.OpsPerSecond.CriticalGe,
	}); err != nil {
		return err
	}
	if ins.UsedMemory.WarnGe < 0 || ins.UsedMemory.CriticalGe < 0 {
		return fmt.Errorf("used_memory thresholds must be >= 0")
	}
	if ins.UsedMemory.WarnGe > 0 && ins.UsedMemory.CriticalGe > 0 && ins.UsedMemory.WarnGe >= ins.UsedMemory.CriticalGe {
		return fmt.Errorf("used_memory.warn_ge(%s) must be less than used_memory.critical_ge(%s)",
			ins.UsedMemory.WarnGe.String(), ins.UsedMemory.CriticalGe.String())
	}
	if err := validatePercentCheck("used_memory_pct", ins.UsedMemoryPct); err != nil {
		return err
	}
	masterLinkExpect, err := normalizeMasterLinkStatus(ins.MasterLink.Expect)
	if err != nil {
		return err
	}
	ins.MasterLink.Expect = masterLinkExpect
	if ins.MasterLink.Expect != "" {
		if ins.MasterLink.Severity == "" {
			ins.MasterLink.Severity = types.EventStatusWarning
		} else if !types.EventStatusValid(ins.MasterLink.Severity) {
			return fmt.Errorf("invalid master_link_status.severity %q", ins.MasterLink.Severity)
		}
	}
	if ins.Persistence.Enabled {
		if ins.Persistence.Severity == "" {
			ins.Persistence.Severity = types.EventStatusCritical
		} else if !types.EventStatusValid(ins.Persistence.Severity) {
			return fmt.Errorf("invalid persistence.severity %q", ins.Persistence.Severity)
		}
	}
	if ins.Mode != redisModeStandalone && ins.clusterStateEnabled() {
		if ins.ClusterState.Severity == "" {
			ins.ClusterState.Severity = types.EventStatusCritical
		} else if !types.EventStatusValid(ins.ClusterState.Severity) {
			return fmt.Errorf("invalid cluster_state.severity %q", ins.ClusterState.Severity)
		}
	}
	if ins.Username != "" && ins.Password == "" {
		return fmt.Errorf("password must not be empty when username is set")
	}
	if ins.DB < 0 {
		return fmt.Errorf("db must be >= 0 (got %d)", ins.DB)
	}

	for i := 0; i < len(ins.Targets); i++ {
		target, err := normalizeTarget(ins.Targets[i])
		if err != nil {
			return err
		}
		ins.Targets[i] = target
	}

	tlsConfig, err := ins.ClientConfig.TLSConfig()
	if err != nil {
		return fmt.Errorf("failed to build redis TLS config: %v", err)
	}
	ins.tlsConfig = tlsConfig
	if ins.prevStats == nil {
		ins.prevStats = make(map[string]redisCounterSnapshot)
	}
	if ins.initialized == nil {
		ins.initialized = make(map[string]bool)
	}

	return nil
}

func (ins *Instance) clusterStateEnabled() bool {
	return ins.ClusterState.Disabled == nil || !*ins.ClusterState.Disabled
}

func (ins *Instance) clusterTopologyEnabled() bool {
	return ins.ClusterTopology.Disabled == nil || !*ins.ClusterTopology.Disabled
}

func normalizeMasterLinkStatus(status string) (string, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "":
		return "", nil
	case "up", "down":
		return status, nil
	default:
		return "", fmt.Errorf("invalid master_link_status.expect %q, must be one of: up, down", status)
	}
}

func normalizeRedisMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", redisModeAuto:
		return redisModeAuto, nil
	case redisModeStandalone:
		return redisModeStandalone, nil
	case redisModeCluster:
		return redisModeCluster, nil
	default:
		return "", fmt.Errorf("invalid mode %q, must be one of: auto, standalone, cluster", mode)
	}
}

func mergeClientConfig(dst *tlscfg.ClientConfig, src tlscfg.ClientConfig) {
	if !dst.UseTLS {
		dst.UseTLS = src.UseTLS
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
	if !dst.InsecureSkipVerify {
		dst.InsecureSkipVerify = src.InsecureSkipVerify
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

func validateCountCheck(name string, check CountCheck) error {
	if check.WarnGe < 0 || check.CriticalGe < 0 {
		return fmt.Errorf("%s thresholds must be >= 0", name)
	}
	if check.WarnGe > 0 && check.CriticalGe > 0 && check.WarnGe >= check.CriticalGe {
		return fmt.Errorf("%s.warn_ge(%d) must be less than %s.critical_ge(%d)",
			name, check.WarnGe, name, check.CriticalGe)
	}
	return nil
}

func validateMinCountCheck(name string, check MinCountCheck) error {
	if check.WarnLt < 0 || check.CriticalLt < 0 {
		return fmt.Errorf("%s thresholds must be >= 0", name)
	}
	if check.WarnLt > 0 && check.CriticalLt > 0 && check.WarnLt <= check.CriticalLt {
		return fmt.Errorf("%s.warn_lt(%d) must be greater than %s.critical_lt(%d)",
			name, check.WarnLt, name, check.CriticalLt)
	}
	return nil
}

func validatePercentCheck(name string, check PercentCheck) error {
	if check.WarnGe < 0 || check.CriticalGe < 0 {
		return fmt.Errorf("%s thresholds must be >= 0", name)
	}
	if check.WarnGe > 100 || check.CriticalGe > 100 {
		return fmt.Errorf("%s thresholds must be <= 100", name)
	}
	if check.WarnGe > 0 && check.CriticalGe > 0 && check.WarnGe >= check.CriticalGe {
		return fmt.Errorf("%s.warn_ge(%d) must be less than %s.critical_ge(%d)",
			name, check.WarnGe, name, check.CriticalGe)
	}
	return nil
}

func normalizeRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "":
		return "", nil
	case "master":
		return "master", nil
	case "slave", "replica":
		return "slave", nil
	default:
		return "", fmt.Errorf("invalid role.expect %q, must be one of: master, slave, replica", role)
	}
}

func normalizeTarget(raw string) (string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", fmt.Errorf("redis target must not be empty")
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
			return "", fmt.Errorf("redis IPv6 target must use [addr]:port format: %s", raw)
		}
		return net.JoinHostPort(target, defaultRedisPort), nil
	}

	return "", fmt.Errorf("failed to parse redis target %q: %v", raw, err)
}
