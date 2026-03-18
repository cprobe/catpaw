package redis

import (
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/digcore/config"
	"github.com/cprobe/digcore/logger"
	"github.com/cprobe/digcore/pkg/conv"
	"github.com/cprobe/digcore/pkg/safe"
	tlscfg "github.com/cprobe/digcore/pkg/tls"
	"github.com/cprobe/digcore/plugins"
	"github.com/cprobe/digcore/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const (
	pluginName       = "redis"
	defaultRedisPort = "6379"
	maxBulkSize      = 1 << 20 // 1MB, prevent unbounded allocation from malformed replies
)

type ConnectivityCheck struct {
	Severity string `toml:"severity"`
}

type ResponseTimeCheck struct {
	WarnGe     config.Duration `toml:"warn_ge"`
	CriticalGe config.Duration `toml:"critical_ge"`
}

type RoleCheck struct {
	Expect   string `toml:"expect"`
	Severity string `toml:"severity"`
}

type CountCheck struct {
	WarnGe     int `toml:"warn_ge"`
	CriticalGe int `toml:"critical_ge"`
}

type MinCountCheck struct {
	WarnLt     int `toml:"warn_lt"`
	CriticalLt int `toml:"critical_lt"`
}

type MemoryUsageCheck struct {
	WarnGe     config.Size `toml:"warn_ge"`
	CriticalGe config.Size `toml:"critical_ge"`
}

type MasterLinkCheck struct {
	Expect   string `toml:"expect"`
	Severity string `toml:"severity"`
}

type PersistenceCheck struct {
	Enabled  bool   `toml:"enabled"`
	Severity string `toml:"severity"`
}

type OpsPerSecondCheck struct {
	WarnGe     int `toml:"warn_ge"`
	CriticalGe int `toml:"critical_ge"`
}

type Partial struct {
	ID          string          `toml:"id"`
	Concurrency int             `toml:"concurrency"`
	Timeout     config.Duration `toml:"timeout"`
	ReadTimeout config.Duration `toml:"read_timeout"`
	Username    string          `toml:"username"`
	Password    string          `toml:"password"`
	DB          int             `toml:"db"`
	tlscfg.ClientConfig
	Connectivity     ConnectivityCheck `toml:"connectivity"`
	ResponseTime     ResponseTimeCheck `toml:"response_time"`
	Role             RoleCheck         `toml:"role"`
	ConnectedClients CountCheck        `toml:"connected_clients"`
	BlockedClients   CountCheck        `toml:"blocked_clients"`
	UsedMemory       MemoryUsageCheck  `toml:"used_memory"`
	RejectedConn     CountCheck        `toml:"rejected_connections"`
	MasterLink       MasterLinkCheck   `toml:"master_link_status"`
	ConnectedSlaves  MinCountCheck     `toml:"connected_slaves"`
	EvictedKeys      CountCheck        `toml:"evicted_keys"`
	ExpiredKeys      CountCheck        `toml:"expired_keys"`
	OpsPerSecond     OpsPerSecondCheck `toml:"instantaneous_ops_per_sec"`
	Persistence      PersistenceCheck  `toml:"persistence"`
}

type Instance struct {
	config.InternalConfig
	Partial string `toml:"partial"`

	Targets          []string          `toml:"targets"`
	Concurrency      int               `toml:"concurrency"`
	Timeout          config.Duration   `toml:"timeout"`
	ReadTimeout      config.Duration   `toml:"read_timeout"`
	Username         string            `toml:"username"`
	Password         string            `toml:"password"`
	DB               int               `toml:"db"`
	Connectivity     ConnectivityCheck `toml:"connectivity"`
	ResponseTime     ResponseTimeCheck `toml:"response_time"`
	Role             RoleCheck         `toml:"role"`
	ConnectedClients CountCheck        `toml:"connected_clients"`
	BlockedClients   CountCheck        `toml:"blocked_clients"`
	UsedMemory       MemoryUsageCheck  `toml:"used_memory"`
	RejectedConn     CountCheck        `toml:"rejected_connections"`
	MasterLink       MasterLinkCheck   `toml:"master_link_status"`
	ConnectedSlaves  MinCountCheck     `toml:"connected_slaves"`
	EvictedKeys      CountCheck        `toml:"evicted_keys"`
	ExpiredKeys      CountCheck        `toml:"expired_keys"`
	OpsPerSecond     OpsPerSecondCheck `toml:"instantaneous_ops_per_sec"`
	Persistence      PersistenceCheck  `toml:"persistence"`

	tlscfg.ClientConfig
	tlsConfig *tls.Config
	dialFunc  func(network, address string) (net.Conn, error)

	statsMu     sync.Mutex
	prevStats   map[string]redisCounterSnapshot
	initialized map[string]bool
}

