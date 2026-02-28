package net

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "net"

type ConnectivityCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type ResponseTimeCheck struct {
	WarnGe     config.Duration `toml:"warn_ge"`
	CriticalGe config.Duration `toml:"critical_ge"`
	TitleRule   string          `toml:"title_rule"`
}

type Partial struct {
	ID           string            `toml:"id"`
	Concurrency  int               `toml:"concurrency"`
	Timeout      config.Duration   `toml:"timeout"`
	ReadTimeout  config.Duration   `toml:"read_timeout"`
	Protocol     string            `toml:"protocol"`
	Send         string            `toml:"send"`
	Expect       string            `toml:"expect"`
	Connectivity ConnectivityCheck `toml:"connectivity"`
	ResponseTime ResponseTimeCheck `toml:"response_time"`
}

type Instance struct {
	config.InternalConfig
	Partial string `toml:"partial"`

	Targets      []string          `toml:"targets"`
	Concurrency  int               `toml:"concurrency"`
	Timeout      config.Duration   `toml:"timeout"`
	ReadTimeout  config.Duration   `toml:"read_timeout"`
	Protocol     string            `toml:"protocol"`
	Send         string            `toml:"send"`
	Expect       string            `toml:"expect"`
	Connectivity ConnectivityCheck `toml:"connectivity"`
	ResponseTime ResponseTimeCheck `toml:"response_time"`
}

type NETPlugin struct {
	config.InternalConfig
	Partials  []Partial   `toml:"partials"`
	Instances []*Instance `toml:"instances"`
}

func (p *NETPlugin) ApplyPartials() error {
	for i := 0; i < len(p.Instances); i++ {
		id := p.Instances[i].Partial
		if id != "" {
			for _, partial := range p.Partials {
				if partial.ID == id {
					if p.Instances[i].Concurrency == 0 {
						p.Instances[i].Concurrency = partial.Concurrency
					}
					if p.Instances[i].Timeout == 0 {
						p.Instances[i].Timeout = partial.Timeout
					}
					if p.Instances[i].ReadTimeout == 0 {
						p.Instances[i].ReadTimeout = partial.ReadTimeout
					}
					if p.Instances[i].Protocol == "" {
						p.Instances[i].Protocol = partial.Protocol
					}
					if p.Instances[i].Send == "" {
						p.Instances[i].Send = partial.Send
					}
					if p.Instances[i].Expect == "" {
						p.Instances[i].Expect = partial.Expect
					}
					if p.Instances[i].Connectivity.Severity == "" {
						p.Instances[i].Connectivity.Severity = partial.Connectivity.Severity
					}
					if p.Instances[i].ResponseTime.WarnGe == 0 {
						p.Instances[i].ResponseTime.WarnGe = partial.ResponseTime.WarnGe
					}
					if p.Instances[i].ResponseTime.CriticalGe == 0 {
						p.Instances[i].ResponseTime.CriticalGe = partial.ResponseTime.CriticalGe
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
		return &NETPlugin{}
	})
}

func (p *NETPlugin) GetInstances() []plugins.Instance {
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
		ins.Timeout = config.Duration(time.Second)
	}
	if ins.ReadTimeout == 0 {
		ins.ReadTimeout = config.Duration(time.Second)
	}
	if ins.Protocol == "" {
		ins.Protocol = "tcp"
	}
	if ins.Protocol != "tcp" && ins.Protocol != "udp" {
		return errors.New("bad protocol, only tcp and udp are supported")
	}
	if ins.Protocol == "udp" && ins.Send == "" {
		return errors.New("send string cannot be empty when protocol is udp")
	}
	if ins.Protocol == "udp" && ins.Expect == "" {
		return errors.New("expected string cannot be empty when protocol is udp")
	}

	if ins.Connectivity.Severity == "" {
		ins.Connectivity.Severity = types.EventStatusCritical
	}

	if ins.ResponseTime.WarnGe > 0 && ins.ResponseTime.CriticalGe > 0 {
		if ins.ResponseTime.WarnGe >= ins.ResponseTime.CriticalGe {
			return fmt.Errorf("response_time.warn_ge(%s) must be less than response_time.critical_ge(%s)",
				time.Duration(ins.ResponseTime.WarnGe), time.Duration(ins.ResponseTime.CriticalGe))
		}
	}

	for i := 0; i < len(ins.Targets); i++ {
		target := ins.Targets[i]
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			return fmt.Errorf("failed to split host port, target: %s, error: %v", target, err)
		}
		if host == "" {
			ins.Targets[i] = "localhost:" + port
		}
		if port == "" {
			return errors.New("bad port, target: " + target)
		}
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	logger.Logger.Debugw("net gather", "targets", ins.Targets)

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
					logger.Logger.Errorw("panic in net gather goroutine", "target", target, "recover", r)
					q.PushFront(types.BuildEvent(map[string]string{
						"check":  "net::connectivity",
						"target": target,
					}).SetTitleRule("[check] [target]").
						SetEventStatus(types.EventStatusCritical).
						SetDescription(fmt.Sprintf("panic during check: %v", r)))
				}
				se.Release()
				wg.Done()
			}()
			ins.gather(q, target)
		}(target)
	}
	wg.Wait()
}

