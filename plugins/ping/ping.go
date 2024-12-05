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

type Partial struct {
	ID                         string  `toml:"id"`
	Concurrency                int     `toml:"concurrency"`
	Count                      int     `toml:"count"`         // ping -c <COUNT>
	PingInterval               float64 `toml:"ping_interval"` // ping -i <INTERVAL>
	Timeout                    float64 `toml:"timeout"`       // ping -W <TIMEOUT>
	Interface                  string  `toml:"interface"`     // ping -I/-S <INTERFACE/SRC_ADDR>
	IPv6                       *bool   `toml:"ipv6"`          // Whether to resolve addresses using ipv6 or not.
	Size                       *int    `toml:"size"`          // Packet size
	AlertIfPacketLossPercentGe float64 `toml:"alert_if_packet_loss_percent_ge"`
}

type Instance struct {
	config.InternalConfig
	Partial string `toml:"partial"`

	Targets                    []string `toml:"targets"`
	Concurrency                int      `toml:"concurrency"`
	Count                      int      `toml:"count"`         // ping -c <COUNT>
	PingInterval               float64  `toml:"ping_interval"` // ping -i <INTERVAL>
	Timeout                    float64  `toml:"timeout"`       // ping -W <TIMEOUT>
	Interface                  string   `toml:"interface"`     // ping -I/-S <INTERFACE/SRC_ADDR>
	IPv6                       *bool    `toml:"ipv6"`          // Whether to resolve addresses using ipv6 or not.
	Size                       *int     `toml:"size"`          // Packet size
	AlertIfPacketLossPercentGe float64  `toml:"alert_if_packet_loss_percent_ge"`

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
					// use partial config as default
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

					if p.Instances[i].AlertIfPacketLossPercentGe == 0 {
						p.Instances[i].AlertIfPacketLossPercentGe = partial.AlertIfPacketLossPercentGe
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

	if ins.PingInterval < 0.2 {
		ins.calcInterval = time.Duration(0.2 * float64(time.Second))
	} else {
		ins.calcInterval = time.Duration(ins.PingInterval * float64(time.Second))
	}

	if ins.Timeout == 0 {
		ins.calcTimeout = time.Duration(3) * time.Second
	} else {
		ins.calcTimeout = time.Duration(ins.Timeout * float64(time.Second))
	}

	if ins.Interface != "" {
		if addr := net.ParseIP(ins.Interface); addr != nil {
			ins.sourceAddress = ins.Interface
		} else {
			i, err := net.InterfaceByName(ins.Interface)
			if err != nil {
				return fmt.Errorf("failed to get interface: %v", err)
			}

			addrs, err := i.Addrs()
			if err != nil {
				return fmt.Errorf("failed to get the address of interface: %v", err)
			}

			ins.sourceAddress = addrs[0].(*net.IPNet).IP.String()
		}
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	logger.Logger.Debug("ping... targets: ", ins.Targets)

	if len(ins.Targets) == 0 {
		return
	}

	if !ins.GetInitialized() {
		if err := ins.Init(); err != nil {
			logger.Logger.Errorf("failed to init ping plugin instance: %v", err)
			return
		} else {
			ins.SetInitialized()
		}
	}

	wg := new(sync.WaitGroup)
	se := make(chan struct{}, ins.Concurrency)
	for _, target := range ins.Targets {
		wg.Add(1)
		se <- struct{}{}
		go func(target string) {
			defer func() {
				<-se
				wg.Done()
			}()
			ins.gather(q, target)
		}(target)
	}
	wg.Wait()
	close(se)
}

func (ins *Instance) gather(q *safe.Queue[*types.Event], target string) {
	logger.Logger.Debug("ping target: ", target)

	labels := map[string]string{
		"target": target,
	}

	event := types.BuildEvent(map[string]string{
		"check": "ping check",
	}, labels).SetTitleRule("$check").SetDescription(ins.buildDesc(target, "everything is ok"))

	stats, err := ins.ping(target)
	if err != nil {
		message := fmt.Sprintf("ping %s failed: %v", target, err)
		logger.Logger.Error(message)
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(target, message)))
		return
	}

	if stats.PacketsSent == 0 {
		message := fmt.Sprintf("no packets sent to %s", target)
		logger.Logger.Error(message)
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(target, message)))
		return
	}

	if stats.PacketsRecv == 0 {
		message := fmt.Sprintf("no packets received to %s", target)
		logger.Logger.Error(message)
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(target, message)))
		return
	}

	if stats.PacketLoss >= float64(ins.AlertIfPacketLossPercentGe) {
		message := fmt.Sprintf("packet loss is %f%%", stats.PacketLoss)
		logger.Logger.Error(message)
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(target, message)))
		return
	}

	q.PushFront(event)
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

	// Get Time to live (TTL) of first response, matching original implementation
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

func (ins *Instance) buildDesc(target, message string) string {
	return `[MD]
- target: ` + target + `
- alert_if_packet_loss_percent_ge: ` + fmt.Sprint(ins.AlertIfPacketLossPercentGe) + `


**message**:

` + "```" + `
` + message + `
` + "```" + `
`
}
