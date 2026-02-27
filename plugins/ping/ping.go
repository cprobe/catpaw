package ping

import (
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
	ping "github.com/prometheus-community/pro-bing"
)

const (
	pluginName               = "ping"
	defaultPingDataBytesSize = 56
)

type ConnectivityCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type PacketLossCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule   string  `toml:"title_rule"`
}

type RttCheck struct {
	WarnGe     config.Duration `toml:"warn_ge"`
	CriticalGe config.Duration `toml:"critical_ge"`
	TitleRule   string          `toml:"title_rule"`
}

type Partial struct {
	ID           string            `toml:"id"`
	Concurrency  int               `toml:"concurrency"`
	Count        int               `toml:"count"`
	PingInterval config.Duration   `toml:"ping_interval"`
	Timeout      config.Duration   `toml:"timeout"`
	Interface    string            `toml:"interface"`
	IPv6         *bool             `toml:"ipv6"`
	Size         *int              `toml:"size"`
	Connectivity ConnectivityCheck `toml:"connectivity"`
	PacketLoss   PacketLossCheck   `toml:"packet_loss"`
	Rtt          RttCheck          `toml:"rtt"`
}

type Instance struct {
	config.InternalConfig
	Partial string `toml:"partial"`

	Targets      []string          `toml:"targets"`
	Concurrency  int               `toml:"concurrency"`
	Count        int               `toml:"count"`
	PingInterval config.Duration   `toml:"ping_interval"`
	Timeout      config.Duration   `toml:"timeout"`
	Interface    string            `toml:"interface"`
	IPv6         *bool             `toml:"ipv6"`
	Size         *int              `toml:"size"`
	Connectivity ConnectivityCheck `toml:"connectivity"`
	PacketLoss   PacketLossCheck   `toml:"packet_loss"`
	Rtt          RttCheck          `toml:"rtt"`

	calcInterval  time.Duration
	calcTimeout   time.Duration
	sourceAddress string
}

type PingPlugin struct {
	config.InternalConfig
	Partials  []Partial   `toml:"partials"`
	Instances []*Instance `toml:"instances"`
}

func (p *PingPlugin) ApplyPartials() error {
	for i := 0; i < len(p.Instances); i++ {
		id := p.Instances[i].Partial
		if id != "" {
			for _, partial := range p.Partials {
				if partial.ID == id {
					if p.Instances[i].Concurrency == 0 {
						p.Instances[i].Concurrency = partial.Concurrency
					}
					if p.Instances[i].Count == 0 {
						p.Instances[i].Count = partial.Count
					}
					if p.Instances[i].PingInterval == 0 {
						p.Instances[i].PingInterval = partial.PingInterval
					}
					if p.Instances[i].Timeout == 0 {
						p.Instances[i].Timeout = partial.Timeout
					}
					if p.Instances[i].Interface == "" {
						p.Instances[i].Interface = partial.Interface
					}
					if p.Instances[i].IPv6 == nil {
						p.Instances[i].IPv6 = partial.IPv6
					}
					if p.Instances[i].Size == nil {
						p.Instances[i].Size = partial.Size
					}
					if p.Instances[i].Connectivity.Severity == "" {
						p.Instances[i].Connectivity.Severity = partial.Connectivity.Severity
					}
					if p.Instances[i].PacketLoss.WarnGe == 0 {
						p.Instances[i].PacketLoss.WarnGe = partial.PacketLoss.WarnGe
					}
					if p.Instances[i].PacketLoss.CriticalGe == 0 {
						p.Instances[i].PacketLoss.CriticalGe = partial.PacketLoss.CriticalGe
					}
					if p.Instances[i].Rtt.WarnGe == 0 {
						p.Instances[i].Rtt.WarnGe = partial.Rtt.WarnGe
					}
					if p.Instances[i].Rtt.CriticalGe == 0 {
						p.Instances[i].Rtt.CriticalGe = partial.Rtt.CriticalGe
					}
					break
				}
			}
		}
	}
	return nil
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &PingPlugin{}
	})
}