type redisCounterSnapshot struct {
	evictedKeys  uint64
	expiredKeys  uint64
	rejectedConn uint64
}

type RedisPlugin struct {
	config.InternalConfig
	Partials  []Partial   `toml:"partials"`
	Instances []*Instance `toml:"instances"`
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &RedisPlugin{}
	})
}

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
			mergeClientConfig(&ins.ClientConfig, partial.ClientConfig)
			mergeConnectivityCheck(&ins.Connectivity, partial.Connectivity)
			mergeResponseTimeCheck(&ins.ResponseTime, partial.ResponseTime)
			mergeRoleCheck(&ins.Role, partial.Role)
			mergeCountCheck(&ins.ConnectedClients, partial.ConnectedClients)
			mergeCountCheck(&ins.BlockedClients, partial.BlockedClients)
			mergeMemoryUsageCheck(&ins.UsedMemory, partial.UsedMemory)
			mergeCountCheck(&ins.RejectedConn, partial.RejectedConn)
			mergeMasterLinkCheck(&ins.MasterLink, partial.MasterLink)
			mergeMinCountCheck(&ins.ConnectedSlaves, partial.ConnectedSlaves)
			mergeCountCheck(&ins.EvictedKeys, partial.EvictedKeys)
			mergeCountCheck(&ins.ExpiredKeys, partial.ExpiredKeys)
			mergeOpsPerSecondCheck(&ins.OpsPerSecond, partial.OpsPerSecond)
			mergePersistenceCheck(&ins.Persistence, partial.Persistence)
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

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if len(ins.Targets) == 0 {
		return
	}

	wg := new(sync.WaitGroup)
	se := semaphore.NewSemaphore(ins.Concurrency)
	for _, target := range ins.Targets {
		wg.Add(1)
		go func(target string) {
			se.Acquire()
			defer func() {
				if r := recover(); r != nil {
					logger.Logger.Errorw("panic in redis gather goroutine", "target", target, "recover", r)
					q.PushFront(types.BuildEvent(map[string]string{
						"check":  "redis::connectivity",
						"target": target,
					}).SetEventStatus(types.EventStatusCritical).
						SetDescription(fmt.Sprintf("panic during check: %v", r)))
				}
				se.Release()
				wg.Done()
			}()
			ins.gatherTarget(q, target)
		}(target)
	}

	perTarget := time.Duration(ins.Timeout) + time.Duration(ins.ReadTimeout)*8
	batches := (len(ins.Targets) + ins.Concurrency - 1) / ins.Concurrency
	gatherTimeout := perTarget * time.Duration(batches+1)
	if gatherTimeout < 30*time.Second {
		gatherTimeout = 30 * time.Second
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(gatherTimeout):
		logger.Logger.Errorw("redis gather timeout, some targets may still be running",
			"timeout", gatherTimeout, "targets", len(ins.Targets))
	}
}

func (ins *Instance) newAccessor(target string) (*RedisAccessor, error) {
	return NewRedisAccessor(RedisAccessorConfig{
		Target:      target,
		Username:    ins.Username,
		Password:    ins.Password,
		DB:          ins.DB,
		Timeout:     time.Duration(ins.Timeout),
		ReadTimeout: time.Duration(ins.ReadTimeout),
		TLSConfig:   ins.tlsConfig,
		DialFunc:    ins.dialFunc,
	})
}

