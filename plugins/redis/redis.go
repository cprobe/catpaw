package redis

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/conv"
	"github.com/cprobe/catpaw/pkg/safe"
	tlscfg "github.com/cprobe/catpaw/pkg/tls"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const (
	pluginName       = "redis"
	defaultRedisPort = "6379"
)

type ConnectivityCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type ResponseTimeCheck struct {
	WarnGe     config.Duration `toml:"warn_ge"`
	CriticalGe config.Duration `toml:"critical_ge"`
	TitleRule  string          `toml:"title_rule"`
}

type RoleCheck struct {
	Expect    string `toml:"expect"`
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type CountCheck struct {
	WarnGe     int    `toml:"warn_ge"`
	CriticalGe int    `toml:"critical_ge"`
	TitleRule  string `toml:"title_rule"`
}

type MinCountCheck struct {
	WarnLt     int    `toml:"warn_lt"`
	CriticalLt int    `toml:"critical_lt"`
	TitleRule  string `toml:"title_rule"`
}

type MemoryUsageCheck struct {
	WarnGe     config.Size `toml:"warn_ge"`
	CriticalGe config.Size `toml:"critical_ge"`
	TitleRule  string      `toml:"title_rule"`
}

type MasterLinkCheck struct {
	Expect    string `toml:"expect"`
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type PersistenceCheck struct {
	Enabled   bool   `toml:"enabled"`
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type OpsPerSecondCheck struct {
	WarnGe     int    `toml:"warn_ge"`
	CriticalGe int    `toml:"critical_ge"`
	TitleRule  string `toml:"title_rule"`
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
	evictedKeys uint64
	expiredKeys uint64
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
			if ins.Connectivity.Severity == "" {
				ins.Connectivity.Severity = partial.Connectivity.Severity
			}
			if ins.Connectivity.TitleRule == "" {
				ins.Connectivity.TitleRule = partial.Connectivity.TitleRule
			}
			if ins.ResponseTime.WarnGe == 0 {
				ins.ResponseTime.WarnGe = partial.ResponseTime.WarnGe
			}
			if ins.ResponseTime.CriticalGe == 0 {
				ins.ResponseTime.CriticalGe = partial.ResponseTime.CriticalGe
			}
			if ins.ResponseTime.TitleRule == "" {
				ins.ResponseTime.TitleRule = partial.ResponseTime.TitleRule
			}
			if ins.Role.Expect == "" {
				ins.Role.Expect = partial.Role.Expect
			}
			if ins.Role.Severity == "" {
				ins.Role.Severity = partial.Role.Severity
			}
			if ins.Role.TitleRule == "" {
				ins.Role.TitleRule = partial.Role.TitleRule
			}
			if ins.ConnectedClients.WarnGe == 0 {
				ins.ConnectedClients.WarnGe = partial.ConnectedClients.WarnGe
			}
			if ins.ConnectedClients.CriticalGe == 0 {
				ins.ConnectedClients.CriticalGe = partial.ConnectedClients.CriticalGe
			}
			if ins.ConnectedClients.TitleRule == "" {
				ins.ConnectedClients.TitleRule = partial.ConnectedClients.TitleRule
			}
			if ins.BlockedClients.WarnGe == 0 {
				ins.BlockedClients.WarnGe = partial.BlockedClients.WarnGe
			}
			if ins.BlockedClients.CriticalGe == 0 {
				ins.BlockedClients.CriticalGe = partial.BlockedClients.CriticalGe
			}
			if ins.BlockedClients.TitleRule == "" {
				ins.BlockedClients.TitleRule = partial.BlockedClients.TitleRule
			}
			if ins.UsedMemory.WarnGe == 0 {
				ins.UsedMemory.WarnGe = partial.UsedMemory.WarnGe
			}
			if ins.UsedMemory.CriticalGe == 0 {
				ins.UsedMemory.CriticalGe = partial.UsedMemory.CriticalGe
			}
			if ins.UsedMemory.TitleRule == "" {
				ins.UsedMemory.TitleRule = partial.UsedMemory.TitleRule
			}
			if ins.MasterLink.Expect == "" {
				ins.MasterLink.Expect = partial.MasterLink.Expect
			}
			if ins.MasterLink.Severity == "" {
				ins.MasterLink.Severity = partial.MasterLink.Severity
			}
			if ins.MasterLink.TitleRule == "" {
				ins.MasterLink.TitleRule = partial.MasterLink.TitleRule
			}
			if ins.RejectedConn.WarnGe == 0 {
				ins.RejectedConn.WarnGe = partial.RejectedConn.WarnGe
			}
			if ins.RejectedConn.CriticalGe == 0 {
				ins.RejectedConn.CriticalGe = partial.RejectedConn.CriticalGe
			}
			if ins.RejectedConn.TitleRule == "" {
				ins.RejectedConn.TitleRule = partial.RejectedConn.TitleRule
			}
			if ins.ConnectedSlaves.WarnLt == 0 {
				ins.ConnectedSlaves.WarnLt = partial.ConnectedSlaves.WarnLt
			}
			if ins.ConnectedSlaves.CriticalLt == 0 {
				ins.ConnectedSlaves.CriticalLt = partial.ConnectedSlaves.CriticalLt
			}
			if ins.ConnectedSlaves.TitleRule == "" {
				ins.ConnectedSlaves.TitleRule = partial.ConnectedSlaves.TitleRule
			}
			if ins.EvictedKeys.WarnGe == 0 {
				ins.EvictedKeys.WarnGe = partial.EvictedKeys.WarnGe
			}
			if ins.EvictedKeys.CriticalGe == 0 {
				ins.EvictedKeys.CriticalGe = partial.EvictedKeys.CriticalGe
			}
			if ins.EvictedKeys.TitleRule == "" {
				ins.EvictedKeys.TitleRule = partial.EvictedKeys.TitleRule
			}
			if ins.ExpiredKeys.WarnGe == 0 {
				ins.ExpiredKeys.WarnGe = partial.ExpiredKeys.WarnGe
			}
			if ins.ExpiredKeys.CriticalGe == 0 {
				ins.ExpiredKeys.CriticalGe = partial.ExpiredKeys.CriticalGe
			}
			if ins.ExpiredKeys.TitleRule == "" {
				ins.ExpiredKeys.TitleRule = partial.ExpiredKeys.TitleRule
			}
			if ins.OpsPerSecond.WarnGe == 0 {
				ins.OpsPerSecond.WarnGe = partial.OpsPerSecond.WarnGe
			}
			if ins.OpsPerSecond.CriticalGe == 0 {
				ins.OpsPerSecond.CriticalGe = partial.OpsPerSecond.CriticalGe
			}
			if ins.OpsPerSecond.TitleRule == "" {
				ins.OpsPerSecond.TitleRule = partial.OpsPerSecond.TitleRule
			}
			if !ins.Persistence.Enabled {
				ins.Persistence.Enabled = partial.Persistence.Enabled
			}
			if ins.Persistence.Severity == "" {
				ins.Persistence.Severity = partial.Persistence.Severity
			}
			if ins.Persistence.TitleRule == "" {
				ins.Persistence.TitleRule = partial.Persistence.TitleRule
			}
			break
		}
	}
	return nil
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
		TitleRule:  ins.OpsPerSecond.TitleRule,
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
					}).SetTitleRule("[TPL]${check} ${from_hostip} ${target}").
						SetEventStatus(types.EventStatusCritical).
						SetDescription(fmt.Sprintf("panic during check: %v", r)))
				}
				se.Release()
				wg.Done()
			}()
			ins.gatherTarget(q, target)
		}(target)
	}
	wg.Wait()
}

