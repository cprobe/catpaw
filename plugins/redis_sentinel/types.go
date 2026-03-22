package redis_sentinel

import (
	"crypto/tls"
	"net"
	"sync"

	"github.com/cprobe/catpaw/digcore/config"
	tlscfg "github.com/cprobe/catpaw/digcore/pkg/tls"
)

const (
	pluginName           = "redis_sentinel"
	defaultSentinelPort  = "26379"
	maxBulkSize          = 1 << 20
	defaultGatherMinimum = 30
)

type SeverityCheck struct {
	Enabled  *bool  `toml:"enabled"`
	Severity string `toml:"severity"`
}

type OverviewCheck struct {
	Enabled       *bool  `toml:"enabled"`
	EmptySeverity string `toml:"empty_severity"`
}

type ThresholdCheck struct {
	Enabled    *bool `toml:"enabled"`
	WarnLt     int   `toml:"warn_lt"`
	CriticalLt int   `toml:"critical_lt"`
}

type MasterRef struct {
	Name string `toml:"name"`
}

type Partial struct {
	ID          string          `toml:"id"`
	Concurrency int             `toml:"concurrency"`
	Timeout     config.Duration `toml:"timeout"`
	ReadTimeout config.Duration `toml:"read_timeout"`
	Username    string          `toml:"username"`
	Password    string          `toml:"password"`

	tlscfg.ClientConfig

	Connectivity         SeverityCheck  `toml:"connectivity"`
	Role                 SeverityCheck  `toml:"role"`
	MastersOverview      OverviewCheck  `toml:"masters_overview"`
	CKQuorum             SeverityCheck  `toml:"ckquorum"`
	MasterSDown          SeverityCheck  `toml:"master_sdown"`
	MasterODown          SeverityCheck  `toml:"master_odown"`
	MasterAddrResolution SeverityCheck  `toml:"master_addr_resolution"`
	PeerCount            ThresholdCheck `toml:"peer_count"`
	KnownReplicas        ThresholdCheck `toml:"known_replicas"`
	KnownSentinels       ThresholdCheck `toml:"known_sentinels"`
	FailoverInProgress   SeverityCheck  `toml:"failover_in_progress"`
	Tilt                 SeverityCheck  `toml:"tilt"`
}

type Instance struct {
	config.InternalConfig
	Partial string `toml:"partial"`

	Targets              []string        `toml:"targets"`
	Concurrency          int             `toml:"concurrency"`
	Timeout              config.Duration `toml:"timeout"`
	ReadTimeout          config.Duration `toml:"read_timeout"`
	Username             string          `toml:"username"`
	Password             string          `toml:"password"`
	Masters              []MasterRef     `toml:"masters"`
	Connectivity         SeverityCheck   `toml:"connectivity"`
	Role                 SeverityCheck   `toml:"role"`
	MastersOverview      OverviewCheck   `toml:"masters_overview"`
	CKQuorum             SeverityCheck   `toml:"ckquorum"`
	MasterSDown          SeverityCheck   `toml:"master_sdown"`
	MasterODown          SeverityCheck   `toml:"master_odown"`
	MasterAddrResolution SeverityCheck   `toml:"master_addr_resolution"`
	PeerCount            ThresholdCheck  `toml:"peer_count"`
	KnownReplicas        ThresholdCheck  `toml:"known_replicas"`
	KnownSentinels       ThresholdCheck  `toml:"known_sentinels"`
	FailoverInProgress   SeverityCheck   `toml:"failover_in_progress"`
	Tilt                 SeverityCheck   `toml:"tilt"`

	tlscfg.ClientConfig
	tlsConfig *tls.Config
	dialFunc  func(network, address string) (net.Conn, error)

	inFlight sync.Map
	prevHung sync.Map
}

type RedisSentinelPlugin struct {
	config.InternalConfig
	Partials  []Partial   `toml:"partials"`
	Instances []*Instance `toml:"instances"`
}