func (ins *Instance) gatherTarget(q *safe.Queue[*types.Event], target string) {
	connEvent := ins.newEvent("redis::connectivity", target)
	start := time.Now()

	acc, err := ins.newAccessor(target)
	if err != nil {
		connEvent.SetAttrs(map[string]string{
			"response_time":   time.Since(start).String(),
			"threshold_desc": fmt.Sprintf("%s: redis ping failed", ins.Connectivity.Severity),
		})
		q.PushFront(connEvent.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("redis ping failed: %v", err)))
		return
	}
	defer acc.Close()

	if err := acc.Ping(); err != nil {
		connEvent.SetAttrs(map[string]string{
			"response_time":   time.Since(start).String(),
			"threshold_desc": fmt.Sprintf("%s: redis ping failed", ins.Connectivity.Severity),
		})
		q.PushFront(connEvent.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("redis ping failed: %v", err)))
		return
	}

	responseTime := time.Since(start)
	connEvent.SetAttrs(map[string]string{
		"response_time":   responseTime.String(),
		"threshold_desc": fmt.Sprintf("%s: redis ping failed", ins.Connectivity.Severity),
	})
	q.PushFront(connEvent.SetDescription("redis ping ok"))

	ins.checkResponseTime(q, target, responseTime)

	infoCache := make(map[string]map[string]string)
	infoSection := func(section string) (map[string]string, error) {
		if info, ok := infoCache[section]; ok {
			return info, nil
		}
		info, err := acc.Info(section)
		if err != nil {
			return nil, err
		}
		infoCache[section] = info
		return info, nil
	}

	if ins.Role.Expect != "" {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::role", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis replication info: %v", err)))
		} else {
			ins.checkRole(q, target, info)
		}
	}

	if ins.MasterLink.Expect != "" {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::master_link_status", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis replication info: %v", err)))
		} else {
			ins.checkMasterLink(q, target, info)
		}
	}

	if ins.ConnectedSlaves.WarnLt > 0 || ins.ConnectedSlaves.CriticalLt > 0 {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::connected_slaves", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis replication info: %v", err)))
		} else {
			ins.checkMinCountFromInfo(q, target, "redis::connected_slaves", info, "connected_slaves",
				ins.ConnectedSlaves, "connected slaves")
		}
	}

	if ins.ConnectedClients.WarnGe > 0 || ins.ConnectedClients.CriticalGe > 0 || ins.BlockedClients.WarnGe > 0 || ins.BlockedClients.CriticalGe > 0 {
		info, err := infoSection("clients")
		if err != nil {
			if ins.ConnectedClients.WarnGe > 0 || ins.ConnectedClients.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::connected_clients", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis client info: %v", err)))
			}
			if ins.BlockedClients.WarnGe > 0 || ins.BlockedClients.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::blocked_clients", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis client info: %v", err)))
			}
		} else {
			if ins.ConnectedClients.WarnGe > 0 || ins.ConnectedClients.CriticalGe > 0 {
				ins.checkCountFromInfo(q, target, "redis::connected_clients", info, "connected_clients",
					ins.ConnectedClients, "connected clients")
			}
			if ins.BlockedClients.WarnGe > 0 || ins.BlockedClients.CriticalGe > 0 {
				ins.checkCountFromInfo(q, target, "redis::blocked_clients", info, "blocked_clients",
					ins.BlockedClients, "blocked clients")
			}
		}
	}

	if ins.UsedMemory.WarnGe > 0 || ins.UsedMemory.CriticalGe > 0 {
		info, err := infoSection("memory")
		if err != nil {
			q.PushFront(ins.newEvent("redis::used_memory", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis memory info: %v", err)))
		} else {
			ins.checkUsedMemory(q, target, info)
		}
	}

	needStats := (ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0) ||
		(ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0) ||
		(ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0) ||
		(ins.OpsPerSecond.WarnGe > 0 || ins.OpsPerSecond.CriticalGe > 0)
	if needStats {
		info, err := infoSection("stats")
		if err != nil {
			if ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::rejected_connections", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
			if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::evicted_keys", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
			if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::expired_keys", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
			if ins.OpsPerSecond.WarnGe > 0 || ins.OpsPerSecond.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::instantaneous_ops_per_sec", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
		} else {
			ins.checkCounterDeltas(q, target, info)
			if ins.OpsPerSecond.WarnGe > 0 || ins.OpsPerSecond.CriticalGe > 0 {
				ins.checkCountFromInfo(q, target, "redis::instantaneous_ops_per_sec", info, "instantaneous_ops_per_sec",
					CountCheck{
						WarnGe:     ins.OpsPerSecond.WarnGe,
						CriticalGe: ins.OpsPerSecond.CriticalGe,
					},
					"instantaneous ops per second")
			}
		}
	}

	if ins.Persistence.Enabled {
		info, err := infoSection("persistence")
		if err != nil {
			q.PushFront(ins.newEvent("redis::persistence", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis persistence info: %v", err)))
		} else {
			ins.checkPersistence(q, target, info)
		}
	}
}


func (ins *Instance) checkResponseTime(q *safe.Queue[*types.Event], target string, responseTime time.Duration) {
	if ins.ResponseTime.WarnGe == 0 && ins.ResponseTime.CriticalGe == 0 {
		return
	}

	var parts []string
	if ins.ResponseTime.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning ≥ %s", time.Duration(ins.ResponseTime.WarnGe).String()))
	}
	if ins.ResponseTime.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical ≥ %s", time.Duration(ins.ResponseTime.CriticalGe).String()))
	}
	attrs := map[string]string{
		"response_time":   responseTime.String(),
		"threshold_desc": strings.Join(parts, ", "),
	}
	event := ins.newEvent("redis::response_time", target).SetAttrs(attrs).SetCurrentValue(responseTime.String())

	status := types.EvaluateGeThreshold(float64(responseTime), float64(ins.ResponseTime.WarnGe), float64(ins.ResponseTime.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis response time %s >= critical threshold %s",
				responseTime, time.Duration(ins.ResponseTime.CriticalGe))))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis response time %s >= warning threshold %s",
				responseTime, time.Duration(ins.ResponseTime.WarnGe))))
	default:
		q.PushFront(event.SetDescription(fmt.Sprintf("redis response time %s, everything is ok", responseTime)))
	}
}

