package net

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "net"

type Instance struct {
	config.InternalConfig

	Targets     []string        `toml:"targets"`
	Concurrency int             `toml:"concurrency"`
	Timeout     config.Duration `toml:"timeout"`
	ReadTimeout config.Duration `toml:"read_timeout"`
	Protocol    string          `toml:"protocol"`
	Send        string          `toml:"send"`
	Expect      string          `toml:"expect"`
}

type NET struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *NET) IsSystemPlugin() bool {
	return false
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &NET{}
	})
}

func (p *NET) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func (ins *Instance) Init() error {
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
	logger.Logger.Debug("net gather, targets: ", ins.Targets)

	if len(ins.Targets) == 0 {
		return
	}

	if !ins.GetInitialized() {
		if err := ins.Init(); err != nil {
			logger.Logger.Errorf("failed to init net plugin instance: %v", err)
			return
		} else {
			ins.SetInitialized()
		}
	}

	wg := new(sync.WaitGroup)
	se := semaphore.NewSemaphore(ins.Concurrency)
	for _, target := range ins.Targets {
		wg.Add(1)
		se.Acquire()
		go func(target string) {
			defer func() {
				se.Release()
				wg.Done()
			}()
			ins.gather(q, target)
		}(target)
	}
	wg.Wait()
}

func (ins *Instance) gather(q *safe.Queue[*types.Event], target string) {
	logger.Logger.Debug("net target: ", target)

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
	event := types.BuildEvent(map[string]string{
		"check": "tcp check",
	}, labels).SetTitleRule("$check").SetDescription(ins.buildDesc(address, "everything is ok"))

	conn, err := net.DialTimeout("tcp", address, time.Duration(ins.Timeout))
	if err != nil {
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, fmt.Sprintf("connection error: %v", err))))
		logger.Logger.Errorw("failed to send tcp request", "error", err, "plugin", pluginName, "target", address)
		return
	}

	defer conn.Close()

	// check expect string
	if ins.Send == "" {
		// no need check send and expect
		q.PushFront(event)
		return
	}

	msg := []byte(ins.Send)
	if _, err = conn.Write(msg); err != nil {
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, fmt.Sprintf("failed to send message: %s, error: %v", ins.Send, err))))
		return
	}

	// Read string if needed
	if ins.Expect != "" {
		// Set read timeout
		if err := conn.SetReadDeadline(time.Now().Add(time.Duration(ins.ReadTimeout))); err != nil {
			q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, fmt.Sprintf("failed to set read deadline, error: %v", err))))
			return
		}

		// Prepare reader
		reader := bufio.NewReader(conn)
		tp := textproto.NewReader(reader)
		// Read
		data, err := tp.ReadLine()
		if err != nil {
			q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, fmt.Sprintf("failed to read response line, error: %v", err))))
			return
		}

		if !strings.Contains(data, ins.Expect) {
			q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, fmt.Sprintf("response mismatch. expected: %s, real response: %s", ins.Expect, data))))
			return
		}
	}

	q.PushFront(event)
}

func (ins *Instance) UDPGather(address string, labels map[string]string, q *safe.Queue[*types.Event]) {
	event := types.BuildEvent(map[string]string{
		"check": "udp check",
	}, labels).SetTitleRule("$check").SetDescription(ins.buildDesc(address, "everything is ok"))

	udpAddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		message := fmt.Sprintf("resolve udp address(%s) error: %v", address, err)
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, message)))
		logger.Logger.Error(message)
		return
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		message := fmt.Sprintf("dial udp address(%s) error: %v", address, err)
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, message)))
		logger.Logger.Error(message)
		return
	}

	defer conn.Close()

	if _, err = conn.Write([]byte(ins.Send)); err != nil {
		message := fmt.Sprintf("write string(%s) to udp address(%s) error: %v", ins.Send, address, err)
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, message)))
		logger.Logger.Error(message)
		return
	}

	if err = conn.SetReadDeadline(time.Now().Add(time.Duration(ins.ReadTimeout))); err != nil {
		message := fmt.Sprintf("set connection deadline to udp address(%s) error: %v", address, err)
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, message)))
		logger.Logger.Error(message)
		return
	}

	// Read
	buf := make([]byte, 1024)
	if _, _, err = conn.ReadFromUDP(buf); err != nil {
		message := fmt.Sprintf("read from udp address(%s) error: %v", address, err)
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, message)))
		logger.Logger.Error(message)
		return
	}

	if !strings.Contains(string(buf), ins.Expect) {
		message := fmt.Sprintf("response mismatch. expect: %s, real: %s", ins.Expect, string(buf))
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(ins.buildDesc(address, message)))
		logger.Logger.Error(message)
		return
	}

	q.PushFront(event)
}

func (ins *Instance) buildDesc(target, message string) string {
	return `[MD]
` + message + `

- **target**: ` + target + `
- **protocol**: ` + ins.Protocol + `
- **config.send**: ` + ins.Send + `
- **config.expect**:` + ins.Expect + `
`
}