func (ins *Instance) gather(q *safe.Queue[*types.Event], target string) {
	logger.Logger.Debugw("net target", "target", target)

	labels := map[string]string{
		"target":   target,
		"protocol": ins.Protocol,
	}

	switch ins.Protocol {
	case "tcp":
		ins.TCPGather(target, labels, q)
	case "udp":
		ins.UDPGather(target, labels, q)
	}
}

func (ins *Instance) TCPGather(address string, labels map[string]string, q *safe.Queue[*types.Event]) {
	connTR := ins.Connectivity.TitleRule
	if connTR == "" {
		connTR = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check": "net::connectivity",
	}, labels).SetTitleRule(connTR)

	start := time.Now()

	conn, err := net.DialTimeout("tcp", address, time.Duration(ins.Timeout))
	if err != nil {
		event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
		q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("connection error: %v", err)))
		logger.Logger.Errorw("failed to send tcp request", "error", err, "plugin", pluginName, "target", address)
		return
	}

	defer conn.Close()

	if ins.Send != "" {
		if _, err = conn.Write([]byte(ins.Send)); err != nil {
			event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
			q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
				SetDescription(fmt.Sprintf("failed to send message: %s, error: %v", ins.Send, err)))
			return
		}
	}

	if ins.Expect != "" {
		if err := conn.SetReadDeadline(time.Now().Add(time.Duration(ins.ReadTimeout))); err != nil {
			event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
			q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
				SetDescription(fmt.Sprintf("failed to set read deadline, error: %v", err)))
			return
		}

		const maxResponseSize = 65536
		var dataBuf bytes.Buffer
		tmp := make([]byte, 4096)
		for dataBuf.Len() < maxResponseSize {
			n, readErr := conn.Read(tmp)
			if n > 0 {
				dataBuf.Write(tmp[:n])
				if strings.Contains(dataBuf.String(), ins.Expect) {
					break
				}
			}
			if readErr != nil {
				break
			}
		}
		data := dataBuf.String()

		if !strings.Contains(data, ins.Expect) {
			event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
			q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
				SetDescription(fmt.Sprintf("response mismatch. expected: %s, real response: %s", ins.Expect, truncateStr(data, maxResponseDisplaySize))))
			return
		}
	}

	responseTime := time.Since(start)
	event.Labels[types.AttrPrefix+"response_time"] = responseTime.String()
	event.SetDescription("everything is ok")
	q.PushFront(event)

	ins.checkResponseTime(q, address, labels, responseTime)
}

