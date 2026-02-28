package http

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/filter"
	"github.com/cprobe/catpaw/pkg/netx"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "http"

const (
	maxBodyReadSize    = 1 << 20 // 1MB max read from response body
	maxBodyDisplaySize = 1024    // 1KB max in alert description
)

type ConnectivityCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type ResponseTimeCheck struct {
	WarnGe     config.Duration `toml:"warn_ge"`
	CriticalGe config.Duration `toml:"critical_ge"`
	TitleRule   string          `toml:"title_rule"`
}

type CertExpiryCheck struct {
	WarnWithin     config.Duration `toml:"warn_within"`
	CriticalWithin config.Duration `toml:"critical_within"`
	TitleRule       string          `toml:"title_rule"`
}

type StatusCodeCheck struct {
	Expect    []string      `toml:"expect"`
	Severity  string        `toml:"severity"`
	TitleRule string        `toml:"title_rule"`
	filter    filter.Filter
}

type ResponseBodyCheck struct {
	ExpectSubstring string `toml:"expect_substring"`
	ExpectRegex     string `toml:"expect_regex"`
	Severity        string `toml:"severity"`
	TitleRule       string `toml:"title_rule"`
	compiledRegex   *regexp.Regexp
}

type Partial struct {
	ID          string `toml:"id"`
	Concurrency int    `toml:"concurrency"`
	config.HTTPConfig
}

type Instance struct {
	config.InternalConfig
	Partial string `toml:"partial"`

	Targets      []string          `toml:"targets"`
	Concurrency  int               `toml:"concurrency"`
	Connectivity ConnectivityCheck `toml:"connectivity"`
	ResponseTime ResponseTimeCheck `toml:"response_time"`
	CertExpiry   CertExpiryCheck   `toml:"cert_expiry"`
	StatusCode   StatusCodeCheck   `toml:"status_code"`
	ResponseBody ResponseBodyCheck `toml:"response_body"`

	config.HTTPConfig
	client httpClient
}

type HttpPlugin struct {
	config.InternalConfig
	Partials  []Partial   `toml:"partials"`
	Instances []*Instance `toml:"instances"`
}