func (ins *Instance) checkRole(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::role", target)
	actual, ok := info["role"]
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis info output missing role"))
		return
	}

	actual = strings.ToLower(strings.TrimSpace(actual))
	event.SetAttrs(map[string]string{
		"actual":         actual,
		"expect":         ins.Role.Expect,
		"threshold_desc": fmt.Sprintf("%s: role ≠ %s", ins.Role.Severity, ins.Role.Expect),
	}).SetCurrentValue(actual)

	if actual == ins.Role.Expect {
		q.PushFront(event.SetDescription(fmt.Sprintf("redis role is %s, matches expectation", actual)))
		return
	}

	q.PushFront(event.SetEventStatus(ins.Role.Severity).
		SetDescription(fmt.Sprintf("redis role is %s, expected %s", actual, ins.Role.Expect)))
}

func (ins *Instance) checkMasterLink(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::master_link_status", target)
	attrs := map[string]string{
		"threshold_desc": fmt.Sprintf("%s: master link status does not match expected", ins.MasterLink.Severity),
	}
	if role, ok := info["role"]; ok {
		attrs["role"] = role
	}
	actual, ok := info["master_link_status"]
	if !ok {
		q.PushFront(event.SetEventStatus(ins.MasterLink.Severity).
			SetDescription("redis replication info missing master_link_status"))
		return
	}

	actual = strings.ToLower(strings.TrimSpace(actual))
	attrs["actual"] = actual
	attrs["expect"] = ins.MasterLink.Expect
	if v, ok := info["master_host"]; ok && v != "" {
		attrs["master_host"] = v
	}
	if v, ok := info["master_port"]; ok && v != "" {
		attrs["master_port"] = v
	}
	event.SetAttrs(attrs).SetCurrentValue(actual)

	if actual == ins.MasterLink.Expect {
		q.PushFront(event.SetDescription(fmt.Sprintf("redis master link status is %s, matches expectation", actual)))
		return
	}

	q.PushFront(event.SetEventStatus(ins.MasterLink.Severity).
		SetDescription(fmt.Sprintf("redis master link status is %s, expected %s", actual, ins.MasterLink.Expect)))
}