func (ins *Instance) UDPGather(address string, labels map[string]string, q *safe.Queue[*types.Event]) {
	connTR := ins.Connectivity.TitleRule
	if connTR == "" {
		connTR = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check": "net::connectivity",
	}, labels).SetTitleRule(connTR)

	start := time.Now()

	udpAddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
		q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("resolve udp address(%s) error: %v", address, err)))
		logger.Logger.Errorw("resolve udp address fail", "address", address, "error", err)
		return
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
		q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("dial udp address(%s) error: %v", address, err)))
		logger.Logger.Errorw("dial udp address fail", "address", address, "error", err)
		return
	}

	defer conn.Close()

	if _, err = conn.Write([]byte(ins.Send)); err != nil {
		event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
		q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("write string(%s) to udp address(%s) error: %v", ins.Send, address, err)))
		logger.Logger.Errorw("write to udp address fail", "address", address, "send", ins.Send, "error", err)
		return
	}

	if err = conn.SetReadDeadline(time.Now().Add(time.Duration(ins.ReadTimeout))); err != nil {
		event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
		q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("set connection deadline to udp address(%s) error: %v", address, err)))
		logger.Logger.Errorw("set udp read deadline fail", "address", address, "error", err)
		return
	}

	buf := make([]byte, 65536)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
		q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("read from udp address(%s) error: %v", address, err)))
		logger.Logger.Errorw("read from udp address fail", "address", address, "error", err)
		return
	}

	data := string(buf[:n])
	if !strings.Contains(data, ins.Expect) {
		event.Labels[types.AttrPrefix+"response_time"] = time.Since(start).String()
		q.PushFront(event.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("response mismatch. expect: %s, real: %s", ins.Expect, truncateStr(data, maxResponseDisplaySize))))
		logger.Logger.Errorw("udp response mismatch", "address", address, "expect", ins.Expect, "actual", truncateStr(data, maxResponseDisplaySize))
		return
	}

	responseTime := time.Since(start)
	event.Labels[types.AttrPrefix+"response_time"] = responseTime.String()
	event.SetDescription("everything is ok")
	q.PushFront(event)

	ins.checkResponseTime(q, address, labels, responseTime)
}

func (ins *Instance) checkResponseTime(q *safe.Queue[*types.Event], address string, labels map[string]string, responseTime time.Duration) {
	if ins.ResponseTime.WarnGe == 0 && ins.ResponseTime.CriticalGe == 0 {
		return
	}

	tr := ins.ResponseTime.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	rtEvent := types.BuildEvent(map[string]string{
		"check":                                    "net::response_time",
		types.AttrPrefix + "response_time":         responseTime.String(),
		types.AttrPrefix + "warn_threshold":        ins.ResponseTime.WarnGe.HumanString(),
		types.AttrPrefix + "critical_threshold":    ins.ResponseTime.CriticalGe.HumanString(),
	}, labels).SetTitleRule(tr)

	if ins.ResponseTime.CriticalGe > 0 && responseTime >= time.Duration(ins.ResponseTime.CriticalGe) {
		rtEvent.SetEventStatus(types.EventStatusCritical)
		rtEvent.SetDescription(fmt.Sprintf("response time %s >= critical threshold %s", responseTime, ins.ResponseTime.CriticalGe.HumanString()))
	} else if ins.ResponseTime.WarnGe > 0 && responseTime >= time.Duration(ins.ResponseTime.WarnGe) {
		rtEvent.SetEventStatus(types.EventStatusWarning)
		rtEvent.SetDescription(fmt.Sprintf("response time %s >= warning threshold %s", responseTime, ins.ResponseTime.WarnGe.HumanString()))
	} else {
		rtEvent.SetDescription(fmt.Sprintf("response time %s, everything is ok", responseTime))
	}

	q.PushFront(rtEvent)
}

const maxResponseDisplaySize = 512

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "... (truncated)"
}