func (p *HttpPlugin) ApplyPartials() error {
	for i := 0; i < len(p.Instances); i++ {
		id := p.Instances[i].Partial
		if id != "" {
			for _, partial := range p.Partials {
				if partial.ID == id {
					if p.Instances[i].Concurrency == 0 {
						p.Instances[i].Concurrency = partial.Concurrency
					}
					if p.Instances[i].HTTPConfig.HTTPProxy == "" {
						p.Instances[i].HTTPConfig.HTTPProxy = partial.HTTPProxy
					}
					if p.Instances[i].HTTPConfig.Interface == "" {
						p.Instances[i].HTTPConfig.Interface = partial.Interface
					}
					if p.Instances[i].HTTPConfig.Method == "" {
						p.Instances[i].HTTPConfig.Method = partial.Method
					}
					if p.Instances[i].HTTPConfig.Timeout == 0 {
						p.Instances[i].HTTPConfig.Timeout = partial.Timeout
					}
					if p.Instances[i].HTTPConfig.FollowRedirects == nil {
						p.Instances[i].HTTPConfig.FollowRedirects = partial.FollowRedirects
					}
					if p.Instances[i].HTTPConfig.BasicAuthUser == "" {
						p.Instances[i].HTTPConfig.BasicAuthUser = partial.BasicAuthUser
					}
					if p.Instances[i].HTTPConfig.BasicAuthPass == "" {
						p.Instances[i].HTTPConfig.BasicAuthPass = partial.BasicAuthPass
					}
					if len(p.Instances[i].HTTPConfig.Headers) == 0 {
						p.Instances[i].HTTPConfig.Headers = partial.Headers
					}
					if p.Instances[i].HTTPConfig.Payload == "" {
						p.Instances[i].HTTPConfig.Payload = partial.Payload
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
		return &HttpPlugin{}
	})
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func (ins *Instance) Init() error {
	if ins.Concurrency == 0 {
		ins.Concurrency = 10
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

	if ins.CertExpiry.WarnWithin > 0 && ins.CertExpiry.CriticalWithin > 0 {
		if ins.CertExpiry.CriticalWithin >= ins.CertExpiry.WarnWithin {
			return fmt.Errorf("cert_expiry.critical_within(%s) must be less than cert_expiry.warn_within(%s)",
				time.Duration(ins.CertExpiry.CriticalWithin), time.Duration(ins.CertExpiry.WarnWithin))
		}
	}

	if len(ins.StatusCode.Expect) > 0 {
		if ins.StatusCode.Severity == "" {
			ins.StatusCode.Severity = types.EventStatusWarning
		}
		var err error
		ins.StatusCode.filter, err = filter.Compile(ins.StatusCode.Expect)
		if err != nil {
			return fmt.Errorf("failed to compile status_code.expect: %v", err)
		}
	}

	if ins.ResponseBody.ExpectSubstring != "" && ins.ResponseBody.ExpectRegex != "" {
		return fmt.Errorf("response_body: expect_substring and expect_regex are mutually exclusive")
	}
	if ins.ResponseBody.ExpectSubstring != "" && ins.ResponseBody.Severity == "" {
		ins.ResponseBody.Severity = types.EventStatusWarning
	}
	if ins.ResponseBody.ExpectRegex != "" {
		compiled, err := regexp.Compile(ins.ResponseBody.ExpectRegex)
		if err != nil {
			return fmt.Errorf("failed to compile response_body.expect_regex: %v", err)
		}
		ins.ResponseBody.compiledRegex = compiled
		if ins.ResponseBody.Severity == "" {
			ins.ResponseBody.Severity = types.EventStatusWarning
		}
	}

	if len(ins.Headers) > 0 && len(ins.Headers)%2 != 0 {
		return fmt.Errorf("headers must be key-value pairs (even number of elements), got %d", len(ins.Headers))
	}

	for _, target := range ins.Targets {
		addr, err := url.Parse(target)
		if err != nil {
			return fmt.Errorf("failed to parse http url: %s, error: %v", target, err)
		}
		if addr.Scheme != "http" && addr.Scheme != "https" {
			return fmt.Errorf("only http and https are supported, target: %s", target)
		}
		if addr.Scheme == "https" && !ins.UseTLS {
			ins.UseTLS = true
		}
	}

	client, err := ins.createHTTPClient()
	if err != nil {
		return fmt.Errorf("failed to create http client: %v", err)
	}
	ins.client = client

	return nil
}

func (ins *Instance) createHTTPClient() (*http.Client, error) {
	tlsCfg, err := ins.ClientConfig.TLSConfig()
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{}

	if ins.Interface != "" {
		dialer.LocalAddr, err = netx.LocalAddressByInterfaceName(ins.Interface)
		if err != nil {
			return nil, err
		}
	}

	proxy, err := ins.GetProxy()
	if err != nil {
		return nil, err
	}

	trans := &http.Transport{
		Proxy:             proxy,
		DialContext:       dialer.DialContext,
		DisableKeepAlives: true,
		TLSClientConfig:   tlsCfg,
	}

	client := &http.Client{
		Transport: trans,
		Timeout:   time.Duration(ins.GetTimeout()),
	}

	if ins.FollowRedirects != nil && !*ins.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	return client, nil
}

func (h *HttpPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(h.Instances))
	for i := 0; i < len(h.Instances); i++ {
		ret[i] = h.Instances[i]
	}
	return ret
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
					logger.Logger.Errorw("panic in http gather goroutine", "target", target, "recover", r)
					q.PushFront(types.BuildEvent(map[string]string{
						"check":  "http::connectivity",
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
	logger.Logger.Debugw("http target", "target", target)

	labels := map[string]string{
		"target": target,
		"method": ins.GetMethod(),
	}

	var payload io.Reader
	if ins.Payload != "" {
		payload = strings.NewReader(ins.Payload)
	}

	request, err := http.NewRequest(ins.GetMethod(), target, payload)
	if err != nil {
		connTR := ins.Connectivity.TitleRule
		if connTR == "" {
			connTR = "[check] [target]"
		}
		connEvent := types.BuildEvent(map[string]string{
			"check": "http::connectivity",
		}, labels).SetTitleRule(connTR)
		connEvent.SetEventStatus(ins.Connectivity.Severity)
		connEvent.SetDescription(fmt.Sprintf("failed to create request: %v", err))
		q.PushFront(connEvent)
		return
	}

	for i := 0; i < len(ins.Headers); i += 2 {
		request.Header.Add(ins.Headers[i], ins.Headers[i+1])
		if ins.Headers[i] == "Host" {
			request.Host = ins.Headers[i+1]
		}
	}

	if ins.BasicAuthUser != "" || ins.BasicAuthPass != "" {
		request.SetBasicAuth(ins.BasicAuthUser, ins.BasicAuthPass)
	}

	// connectivity check
	connTR := ins.Connectivity.TitleRule
	if connTR == "" {
		connTR = "[check] [target]"
	}

	start := time.Now()
	resp, err := ins.client.Do(request)
	responseTime := time.Since(start)

	connEvent := types.BuildEvent(map[string]string{
		"check": "http::connectivity",
		types.AttrPrefix + "response_time": responseTime.String(),
	}, labels).SetTitleRule(connTR)

	if err != nil {
		connEvent.SetEventStatus(ins.Connectivity.Severity)
		connEvent.SetDescription(err.Error())
	} else {
		connEvent.SetDescription("everything is ok")
	}

	q.PushFront(connEvent)

	if err != nil {
		logger.Logger.Errorw("failed to send http request", "error", err, "plugin", pluginName, "target", target)
		return
	}

	defer resp.Body.Close()

	// response time check
	if ins.ResponseTime.WarnGe > 0 || ins.ResponseTime.CriticalGe > 0 {
		rtTR := ins.ResponseTime.TitleRule
		if rtTR == "" {
			rtTR = "[check] [target]"
		}

		rtEvent := types.BuildEvent(map[string]string{
			"check":                                    "http::response_time",
			types.AttrPrefix + "response_time":         responseTime.String(),
			types.AttrPrefix + "warn_threshold":        ins.ResponseTime.WarnGe.HumanString(),
			types.AttrPrefix + "critical_threshold":    ins.ResponseTime.CriticalGe.HumanString(),
		}, labels).SetTitleRule(rtTR)

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

	// cert expiry check
	if (ins.CertExpiry.WarnWithin > 0 || ins.CertExpiry.CriticalWithin > 0) &&
		strings.HasPrefix(target, "https://") && resp.TLS != nil {

		certTR := ins.CertExpiry.TitleRule
		if certTR == "" {
			certTR = "[check] [target]"
		}

		certEvent := types.BuildEvent(map[string]string{
			"check": "http::cert_expiry",
		}, labels).SetTitleRule(certTR)

		certExpiry := getEarliestCertExpiry(resp.TLS)

		if certExpiry.IsZero() {
			certEvent.SetEventStatus(types.EventStatusCritical)
			certEvent.SetDescription("no peer certificates found in TLS connection")
		} else {
			timeUntilExpiry := time.Until(certExpiry)
			certEvent.Labels[types.AttrPrefix+"cert_expires_at"] = certExpiry.Format("2006-01-02 15:04:05")
			certEvent.Labels[types.AttrPrefix+"time_until_expiry"] = timeUntilExpiry.Truncate(time.Minute).String()
			certEvent.Labels[types.AttrPrefix+"warn_within"] = ins.CertExpiry.WarnWithin.HumanString()
			certEvent.Labels[types.AttrPrefix+"critical_within"] = ins.CertExpiry.CriticalWithin.HumanString()

			if ins.CertExpiry.CriticalWithin > 0 && timeUntilExpiry <= time.Duration(ins.CertExpiry.CriticalWithin) {
				certEvent.SetEventStatus(types.EventStatusCritical)
				certEvent.SetDescription(fmt.Sprintf("cert expires in %s, within critical threshold %s",
					timeUntilExpiry.Truncate(time.Minute), ins.CertExpiry.CriticalWithin.HumanString()))
			} else if ins.CertExpiry.WarnWithin > 0 && timeUntilExpiry <= time.Duration(ins.CertExpiry.WarnWithin) {
				certEvent.SetEventStatus(types.EventStatusWarning)
				certEvent.SetDescription(fmt.Sprintf("cert expires in %s, within warning threshold %s",
					timeUntilExpiry.Truncate(time.Minute), ins.CertExpiry.WarnWithin.HumanString()))
			} else {
				certEvent.SetDescription(fmt.Sprintf("cert expires at %s, everything is ok", certExpiry.Format("2006-01-02 15:04:05")))
			}
		}

		q.PushFront(certEvent)
	}

	statusCode := fmt.Sprint(resp.StatusCode)
	needBody := len(ins.StatusCode.Expect) > 0 ||
		ins.ResponseBody.ExpectSubstring != "" ||
		ins.ResponseBody.compiledRegex != nil

	var body []byte
	if needBody {
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxBodyReadSize))
		if err != nil {
			logger.Logger.Errorw("failed to read http response body", "error", err, "plugin", pluginName, "target", target)
		}
	}

	// status code check
	if len(ins.StatusCode.Expect) > 0 {
		scTR := ins.StatusCode.TitleRule
		if scTR == "" {
			scTR = "[check] [target]"
		}

		scEvent := types.BuildEvent(map[string]string{
			"check":                                "http::status_code",
			types.AttrPrefix + "status_code":       statusCode,
			types.AttrPrefix + "expect_code":       fmt.Sprintf("%v", ins.StatusCode.Expect),
			types.AttrPrefix + "response_body":     truncateBody(body, maxBodyDisplaySize),
		}, labels).SetTitleRule(scTR)

		if !ins.StatusCode.filter.Match(statusCode) {
			scEvent.SetEventStatus(ins.StatusCode.Severity)
			scEvent.SetDescription(fmt.Sprintf("status code %s does not match expected %v", statusCode, ins.StatusCode.Expect))
		} else {
			scEvent.SetDescription(fmt.Sprintf("status code %s matches expected %v", statusCode, ins.StatusCode.Expect))
		}

		q.PushFront(scEvent)
	}

	// response body check
	if ins.ResponseBody.ExpectSubstring != "" || ins.ResponseBody.compiledRegex != nil {
		rbTR := ins.ResponseBody.TitleRule
		if rbTR == "" {
			rbTR = "[check] [target]"
		}

		rbEvent := types.BuildEvent(map[string]string{
			"check":                                "http::response_body",
			types.AttrPrefix + "status_code":       statusCode,
			types.AttrPrefix + "response_body":     truncateBody(body, maxBodyDisplaySize),
		}, labels).SetTitleRule(rbTR)

		var matched bool
		var expectDesc string
		if ins.ResponseBody.compiledRegex != nil {
			matched = ins.ResponseBody.compiledRegex.Match(body)
			expectDesc = fmt.Sprintf("expect_regex: %s", ins.ResponseBody.ExpectRegex)
			rbEvent.Labels[types.AttrPrefix+"expect_regex"] = ins.ResponseBody.ExpectRegex
		} else {
			matched = strings.Contains(string(body), ins.ResponseBody.ExpectSubstring)
			expectDesc = fmt.Sprintf("expect_substring: %s", ins.ResponseBody.ExpectSubstring)
			rbEvent.Labels[types.AttrPrefix+"expect_substring"] = ins.ResponseBody.ExpectSubstring
		}

		if !matched {
			rbEvent.SetEventStatus(ins.ResponseBody.Severity)
			rbEvent.SetDescription(fmt.Sprintf("response body does not match %s", expectDesc))
		} else {
			rbEvent.SetDescription(fmt.Sprintf("response body matches %s", expectDesc))
		}

		q.PushFront(rbEvent)
	}
}

func truncateBody(body []byte, max int) string {
	if len(body) <= max {
		return string(body)
	}
	return strings.ToValidUTF8(string(body[:max]), "") + "\n... (truncated)"
}