func (ins *Instance) checkCount(q *safe.Queue[*types.Event], target, check string, value int, thresholds CountCheck, metricName string) {
	labelKey := strings.TrimPrefix(check, "redis::")
	var parts []string
	if thresholds.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning ≥ %d", thresholds.WarnGe))
	}
	if thresholds.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical ≥ %d", thresholds.CriticalGe))
	}
	attrs := map[string]string{
		labelKey:         strconv.Itoa(value),
		"threshold_desc": strings.Join(parts, ", "),
	}
	event := ins.newEvent(check, target).SetAttrs(attrs).SetCurrentValue(strconv.Itoa(value))

	status := types.EvaluateGeThreshold(float64(value), float64(thresholds.WarnGe), float64(thresholds.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis %s %d >= critical threshold %d", metricName, value, thresholds.CriticalGe)))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis %s %d >= warning threshold %d", metricName, value, thresholds.WarnGe)))
	default:
		q.PushFront(event.SetDescription(fmt.Sprintf("redis %s %d, everything is ok", metricName, value)))
	}
}

func (ins *Instance) checkCountFromInfo(q *safe.Queue[*types.Event], target, check string, info map[string]string, key string, thresholds CountCheck, metricName string) {
	value, ok, err := infoGetInt(info, key)
	if err != nil {
		q.PushFront(ins.newEvent(check, target).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis %s: %v", key, err)))
		return
	}
	if !ok {
		q.PushFront(ins.newEvent(check, target).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis info output missing %s", key)))
		return
	}
	ins.checkCount(q, target, check, value, thresholds, metricName)
}

func (ins *Instance) checkMinCountFromInfo(q *safe.Queue[*types.Event], target, check string, info map[string]string, key string, thresholds MinCountCheck, metricName string) {
	value, ok, err := infoGetInt(info, key)
	if err != nil {
		q.PushFront(ins.newEvent(check, target).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis %s: %v", key, err)))
		return
	}
	if !ok {
		q.PushFront(ins.newEvent(check, target).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis info output missing %s", key)))
		return
	}
	ins.checkMinCount(q, target, check, value, thresholds, metricName)
}

func (ins *Instance) checkMinCount(q *safe.Queue[*types.Event], target, check string, value int, thresholds MinCountCheck, metricName string) {
	labelKey := strings.TrimPrefix(check, "redis::")
	var parts []string
	if thresholds.WarnLt > 0 {
		parts = append(parts, fmt.Sprintf("Warning < %d", thresholds.WarnLt))
	}
	if thresholds.CriticalLt > 0 {
		parts = append(parts, fmt.Sprintf("Critical < %d", thresholds.CriticalLt))
	}
	attrs := map[string]string{
		labelKey:         strconv.Itoa(value),
		"threshold_desc": strings.Join(parts, ", "),
	}
	event := ins.newEvent(check, target).SetAttrs(attrs).SetCurrentValue(strconv.Itoa(value))

	if thresholds.CriticalLt > 0 && value < thresholds.CriticalLt {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis %s %d < critical threshold %d", metricName, value, thresholds.CriticalLt)))
		return
	}
	if thresholds.WarnLt > 0 && value < thresholds.WarnLt {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis %s %d < warning threshold %d", metricName, value, thresholds.WarnLt)))
		return
	}
	q.PushFront(event.SetDescription(fmt.Sprintf("redis %s %d, everything is ok", metricName, value)))
}

func (ins *Instance) checkCounterDeltas(q *safe.Queue[*types.Event], target string, info map[string]string) {
	var (
		evicted  uint64
		expired  uint64
		rejected uint64
	)

	if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
		value, ok, err := infoGetUint64(info, "evicted_keys")
		if err != nil {
			q.PushFront(ins.newEvent("redis::evicted_keys", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to parse redis evicted_keys: %v", err)))
			return
		}
		if !ok {
			q.PushFront(ins.newEvent("redis::evicted_keys", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription("redis info output missing evicted_keys"))
			return
		}
		evicted = value
	}

	if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
		value, ok, err := infoGetUint64(info, "expired_keys")
		if err != nil {
			q.PushFront(ins.newEvent("redis::expired_keys", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to parse redis expired_keys: %v", err)))
			return
		}
		if !ok {
			q.PushFront(ins.newEvent("redis::expired_keys", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription("redis info output missing expired_keys"))
			return
		}
		expired = value
	}

	if ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0 {
		value, ok, err := infoGetUint64(info, "rejected_connections")
		if err != nil {
			q.PushFront(ins.newEvent("redis::rejected_connections", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to parse redis rejected_connections: %v", err)))
			return
		}
		if !ok {
			q.PushFront(ins.newEvent("redis::rejected_connections", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription("redis info output missing rejected_connections"))
			return
		}
		rejected = value
	}

	ins.statsMu.Lock()
	prev := ins.prevStats[target]
	initialized := ins.initialized[target]
	ins.prevStats[target] = redisCounterSnapshot{
		evictedKeys:  evicted,
		expiredKeys:  expired,
		rejectedConn: rejected,
	}
	ins.initialized[target] = true
	ins.statsMu.Unlock()

	if !initialized {
		if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
			event := ins.newEvent("redis::evicted_keys", target).SetAttrs(map[string]string{
				"delta": "0",
				"total": strconv.FormatUint(evicted, 10),
			})
			q.PushFront(event.SetDescription(fmt.Sprintf("redis evicted keys baseline established (total: %d)", evicted)))
		}
		if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
			event := ins.newEvent("redis::expired_keys", target).SetAttrs(map[string]string{
				"delta": "0",
				"total": strconv.FormatUint(expired, 10),
			})
			q.PushFront(event.SetDescription(fmt.Sprintf("redis expired keys baseline established (total: %d)", expired)))
		}
		if ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0 {
			event := ins.newEvent("redis::rejected_connections", target).SetAttrs(map[string]string{
				"delta": "0",
				"total": strconv.FormatUint(rejected, 10),
			})
			q.PushFront(event.SetDescription(fmt.Sprintf("redis rejected connections baseline established (total: %d)", rejected)))
		}
		return
	}

	if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
		delta := uint64(0)
		if evicted >= prev.evictedKeys {
			delta = evicted - prev.evictedKeys
		}
		ins.checkDeltaCount(q, target, "redis::evicted_keys", delta, evicted, ins.EvictedKeys, "evicted keys")
	}

	if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
		delta := uint64(0)
		if expired >= prev.expiredKeys {
			delta = expired - prev.expiredKeys
		}
		ins.checkDeltaCount(q, target, "redis::expired_keys", delta, expired, ins.ExpiredKeys, "expired keys")
	}

	if ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0 {
		delta := uint64(0)
		if rejected >= prev.rejectedConn {
			delta = rejected - prev.rejectedConn
		}
		ins.checkDeltaCount(q, target, "redis::rejected_connections", delta, rejected, ins.RejectedConn, "rejected connections")
	}
}

func (ins *Instance) checkDeltaCount(q *safe.Queue[*types.Event], target, check string, delta, total uint64, thresholds CountCheck, metricName string) {
	var parts []string
	if thresholds.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning ≥ %d", thresholds.WarnGe))
	}
	if thresholds.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical ≥ %d", thresholds.CriticalGe))
	}
	attrs := map[string]string{
		"delta":          strconv.FormatUint(delta, 10),
		"total":          strconv.FormatUint(total, 10),
		"threshold_desc": strings.Join(parts, ", "),
	}
	event := ins.newEvent(check, target).SetAttrs(attrs).SetCurrentValue(strconv.FormatUint(delta, 10))

	status := types.EvaluateGeThreshold(float64(delta), float64(thresholds.WarnGe), float64(thresholds.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis %s delta %d >= critical threshold %d", metricName, delta, thresholds.CriticalGe)))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis %s delta %d >= warning threshold %d", metricName, delta, thresholds.WarnGe)))
	default:
		q.PushFront(event.SetDescription(fmt.Sprintf("redis %s delta %d, everything is ok", metricName, delta)))
	}
}

func (ins *Instance) checkUsedMemory(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::used_memory", target)
	usedMemory, ok, err := infoGetInt64(info, "used_memory")
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis used_memory: %v", err)))
		return
	}
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis info output missing used_memory"))
		return
	}

	var memParts []string
	if ins.UsedMemory.WarnGe > 0 {
		memParts = append(memParts, fmt.Sprintf("Warning ≥ %s", ins.UsedMemory.WarnGe.String()))
	}
	if ins.UsedMemory.CriticalGe > 0 {
		memParts = append(memParts, fmt.Sprintf("Critical ≥ %s", ins.UsedMemory.CriticalGe.String()))
	}
	attrs := map[string]string{
		"used_memory":     conv.HumanBytes(uint64(usedMemory)),
		"used_memory_bytes": strconv.FormatInt(usedMemory, 10),
		"threshold_desc":  strings.Join(memParts, ", "),
	}
	if maxmemory, ok, err := infoGetInt64(info, "maxmemory"); err == nil && ok && maxmemory > 0 {
		attrs["maxmemory"] = conv.HumanBytes(uint64(maxmemory))
		attrs["maxmemory_bytes"] = strconv.FormatInt(maxmemory, 10)
		attrs["used_percent_of_maxmemory"] = fmt.Sprintf("%.1f%%", float64(usedMemory)*100/float64(maxmemory))
	}
	event.SetAttrs(attrs).SetCurrentValue(conv.HumanBytes(uint64(usedMemory)))

	status := types.EvaluateGeThreshold(float64(usedMemory), float64(ins.UsedMemory.WarnGe), float64(ins.UsedMemory.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis used memory %s >= critical threshold %s",
				conv.HumanBytes(uint64(usedMemory)), ins.UsedMemory.CriticalGe.String())))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis used memory %s >= warning threshold %s",
				conv.HumanBytes(uint64(usedMemory)), ins.UsedMemory.WarnGe.String())))
	default:
		q.PushFront(event.SetDescription(fmt.Sprintf("redis used memory %s, everything is ok", conv.HumanBytes(uint64(usedMemory)))))
	}
}