func (ins *Instance) gatherTarget(q *safe.Queue[*types.Event], target string) {
	connEvent := ins.newEvent("redis::connectivity", target, ins.Connectivity.TitleRule)
	start := time.Now()

	client, err := ins.connect(target)
	if err != nil {
		connEvent.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
		q.PushFront(connEvent.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("redis ping failed: %v", err)))
		return
	}
	defer client.Close()

	if _, err := client.command("PING"); err != nil {
		connEvent.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
		q.PushFront(connEvent.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("redis ping failed: %v", err)))
		return
	}

	responseTime := time.Since(start)
	connEvent.Labels[types.AttrPrefix+"response_time"] = responseTime.String()
	q.PushFront(connEvent.SetDescription("redis ping ok"))

	ins.checkResponseTime(q, target, responseTime)

	infoCache := make(map[string]string)
	infoSection := func(section string) (string, error) {
		if info, ok := infoCache[section]; ok {
			return info, nil
		}
		info, err := client.info(section)
		if err != nil {
			return "", err
		}
		infoCache[section] = info
		return info, nil
	}

	if ins.Role.Expect != "" {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::role", target, ins.Role.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis replication info: %v", err)))
		} else {
			ins.checkRole(q, target, info)
		}
	}

	if ins.MasterLink.Expect != "" {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::master_link_status", target, ins.MasterLink.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis replication info: %v", err)))
		} else {
			ins.checkMasterLink(q, target, info)
		}
	}

	if ins.ConnectedSlaves.WarnLt > 0 || ins.ConnectedSlaves.CriticalLt > 0 {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::connected_slaves", target, ins.ConnectedSlaves.TitleRule).
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
				q.PushFront(ins.newEvent("redis::connected_clients", target, ins.ConnectedClients.TitleRule).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis client info: %v", err)))
			}
			if ins.BlockedClients.WarnGe > 0 || ins.BlockedClients.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::blocked_clients", target, ins.BlockedClients.TitleRule).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis client info: %v", err)))
			}
			return
		}

		if ins.ConnectedClients.WarnGe > 0 || ins.ConnectedClients.CriticalGe > 0 {
			if value, ok, parseErr := parseInfoInt(info, "connected_clients"); parseErr != nil {
				q.PushFront(ins.newEvent("redis::connected_clients", target, ins.ConnectedClients.TitleRule).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to parse redis connected_clients: %v", parseErr)))
			} else if !ok {
				q.PushFront(ins.newEvent("redis::connected_clients", target, ins.ConnectedClients.TitleRule).
					SetEventStatus(types.EventStatusCritical).
					SetDescription("redis info output missing connected_clients"))
			} else {
				ins.checkCount(q, target, "redis::connected_clients", value, ins.ConnectedClients, "connected clients")
			}
		}

		if ins.BlockedClients.WarnGe > 0 || ins.BlockedClients.CriticalGe > 0 {
			if value, ok, parseErr := parseInfoInt(info, "blocked_clients"); parseErr != nil {
				q.PushFront(ins.newEvent("redis::blocked_clients", target, ins.BlockedClients.TitleRule).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to parse redis blocked_clients: %v", parseErr)))
			} else if !ok {
				q.PushFront(ins.newEvent("redis::blocked_clients", target, ins.BlockedClients.TitleRule).
					SetEventStatus(types.EventStatusCritical).
					SetDescription("redis info output missing blocked_clients"))
			} else {
				ins.checkCount(q, target, "redis::blocked_clients", value, ins.BlockedClients, "blocked clients")
			}
		}
	}

	if ins.UsedMemory.WarnGe > 0 || ins.UsedMemory.CriticalGe > 0 {
		info, err := infoSection("memory")
		if err != nil {
			q.PushFront(ins.newEvent("redis::used_memory", target, ins.UsedMemory.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis memory info: %v", err)))
		} else {
			ins.checkUsedMemory(q, target, info)
		}
	}

	if ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0 {
		info, err := infoSection("stats")
		if err != nil {
			q.PushFront(ins.newEvent("redis::rejected_connections", target, ins.RejectedConn.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
		} else {
			ins.checkCountFromInfo(q, target, "redis::rejected_connections", info, "rejected_connections",
				ins.RejectedConn, "rejected connections")
		}
	}

	if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 || ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 || ins.OpsPerSecond.WarnGe > 0 || ins.OpsPerSecond.CriticalGe > 0 {
		info, err := infoSection("stats")
		if err != nil {
			if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::evicted_keys", target, ins.EvictedKeys.TitleRule).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
			if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::expired_keys", target, ins.ExpiredKeys.TitleRule).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
			if ins.OpsPerSecond.WarnGe > 0 || ins.OpsPerSecond.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::instantaneous_ops_per_sec", target, ins.OpsPerSecond.TitleRule).
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
						TitleRule:  ins.OpsPerSecond.TitleRule,
					},
					"instantaneous ops per second")
			}
		}
	}

	if ins.Persistence.Enabled {
		info, err := infoSection("persistence")
		if err != nil {
			q.PushFront(ins.newEvent("redis::persistence", target, ins.Persistence.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis persistence info: %v", err)))
		} else {
			ins.checkPersistence(q, target, info)
		}
	}
}

func (ins *Instance) connect(target string) (*redisClient, error) {
	var conn net.Conn
	var err error

	if ins.tlsConfig != nil {
		cfg := ins.tlsConfig.Clone()
		host, _, splitErr := net.SplitHostPort(target)
		if splitErr == nil && cfg.ServerName == "" && net.ParseIP(host) == nil {
			cfg.ServerName = host
		}

		rawConn, dialErr := ins.dialTarget("tcp", target)
		if dialErr != nil {
			return nil, dialErr
		}

		tlsConn := tls.Client(rawConn, cfg)
		if err := tlsConn.SetDeadline(time.Now().Add(time.Duration(ins.Timeout))); err != nil {
			rawConn.Close()
			return nil, err
		}
		if err := tlsConn.Handshake(); err != nil {
			rawConn.Close()
			return nil, err
		}
		_ = tlsConn.SetDeadline(time.Time{})
		conn = tlsConn
	} else {
		conn, err = ins.dialTarget("tcp", target)
		if err != nil {
			return nil, err
		}
	}

	client := &redisClient{
		conn:        conn,
		reader:      bufio.NewReader(conn),
		timeout:     time.Duration(ins.Timeout),
		readTimeout: time.Duration(ins.ReadTimeout),
	}

	if ins.Password != "" || ins.Username != "" {
		args := []string{"AUTH"}
		if ins.Username != "" {
			args = append(args, ins.Username)
		}
		args = append(args, ins.Password)
		reply, err := client.command(args...)
		if err != nil {
			client.Close()
			return nil, err
		}
		if strings.ToUpper(reply) != "OK" {
			client.Close()
			return nil, fmt.Errorf("unexpected AUTH reply: %q", reply)
		}
	}

	if ins.DB > 0 {
		reply, err := client.command("SELECT", strconv.Itoa(ins.DB))
		if err != nil {
			client.Close()
			return nil, err
		}
		if strings.ToUpper(reply) != "OK" {
			client.Close()
			return nil, fmt.Errorf("unexpected SELECT reply: %q", reply)
		}
	}

	return client, nil
}

func (ins *Instance) dialTarget(network, target string) (net.Conn, error) {
	if ins.dialFunc != nil {
		return ins.dialFunc(network, target)
	}
	dialer := &net.Dialer{Timeout: time.Duration(ins.Timeout)}
	return dialer.Dial(network, target)
}

func (ins *Instance) checkResponseTime(q *safe.Queue[*types.Event], target string, responseTime time.Duration) {
	if ins.ResponseTime.WarnGe == 0 && ins.ResponseTime.CriticalGe == 0 {
		return
	}

	event := ins.newEvent("redis::response_time", target, ins.ResponseTime.TitleRule)
	event.Labels[types.AttrPrefix+"response_time"] = responseTime.String()
	if ins.ResponseTime.WarnGe > 0 {
		event.Labels[types.AttrPrefix+"warn_ge"] = time.Duration(ins.ResponseTime.WarnGe).String()
	}
	if ins.ResponseTime.CriticalGe > 0 {
		event.Labels[types.AttrPrefix+"critical_ge"] = time.Duration(ins.ResponseTime.CriticalGe).String()
	}

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

func (ins *Instance) checkRole(q *safe.Queue[*types.Event], target string, info string) {
	event := ins.newEvent("redis::role", target, ins.Role.TitleRule)
	actual, ok := parseInfoString(info, "role")
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis info output missing role"))
		return
	}

	actual = strings.ToLower(strings.TrimSpace(actual))
	event.Labels[types.AttrPrefix+"actual"] = actual
	event.Labels[types.AttrPrefix+"expect"] = ins.Role.Expect

	if actual == ins.Role.Expect {
		q.PushFront(event.SetDescription(fmt.Sprintf("redis role is %s, matches expectation", actual)))
		return
	}

	q.PushFront(event.SetEventStatus(ins.Role.Severity).
		SetDescription(fmt.Sprintf("redis role is %s, expected %s", actual, ins.Role.Expect)))
}

func (ins *Instance) checkMasterLink(q *safe.Queue[*types.Event], target string, info string) {
	event := ins.newEvent("redis::master_link_status", target, ins.MasterLink.TitleRule)
	role, ok := parseInfoString(info, "role")
	if ok {
		event.Labels[types.AttrPrefix+"role"] = role
	}
	actual, ok := parseInfoString(info, "master_link_status")
	if !ok {
		q.PushFront(event.SetEventStatus(ins.MasterLink.Severity).
			SetDescription("redis replication info missing master_link_status"))
		return
	}

	actual = strings.ToLower(strings.TrimSpace(actual))
	event.Labels[types.AttrPrefix+"actual"] = actual
	event.Labels[types.AttrPrefix+"expect"] = ins.MasterLink.Expect

	if v, ok := parseInfoString(info, "master_host"); ok && v != "" {
		event.Labels[types.AttrPrefix+"master_host"] = v
	}
	if v, ok := parseInfoString(info, "master_port"); ok && v != "" {
		event.Labels[types.AttrPrefix+"master_port"] = v
	}

	if actual == ins.MasterLink.Expect {
		q.PushFront(event.SetDescription(fmt.Sprintf("redis master link status is %s, matches expectation", actual)))
		return
	}

	q.PushFront(event.SetEventStatus(ins.MasterLink.Severity).
		SetDescription(fmt.Sprintf("redis master link status is %s, expected %s", actual, ins.MasterLink.Expect)))
}

func (ins *Instance) checkCount(q *safe.Queue[*types.Event], target, check string, value int, thresholds CountCheck, metricName string) {
	event := ins.newEvent(check, target, thresholds.TitleRule)
	labelKey := strings.TrimPrefix(check, "redis::")
	event.Labels[types.AttrPrefix+labelKey] = strconv.Itoa(value)
	if thresholds.WarnGe > 0 {
		event.Labels[types.AttrPrefix+"warn_ge"] = strconv.Itoa(thresholds.WarnGe)
	}
	if thresholds.CriticalGe > 0 {
		event.Labels[types.AttrPrefix+"critical_ge"] = strconv.Itoa(thresholds.CriticalGe)
	}

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

func (ins *Instance) checkCountFromInfo(q *safe.Queue[*types.Event], target, check, info, key string, thresholds CountCheck, metricName string) {
	value, ok, err := parseInfoInt(info, key)
	if err != nil {
		q.PushFront(ins.newEvent(check, target, thresholds.TitleRule).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis %s: %v", key, err)))
		return
	}
	if !ok {
		q.PushFront(ins.newEvent(check, target, thresholds.TitleRule).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis info output missing %s", key)))
		return
	}
	ins.checkCount(q, target, check, value, thresholds, metricName)
}

func (ins *Instance) checkMinCountFromInfo(q *safe.Queue[*types.Event], target, check, info, key string, thresholds MinCountCheck, metricName string) {
	value, ok, err := parseInfoInt(info, key)
	if err != nil {
		q.PushFront(ins.newEvent(check, target, thresholds.TitleRule).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis %s: %v", key, err)))
		return
	}
	if !ok {
		q.PushFront(ins.newEvent(check, target, thresholds.TitleRule).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis info output missing %s", key)))
		return
	}
	ins.checkMinCount(q, target, check, value, thresholds, metricName)
}

func (ins *Instance) checkMinCount(q *safe.Queue[*types.Event], target, check string, value int, thresholds MinCountCheck, metricName string) {
	event := ins.newEvent(check, target, thresholds.TitleRule)
	labelKey := strings.TrimPrefix(check, "redis::")
	event.Labels[types.AttrPrefix+labelKey] = strconv.Itoa(value)
	if thresholds.WarnLt > 0 {
		event.Labels[types.AttrPrefix+"warn_lt"] = strconv.Itoa(thresholds.WarnLt)
	}
	if thresholds.CriticalLt > 0 {
		event.Labels[types.AttrPrefix+"critical_lt"] = strconv.Itoa(thresholds.CriticalLt)
	}

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

func (ins *Instance) checkCounterDeltas(q *safe.Queue[*types.Event], target, info string) {
	var (
		evicted uint64
		expired uint64
	)

	if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
		value, ok, err := parseInfoUint64(info, "evicted_keys")
		if err != nil {
			q.PushFront(ins.newEvent("redis::evicted_keys", target, ins.EvictedKeys.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to parse redis evicted_keys: %v", err)))
			return
		}
		if !ok {
			q.PushFront(ins.newEvent("redis::evicted_keys", target, ins.EvictedKeys.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription("redis info output missing evicted_keys"))
			return
		}
		evicted = value
	}

	if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
		value, ok, err := parseInfoUint64(info, "expired_keys")
		if err != nil {
			q.PushFront(ins.newEvent("redis::expired_keys", target, ins.ExpiredKeys.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to parse redis expired_keys: %v", err)))
			return
		}
		if !ok {
			q.PushFront(ins.newEvent("redis::expired_keys", target, ins.ExpiredKeys.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription("redis info output missing expired_keys"))
			return
		}
		expired = value
	}

	ins.statsMu.Lock()
	prev := ins.prevStats[target]
	initialized := ins.initialized[target]
	ins.prevStats[target] = redisCounterSnapshot{
		evictedKeys: evicted,
		expiredKeys: expired,
	}
	ins.initialized[target] = true
	ins.statsMu.Unlock()

	if !initialized {
		if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
			event := ins.newEvent("redis::evicted_keys", target, ins.EvictedKeys.TitleRule)
			event.Labels[types.AttrPrefix+"delta"] = "0"
			event.Labels[types.AttrPrefix+"total"] = strconv.FormatUint(evicted, 10)
			q.PushFront(event.SetDescription(fmt.Sprintf("redis evicted keys baseline established (total: %d)", evicted)))
		}
		if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
			event := ins.newEvent("redis::expired_keys", target, ins.ExpiredKeys.TitleRule)
			event.Labels[types.AttrPrefix+"delta"] = "0"
			event.Labels[types.AttrPrefix+"total"] = strconv.FormatUint(expired, 10)
			q.PushFront(event.SetDescription(fmt.Sprintf("redis expired keys baseline established (total: %d)", expired)))
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
}

func (ins *Instance) checkDeltaCount(q *safe.Queue[*types.Event], target, check string, delta, total uint64, thresholds CountCheck, metricName string) {
	event := ins.newEvent(check, target, thresholds.TitleRule)
	event.Labels[types.AttrPrefix+"delta"] = strconv.FormatUint(delta, 10)
	event.Labels[types.AttrPrefix+"total"] = strconv.FormatUint(total, 10)
	if thresholds.WarnGe > 0 {
		event.Labels[types.AttrPrefix+"warn_ge"] = strconv.Itoa(thresholds.WarnGe)
	}
	if thresholds.CriticalGe > 0 {
		event.Labels[types.AttrPrefix+"critical_ge"] = strconv.Itoa(thresholds.CriticalGe)
	}

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

func (ins *Instance) checkUsedMemory(q *safe.Queue[*types.Event], target string, info string) {
	event := ins.newEvent("redis::used_memory", target, ins.UsedMemory.TitleRule)
	usedMemory, ok, err := parseInfoInt64(info, "used_memory")
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

	event.Labels[types.AttrPrefix+"used_memory"] = conv.HumanBytes(uint64(usedMemory))
	event.Labels[types.AttrPrefix+"used_memory_bytes"] = strconv.FormatInt(usedMemory, 10)
	if ins.UsedMemory.WarnGe > 0 {
		event.Labels[types.AttrPrefix+"warn_ge"] = ins.UsedMemory.WarnGe.String()
	}
	if ins.UsedMemory.CriticalGe > 0 {
		event.Labels[types.AttrPrefix+"critical_ge"] = ins.UsedMemory.CriticalGe.String()
	}
	if maxmemory, ok, err := parseInfoInt64(info, "maxmemory"); err == nil && ok && maxmemory > 0 {
		event.Labels[types.AttrPrefix+"maxmemory"] = conv.HumanBytes(uint64(maxmemory))
		event.Labels[types.AttrPrefix+"maxmemory_bytes"] = strconv.FormatInt(maxmemory, 10)
		event.Labels[types.AttrPrefix+"used_percent_of_maxmemory"] = fmt.Sprintf("%.1f%%", float64(usedMemory)*100/float64(maxmemory))
	}

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

func (ins *Instance) checkPersistence(q *safe.Queue[*types.Event], target string, info string) {
	event := ins.newEvent("redis::persistence", target, ins.Persistence.TitleRule)

	loading, ok, err := parseInfoInt(info, "loading")
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

	event.Labels[types.AttrPrefix+"loading"] = strconv.Itoa(loading)
	if v, ok := parseInfoString(info, "rdb_last_bgsave_status"); ok {
		event.Labels[types.AttrPrefix+"rdb_last_bgsave_status"] = v
	}
	if v, ok := parseInfoString(info, "aof_enabled"); ok {
		event.Labels[types.AttrPrefix+"aof_enabled"] = v
	}
	if v, ok := parseInfoString(info, "aof_last_write_status"); ok {
		event.Labels[types.AttrPrefix+"aof_last_write_status"] = v
	}
	if v, ok := parseInfoString(info, "rdb_bgsave_in_progress"); ok {
		event.Labels[types.AttrPrefix+"rdb_bgsave_in_progress"] = v
	}
	if v, ok := parseInfoString(info, "aof_rewrite_in_progress"); ok {
		event.Labels[types.AttrPrefix+"aof_rewrite_in_progress"] = v
	}

	if loading == 1 {
		q.PushFront(event.SetEventStatus(ins.Persistence.Severity).
			SetDescription("redis is loading persistence data"))
		return
	}

	if status, ok := parseInfoString(info, "rdb_last_bgsave_status"); ok && status != "" && strings.ToLower(status) != "ok" {
		q.PushFront(event.SetEventStatus(ins.Persistence.Severity).
			SetDescription(fmt.Sprintf("redis RDB last bgsave status is %s", status)))
		return
	}

	aofEnabled, ok := parseInfoString(info, "aof_enabled")
	if ok && aofEnabled == "1" {
		if status, ok := parseInfoString(info, "aof_last_write_status"); ok && status != "" && strings.ToLower(status) != "ok" {
			q.PushFront(event.SetEventStatus(ins.Persistence.Severity).
				SetDescription(fmt.Sprintf("redis AOF last write status is %s", status)))
			return
		}
	}

	q.PushFront(event.SetDescription("redis persistence status is healthy"))
}

func (ins *Instance) newEvent(check, target, titleRule string) *types.Event {
	if titleRule == "" {
		titleRule = "[TPL]${check} ${from_hostip} ${target}"
	}
	return types.BuildEvent(map[string]string{
		"check":  check,
		"target": target,
	}).SetTitleRule(titleRule)
}

type redisClient struct {
	conn        net.Conn
	reader      *bufio.Reader
	timeout     time.Duration
	readTimeout time.Duration
}

func (c *redisClient) Close() error {
	return c.conn.Close()
}

func (c *redisClient) info(section string) (string, error) {
	reply, err := c.command("INFO", section)
	if err != nil {
		return "", err
	}
	return reply, nil
}

func (c *redisClient) command(args ...string) (string, error) {
	if err := c.writeCommand(args...); err != nil {
		return "", err
	}
	reply, err := c.readReply()
	if err != nil {
		return "", err
	}

	switch v := reply.(type) {
	case string:
		return v, nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("unsupported redis reply type %T", reply)
	}
}

func (c *redisClient) writeCommand(args ...string) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("*")
	b.WriteString(strconv.Itoa(len(args)))
	b.WriteString("\r\n")
	for _, arg := range args {
		b.WriteString("$")
		b.WriteString(strconv.Itoa(len(arg)))
		b.WriteString("\r\n")
		b.WriteString(arg)
		b.WriteString("\r\n")
	}

	_, err := c.conn.Write([]byte(b.String()))
	return err
}

func (c *redisClient) readReply() (any, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
		return nil, err
	}

	prefix, err := c.reader.ReadByte()
	if err != nil {
		return nil, err
	}

	switch prefix {
	case '+':
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		return line, nil
	case '-':
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		return nil, errors.New(line)
	case ':':
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return nil, err
		}
		return strconv.FormatInt(n, 10), nil
	case '$':
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if size < 0 {
			return nil, nil
		}
		buf := make([]byte, size+2)
		if _, err := ioReadFull(c.reader, buf); err != nil {
			return nil, err
		}
		return string(buf[:size]), nil
	default:
		return nil, fmt.Errorf("unsupported redis reply prefix %q", prefix)
	}
}

func (c *redisClient) readLine() (string, error) {
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func ioReadFull(r *bufio.Reader, buf []byte) (int, error) {
	read := 0
	for read < len(buf) {
		n, err := r.Read(buf[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}

func parseInfoString(info, key string) (string, bool) {
	prefix := key + ":"
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", false
}

func parseInfoInt(info, key string) (int, bool, error) {
	value, ok := parseInfoString(info, key)
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, true, err
	}
	return n, true, nil
}

func parseInfoInt64(info, key string) (int64, bool, error) {
	value, ok := parseInfoString(info, key)
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, true, err
	}
	return n, true, nil
}

func parseInfoUint64(info, key string) (uint64, bool, error) {
	value, ok := parseInfoString(info, key)
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, true, err
	}
	return n, true, nil
}