func (p *PingPlugin) GetInstances() []plugins.Instance {
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
	if ins.Count < 1 {
		ins.Count = 5
	}

	if time.Duration(ins.PingInterval) < 200*time.Millisecond {
		ins.calcInterval = 200 * time.Millisecond
	} else {
		ins.calcInterval = time.Duration(ins.PingInterval)
	}

	if ins.Timeout == 0 {
		ins.calcTimeout = 3 * time.Second
	} else {
		ins.calcTimeout = time.Duration(ins.Timeout)
	}

	minTimeout := time.Duration(ins.Count) * ins.calcInterval
	if ins.calcTimeout < minTimeout {
		ins.calcTimeout = minTimeout
		logger.Logger.Warnw("ping timeout auto-adjusted to accommodate count*ping_interval",
			"timeout", ins.calcTimeout, "count", ins.Count, "ping_interval", ins.calcInterval)
	}

	if ins.Connectivity.Severity == "" {
		ins.Connectivity.Severity = types.EventStatusCritical
	}

	if ins.PacketLoss.WarnGe > 0 && ins.PacketLoss.CriticalGe > 0 {
		if ins.PacketLoss.WarnGe >= ins.PacketLoss.CriticalGe {
			return fmt.Errorf("packet_loss.warn_ge(%.1f) must be less than packet_loss.critical_ge(%.1f)",
				ins.PacketLoss.WarnGe, ins.PacketLoss.CriticalGe)
		}
	}

	if ins.Rtt.WarnGe > 0 && ins.Rtt.CriticalGe > 0 {
		if ins.Rtt.WarnGe >= ins.Rtt.CriticalGe {
			return fmt.Errorf("rtt.warn_ge(%s) must be less than rtt.critical_ge(%s)",
				time.Duration(ins.Rtt.WarnGe), time.Duration(ins.Rtt.CriticalGe))
		}
	}

	if ins.Interface != "" {
		if addr := net.ParseIP(ins.Interface); addr != nil {
			ins.sourceAddress = ins.Interface
		} else {
			iface, err := net.InterfaceByName(ins.Interface)
			if err != nil {
				return fmt.Errorf("failed to get interface %q: %v", ins.Interface, err)
			}
			addrs, err := iface.Addrs()
			if err != nil {
				return fmt.Errorf("failed to get addresses of interface %q: %v", ins.Interface, err)
			}
			if len(addrs) == 0 {
				return fmt.Errorf("interface %q has no addresses", ins.Interface)
			}
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok {
					ins.sourceAddress = ipNet.IP.String()
					break
				}
			}
			if ins.sourceAddress == "" {
				return fmt.Errorf("interface %q has no usable IP address", ins.Interface)
			}
		}
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	logger.Logger.Debugw("ping targets", "targets", ins.Targets)

	if len(ins.Targets) == 0 {
		return
	}

	wg := new(sync.WaitGroup)
	se := make(chan struct{}, ins.Concurrency)
	for _, target := range ins.Targets {
		wg.Add(1)
		go func(target string) {
			se <- struct{}{}
			defer func() {
				if r := recover(); r != nil {
					logger.Logger.Errorw("panic in ping gather goroutine", "target", target, "recover", r)
				}
				<-se
				wg.Done()
			}()
			ins.gather(q, target)
		}(target)
	}
	wg.Wait()
}