func (ins *Instance) checkPersistence(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::persistence", target)

	loading, ok, err := infoGetInt(info, "loading")
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis loading state: %v", err)))
		return
	}
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis persistence info missing loading"))
		return
	}

	attrs := map[string]string{"loading": strconv.Itoa(loading)}
	if v, ok := info["rdb_last_bgsave_status"]; ok {
		attrs["rdb_last_bgsave_status"] = v
	}
	if v, ok := info["aof_enabled"]; ok {
		attrs["aof_enabled"] = v
	}
	if v, ok := info["aof_last_write_status"]; ok {
		attrs["aof_last_write_status"] = v
	}
	if v, ok := info["rdb_bgsave_in_progress"]; ok {
		attrs["rdb_bgsave_in_progress"] = v
	}
	if v, ok := info["aof_rewrite_in_progress"]; ok {
		attrs["aof_rewrite_in_progress"] = v
	}
	attrs["threshold_desc"] = fmt.Sprintf("%s: persistence not healthy", ins.Persistence.Severity)
	event.SetAttrs(attrs)

	if loading == 1 {
		q.PushFront(event.SetEventStatus(ins.Persistence.Severity).
			SetDescription("redis is loading persistence data"))
		return
	}

	if status, ok := info["rdb_last_bgsave_status"]; ok && status != "" && strings.ToLower(status) != "ok" {
		q.PushFront(event.SetEventStatus(ins.Persistence.Severity).
			SetDescription(fmt.Sprintf("redis RDB last bgsave status is %s", status)))
		return
	}

	if aofEnabled, ok := info["aof_enabled"]; ok && aofEnabled == "1" {
		if status, ok := info["aof_last_write_status"]; ok && status != "" && strings.ToLower(status) != "ok" {
			q.PushFront(event.SetEventStatus(ins.Persistence.Severity).
				SetDescription(fmt.Sprintf("redis AOF last write status is %s", status)))
			return
		}
	}

	q.PushFront(event.SetDescription("redis persistence status is healthy"))
}

func (ins *Instance) newEvent(check, target string) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":  check,
		"target": target,
	})
}

