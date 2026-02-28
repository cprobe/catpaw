package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sort"
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

const pluginName = "dns"

type ResolutionCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type ResponseTimeCheck struct {
	WarnGe     config.Duration `toml:"warn_ge"`
	CriticalGe config.Duration `toml:"critical_ge"`
	TitleRule   string          `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	Targets      []string        `toml:"targets"`
	Servers      []string        `toml:"servers"`
	ExpectedIPs  []string        `toml:"expected_ips"`
	Timeout      config.Duration `toml:"timeout"`
	Concurrency  int             `toml:"concurrency"`
	Resolution   ResolutionCheck `toml:"resolution"`
	ResponseTime ResponseTimeCheck `toml:"response_time"`

	resolver     *net.Resolver
	expectedSet  map[string]struct{}
	serverLabel  string
}

type DNSPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *DNSPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &DNSPlugin{}
	})
}

func (ins *Instance) Init() error {
	if len(ins.Targets) == 0 {
		return fmt.Errorf("targets must not be empty")
	}

	for _, t := range ins.Targets {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("target must not be empty or blank")
		}
	}

	if ins.Resolution.Severity == "" {
		ins.Resolution.Severity = types.EventStatusCritical
	} else if !types.EventStatusValid(ins.Resolution.Severity) {
		return fmt.Errorf("invalid resolution.severity %q", ins.Resolution.Severity)
	}

	if ins.ResponseTime.WarnGe > 0 && ins.ResponseTime.CriticalGe > 0 &&
		ins.ResponseTime.WarnGe >= ins.ResponseTime.CriticalGe {
		return fmt.Errorf("response_time.warn_ge(%s) must be less than response_time.critical_ge(%s)",
			time.Duration(ins.ResponseTime.WarnGe), time.Duration(ins.ResponseTime.CriticalGe))
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(5 * time.Second)
	}

	if ins.Concurrency == 0 {
		ins.Concurrency = 10
	}

	// Build expected IP lookup set
	if len(ins.ExpectedIPs) > 0 {
		ins.expectedSet = make(map[string]struct{}, len(ins.ExpectedIPs))
		for _, ip := range ins.ExpectedIPs {
			parsed := net.ParseIP(strings.TrimSpace(ip))
			if parsed == nil {
				return fmt.Errorf("invalid expected IP: %q", ip)
			}
			ins.expectedSet[parsed.String()] = struct{}{}
		}
	}

	// Validate and build resolver
	if len(ins.Servers) > 0 {
		for _, s := range ins.Servers {
			if net.ParseIP(s) == nil {
				host, _, err := net.SplitHostPort(s)
				if err != nil || net.ParseIP(host) == nil {
					return fmt.Errorf("invalid DNS server address %q (expected IP or IP:port)", s)
				}
			}
		}

		if runtime.GOOS == "windows" {
			logger.Logger.Warnw("dns: custom servers may not work on Windows due to Go resolver limitations")
		}

		ins.serverLabel = strings.Join(ins.Servers, ",")
		ins.resolver = buildCustomResolver(ins.Servers, time.Duration(ins.Timeout))
	} else {
		ins.serverLabel = "system"
		ins.resolver = net.DefaultResolver
	}

	return nil
}

func buildCustomResolver(servers []string, timeout time.Duration) *net.Resolver {
	addrs := make([]string, len(servers))
	for i, s := range servers {
		if net.ParseIP(s) != nil {
			addrs[i] = net.JoinHostPort(s, "53")
		} else {
			addrs[i] = s
		}
	}

	idx := 0
	var mu sync.Mutex

	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			addr := addrs[idx%len(addrs)]
			idx++
			mu.Unlock()

			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "udp", addr)
		},
	}
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
					logger.Logger.Errorw("panic in dns gather goroutine", "target", target, "recover", r)
					q.PushFront(types.BuildEvent(map[string]string{
						"check":  "dns::resolution",
						"target": target,
					}).SetTitleRule("[check] [target]").
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
	logger.Logger.Debugw("dns resolving", "target", target, "server", ins.serverLabel)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ins.Timeout))
	defer cancel()

	start := time.Now()
	ips, err := ins.resolver.LookupHost(ctx, target)
	responseTime := time.Since(start)

	sort.Strings(ips)
	resolvedStr := strings.Join(ips, ",")

	logger.Logger.Debugw("dns resolve result",
		"target", target,
		"ips", resolvedStr,
		"response_time", responseTime,
		"error", err,
	)

	baseLabels := map[string]string{
		"target": target,
	}

	attrLabels := map[string]string{
		types.AttrPrefix + "server":        ins.serverLabel,
		types.AttrPrefix + "response_time": responseTime.String(),
	}
	if resolvedStr != "" {
		attrLabels[types.AttrPrefix+"resolved_ips"] = resolvedStr
	}

	// --- resolution check ---
	ins.checkResolution(q, target, ips, err, responseTime, baseLabels, attrLabels)

	if err != nil {
		return
	}

	// --- expected IPs check ---
	if len(ins.expectedSet) > 0 {
		ins.checkExpectedIPs(q, target, ips, baseLabels, attrLabels)
	}

	// --- response time check ---
	ins.checkResponseTime(q, target, responseTime, baseLabels, attrLabels)
}

func (ins *Instance) checkResolution(q *safe.Queue[*types.Event], target string, ips []string, err error, rt time.Duration, baseLabels, attrLabels map[string]string) {
	tr := ins.Resolution.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(mergeMaps(map[string]string{
		"check": "dns::resolution",
	}, baseLabels), attrLabels).SetTitleRule(tr)

	if err != nil {
		event.SetEventStatus(ins.Resolution.Severity)
		event.SetDescription(fmt.Sprintf("DNS resolution failed: %v", classifyDNSError(err)))
		q.PushFront(event)
		return
	}

	event.SetDescription(fmt.Sprintf("resolved to [%s] in %s", strings.Join(ips, ", "), rt))
	q.PushFront(event)
}

func (ins *Instance) checkExpectedIPs(q *safe.Queue[*types.Event], target string, ips []string, baseLabels, attrLabels map[string]string) {
	tr := ins.Resolution.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	extraAttrs := map[string]string{
		types.AttrPrefix + "expected_ips": strings.Join(ins.ExpectedIPs, ","),
	}

	event := types.BuildEvent(mergeMaps(map[string]string{
		"check": "dns::expected_ips",
	}, baseLabels), attrLabels, extraAttrs).SetTitleRule(tr)

	found := false
	for _, ip := range ips {
		if _, ok := ins.expectedSet[ip]; ok {
			found = true
			break
		}
	}

	if !found {
		event.SetEventStatus(ins.Resolution.Severity)
		event.SetDescription(fmt.Sprintf("resolved to [%s], none of expected IPs [%s] found",
			strings.Join(ips, ", "), strings.Join(ins.ExpectedIPs, ", ")))
		q.PushFront(event)
		return
	}

	event.SetDescription(fmt.Sprintf("resolved to [%s], matches expected IPs", strings.Join(ips, ", ")))
	q.PushFront(event)
}

func (ins *Instance) checkResponseTime(q *safe.Queue[*types.Event], target string, responseTime time.Duration, baseLabels, attrLabels map[string]string) {
	if ins.ResponseTime.WarnGe == 0 && ins.ResponseTime.CriticalGe == 0 {
		return
	}

	tr := ins.ResponseTime.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	extraAttrs := map[string]string{}
	if ins.ResponseTime.WarnGe > 0 {
		extraAttrs[types.AttrPrefix+"warn_threshold"] = time.Duration(ins.ResponseTime.WarnGe).String()
	}
	if ins.ResponseTime.CriticalGe > 0 {
		extraAttrs[types.AttrPrefix+"critical_threshold"] = time.Duration(ins.ResponseTime.CriticalGe).String()
	}

	event := types.BuildEvent(mergeMaps(map[string]string{
		"check": "dns::response_time",
	}, baseLabels), attrLabels, extraAttrs).SetTitleRule(tr)

	if ins.ResponseTime.CriticalGe > 0 && responseTime >= time.Duration(ins.ResponseTime.CriticalGe) {
		event.SetEventStatus(types.EventStatusCritical)
		event.SetDescription(fmt.Sprintf("DNS response time %s >= critical threshold %s",
			responseTime, time.Duration(ins.ResponseTime.CriticalGe)))
		q.PushFront(event)
		return
	}

	if ins.ResponseTime.WarnGe > 0 && responseTime >= time.Duration(ins.ResponseTime.WarnGe) {
		event.SetEventStatus(types.EventStatusWarning)
		event.SetDescription(fmt.Sprintf("DNS response time %s >= warning threshold %s",
			responseTime, time.Duration(ins.ResponseTime.WarnGe)))
		q.PushFront(event)
		return
	}

	event.SetDescription(fmt.Sprintf("DNS response time %s, everything is ok", responseTime))
	q.PushFront(event)
}

// classifyDNSError extracts a human-friendly message from DNS errors.
func classifyDNSError(err error) string {
	if err == nil {
		return ""
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return fmt.Sprintf("NXDOMAIN (domain not found): %s", dnsErr.Name)
		}
		if dnsErr.IsTimeout {
			return fmt.Sprintf("timeout resolving %s (server: %s)", dnsErr.Name, dnsErr.Server)
		}
		if dnsErr.IsTemporary {
			return fmt.Sprintf("temporary failure resolving %s: %v", dnsErr.Name, dnsErr.Err)
		}
		return fmt.Sprintf("%s (server: %s)", dnsErr.Err, dnsErr.Server)
	}

	return err.Error()
}

func mergeMaps(maps ...map[string]string) map[string]string {
	total := 0
	for _, m := range maps {
		total += len(m)
	}
	result := make(map[string]string, total)
	for _, m := range maps {
		for k, v := range m {
			result[k] = v
		}
	}
	return result
}