func (ins *Instance) gather(q *safe.Queue[*types.Event], target string) {
	logger.Logger.Debugw("ping target", "target", target)

	labels := map[string]string{
		"target": target,
	}

	connTR := ins.Connectivity.TitleRule
	if connTR == "" {
		connTR = "[check] [target]"
	}

	connEvent := types.BuildEvent(map[string]string{
		"check": "ping::connectivity",
	}, labels).SetTitleRule(connTR)

	stats, err := ins.ping(target)
	if err != nil {
		message := fmt.Sprintf("ping %s failed: %v", target, err)
		logger.Logger.Errorw("ping failed", "target", target, "error", err)
		q.PushFront(connEvent.SetEventStatus(ins.Connectivity.Severity).SetDescription(ins.buildDesc(target, message, nil)))
		return
	}

	if stats.PacketsSent == 0 {
		message := fmt.Sprintf("no packets sent to %s", target)
		logger.Logger.Errorw("no packets sent", "target", target)
		q.PushFront(connEvent.SetEventStatus(ins.Connectivity.Severity).SetDescription(ins.buildDesc(target, message, nil)))
		return
	}

	if stats.PacketsRecv == 0 {
		message := fmt.Sprintf("no packets received from %s, 100%% packet loss", target)
		logger.Logger.Errorw("no packets received", "target", target)
		q.PushFront(connEvent.SetEventStatus(ins.Connectivity.Severity).SetDescription(ins.buildDesc(target, message, stats)))
		return
	}

	// Connectivity OK
	connEvent.SetDescription(ins.buildDesc(target, "everything is ok", stats))
	q.PushFront(connEvent)

	// Packet loss threshold check (separate event)
	if ins.PacketLoss.WarnGe > 0 || ins.PacketLoss.CriticalGe > 0 {
		plTR := ins.PacketLoss.TitleRule
		if plTR == "" {
			plTR = "[check] [target]"
		}

		plEvent := types.BuildEvent(map[string]string{
			"check": "ping::packet_loss",
		}, labels).SetTitleRule(plTR)

		if ins.PacketLoss.CriticalGe > 0 && stats.PacketLoss >= ins.PacketLoss.CriticalGe {
			plEvent.SetEventStatus(types.EventStatusCritical)
		} else if ins.PacketLoss.WarnGe > 0 && stats.PacketLoss >= ins.PacketLoss.WarnGe {
			plEvent.SetEventStatus(types.EventStatusWarning)
		}

		plEvent.SetDescription(fmt.Sprintf(`[MD]
- **target**: %s
- **packet_loss**: %.2f%%
- **warn_threshold**: %.1f%%
- **critical_threshold**: %.1f%%
`, target, stats.PacketLoss, ins.PacketLoss.WarnGe, ins.PacketLoss.CriticalGe))

		q.PushFront(plEvent)
	}

	// RTT threshold check (separate event)
	if ins.Rtt.WarnGe > 0 || ins.Rtt.CriticalGe > 0 {
		rttTR := ins.Rtt.TitleRule
		if rttTR == "" {
			rttTR = "[check] [target]"
		}

		rttEvent := types.BuildEvent(map[string]string{
			"check": "ping::rtt",
		}, labels).SetTitleRule(rttTR)

		if ins.Rtt.CriticalGe > 0 && stats.AvgRtt >= time.Duration(ins.Rtt.CriticalGe) {
			rttEvent.SetEventStatus(types.EventStatusCritical)
		} else if ins.Rtt.WarnGe > 0 && stats.AvgRtt >= time.Duration(ins.Rtt.WarnGe) {
			rttEvent.SetEventStatus(types.EventStatusWarning)
		}

		rttEvent.SetDescription(fmt.Sprintf(`[MD]
- **target**: %s
- **avg_rtt**: %s
- **min_rtt**: %s
- **max_rtt**: %s
- **warn_threshold**: %s
- **critical_threshold**: %s
`, target, stats.AvgRtt, stats.MinRtt, stats.MaxRtt,
			ins.Rtt.WarnGe.HumanString(),
			ins.Rtt.CriticalGe.HumanString()))

		q.PushFront(rttEvent)
	}
}

type pingStats struct {
	ping.Statistics
	ttl int
}

func (ins *Instance) ping(destination string) (*pingStats, error) {
	ps := &pingStats{}

	pinger, err := ping.NewPinger(destination)
	if err != nil {
		return nil, fmt.Errorf("failed to create new pinger: %w", err)
	}

	pinger.SetPrivileged(true)

	if ins.IPv6 != nil && *ins.IPv6 {
		pinger.SetNetwork("ip6")
	}

	pinger.Size = defaultPingDataBytesSize
	if ins.Size != nil {
		pinger.Size = *ins.Size
	}

	pinger.Source = ins.sourceAddress
	pinger.Interval = ins.calcInterval
	pinger.Timeout = ins.calcTimeout

	once := &sync.Once{}
	pinger.OnRecv = func(pkt *ping.Packet) {
		once.Do(func() {
			ps.ttl = pkt.TTL
		})
	}

	pinger.Count = ins.Count
	err = pinger.Run()
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			if runtime.GOOS == "linux" {
				return nil, fmt.Errorf("permission changes required, enable CAP_NET_RAW capabilities (refer to the ping plugin's README.md for more info)")
			}
			return nil, fmt.Errorf("permission changes required, refer to the ping plugin's README.md for more info")
		}
		return nil, fmt.Errorf("%w", err)
	}

	ps.Statistics = *pinger.Statistics()

	return ps, nil
}

func (ins *Instance) buildDesc(target, message string, stats *pingStats) string {
	var b strings.Builder
	b.WriteString("[MD]\n")
	b.WriteString("- **target**: " + target + "\n")
	if stats != nil {
		b.WriteString(fmt.Sprintf("- **packets_sent**: %d\n", stats.PacketsSent))
		b.WriteString(fmt.Sprintf("- **packets_recv**: %d\n", stats.PacketsRecv))
		b.WriteString(fmt.Sprintf("- **packet_loss**: %.2f%%\n", stats.PacketLoss))
		if stats.PacketsRecv > 0 {
			b.WriteString(fmt.Sprintf("- **min_rtt**: %s\n", stats.MinRtt))
			b.WriteString(fmt.Sprintf("- **avg_rtt**: %s\n", stats.AvgRtt))
			b.WriteString(fmt.Sprintf("- **max_rtt**: %s\n", stats.MaxRtt))
			b.WriteString(fmt.Sprintf("- **std_dev_rtt**: %s\n", stats.StdDevRtt))
		}
	}
	b.WriteString("\n**message**:\n\n```\n")
	b.WriteString(message)
	b.WriteString("\n```\n")
	return b.String()
}
