package redis

import (
	"crypto/tls"
	"net"
	"sync"

	"github.com/cprobe/catpaw/config"
	tlscfg "github.com/cprobe/catpaw/pkg/tls"
)

const (
	pluginName       = "redis"
	defaultRedisPort = "6379"
	maxBulkSize      = 1 << 20 // 1MB, prevent unbounded allocation from malformed replies
	clusterSlotsFull = 16384

	redisModeAuto       = "auto"
	redisModeStandalone = "standalone"
	redisModeCluster    = "cluster"
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

type ReplLagCheck struct {
	WarnGe     config.Size `toml:"warn_ge"`
	CriticalGe config.Size `toml:"critical_ge"`
}

type PercentCheck struct {
	WarnGe     int `toml:"warn_ge"`
	CriticalGe int `toml:"critical_ge"`
}

type MasterLinkCheck struct {
	Expect   string `toml:"expect"`
	Severity string `toml:"severity"`
}

type PersistenceCheck struct {
	Enabled  *bool  `toml:"enabled"`
	Severity string `toml:"severity"`
}

type ClusterStateCheck struct {
	Disabled *bool  `toml:"disabled"`
	Severity string `toml:"severity"`
}

type ClusterTopologyCheck struct {
	Disabled *bool `toml:"disabled"`
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
	Mode        string          `toml:"mode"`
	ClusterName string          `toml:"cluster_name"`
	tlscfg.ClientConfig
	Connectivity     ConnectivityCheck    `toml:"connectivity"`
	ResponseTime     ResponseTimeCheck    `toml:"response_time"`
	Role             RoleCheck            `toml:"role"`
	ReplLag          ReplLagCheck         `toml:"repl_lag"`
	ConnectedClients CountCheck           `toml:"connected_clients"`
	BlockedClients   CountCheck           `toml:"blocked_clients"`
	UsedMemory       MemoryUsageCheck     `toml:"used_memory"`
	UsedMemoryPct    PercentCheck         `toml:"used_memory_pct"`
	RejectedConn     CountCheck           `toml:"rejected_connections"`
	MasterLink       MasterLinkCheck      `toml:"master_link_status"`
	ConnectedSlaves  MinCountCheck        `toml:"connected_slaves"`
	EvictedKeys      CountCheck           `toml:"evicted_keys"`
	ExpiredKeys      CountCheck           `toml:"expired_keys"`
	OpsPerSecond     OpsPerSecondCheck    `toml:"instantaneous_ops_per_sec"`
	Persistence      PersistenceCheck     `toml:"persistence"`
	ClusterState     ClusterStateCheck    `toml:"cluster_state"`
	ClusterTopology  ClusterTopologyCheck `toml:"cluster_topology"`
}

type Instance struct {
	config.InternalConfig
	Partial string `toml:"partial"`

	Targets          []string             `toml:"targets"`
	Concurrency      int                  `toml:"concurrency"`
	Timeout          config.Duration      `toml:"timeout"`
	ReadTimeout      config.Duration      `toml:"read_timeout"`
	Username         string               `toml:"username"`
	Password         string               `toml:"password"`
	DB               int                  `toml:"db"`
	Mode             string               `toml:"mode"`
	ClusterName      string               `toml:"cluster_name"`
	Connectivity     ConnectivityCheck    `toml:"connectivity"`
	ResponseTime     ResponseTimeCheck    `toml:"response_time"`
	Role             RoleCheck            `toml:"role"`
	ReplLag          ReplLagCheck         `toml:"repl_lag"`
	ConnectedClients CountCheck           `toml:"connected_clients"`
	BlockedClients   CountCheck           `toml:"blocked_clients"`
	UsedMemory       MemoryUsageCheck     `toml:"used_memory"`
	UsedMemoryPct    PercentCheck         `toml:"used_memory_pct"`
	RejectedConn     CountCheck           `toml:"rejected_connections"`
	MasterLink       MasterLinkCheck      `toml:"master_link_status"`
	ConnectedSlaves  MinCountCheck        `toml:"connected_slaves"`
	EvictedKeys      CountCheck           `toml:"evicted_keys"`
	ExpiredKeys      CountCheck           `toml:"expired_keys"`
	OpsPerSecond     OpsPerSecondCheck    `toml:"instantaneous_ops_per_sec"`
	Persistence      PersistenceCheck     `toml:"persistence"`
	ClusterState     ClusterStateCheck    `toml:"cluster_state"`
	ClusterTopology  ClusterTopologyCheck `toml:"cluster_topology"`

	tlscfg.ClientConfig
	tlsConfig *tls.Config
	dialFunc  func(network, address string) (net.Conn, error)

	statsMu     sync.Mutex
	prevStats   map[string]redisCounterSnapshot
	initialized map[string]bool

	inFlight sync.Map // target → int64 (unix timestamp)
	prevHung sync.Map // target → bool
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
