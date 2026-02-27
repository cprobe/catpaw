package http

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/filter"
	"flashcat.cloud/catpaw/pkg/netx"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "http"

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
	Severity        string `toml:"severity"`
	TitleRule       string `toml:"title_rule"`
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

	if ins.ResponseBody.ExpectSubstring != "" && ins.ResponseBody.Severity == "" {
		ins.ResponseBody.Severity = types.EventStatusWarning
	}

	client, err := ins.createHTTPClient()
	if err != nil {
		return fmt.Errorf("failed to create http client: %v", err)
	}
	ins.client = client

	for _, target := range ins.Targets {
		addr, err := url.Parse(target)
		if err != nil {
			return fmt.Errorf("failed to parse http url: %s, error: %v", target, err)
		}
		if addr.Scheme != "http" && addr.Scheme != "https" {
			return fmt.Errorf("only http and https are supported, target: %s", target)
		}
	}

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

	if ins.UseTLS {
		trans.TLSClientConfig = tlsCfg
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
	logger.Logger.Debugw("http target", "target", target)

	labels := map[string]string{
		"target": target,
		"method": ins.GetMethod(),
	}

	var payload io.Reader
	if ins.Payload != "" {
		payload = strings.NewReader(ins.Payload)
	}

	request, err := http.NewRequest(ins.Method, target, payload)
	if err != nil {
		logger.Logger.Errorw("failed to create http request", "error", err, "plugin", pluginName)
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
	}, labels).SetTitleRule(connTR)

	errString := "null. everything is ok"
	if err != nil {
		connEvent.SetEventStatus(ins.Connectivity.Severity)
		errString = err.Error()
	}

	connEvent.SetDescription(`[MD]
- **target**: ` + target + `
- **method**: ` + ins.GetMethod() + `
- **response_time**: ` + responseTime.String() + `
- **error**: ` + errString + `
	`)

	q.PushFront(connEvent)

	if err != nil {
		logger.Logger.Errorw("failed to send http request", "error", err, "plugin", pluginName, "target", target)
		return
	}

	// response time check
	if ins.ResponseTime.WarnGe > 0 || ins.ResponseTime.CriticalGe > 0 {
		rtTR := ins.ResponseTime.TitleRule
		if rtTR == "" {
			rtTR = "[check] [target]"
		}

		rtEvent := types.BuildEvent(map[string]string{
			"check": "http::response_time",
		}, labels).SetTitleRule(rtTR)

		if ins.ResponseTime.CriticalGe > 0 && responseTime >= time.Duration(ins.ResponseTime.CriticalGe) {
			rtEvent.SetEventStatus(types.EventStatusCritical)
		} else if ins.ResponseTime.WarnGe > 0 && responseTime >= time.Duration(ins.ResponseTime.WarnGe) {
			rtEvent.SetEventStatus(types.EventStatusWarning)
		}

		rtEvent.SetDescription(fmt.Sprintf(`[MD]
- **target**: %s
- **method**: %s
- **response_time**: %s
- **warn_threshold**: %s
- **critical_threshold**: %s
`, target, ins.GetMethod(), responseTime.String(),
			ins.ResponseTime.WarnGe.HumanString(),
			ins.ResponseTime.CriticalGe.HumanString()))

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
		timeUntilExpiry := time.Until(certExpiry)

		if ins.CertExpiry.CriticalWithin > 0 && timeUntilExpiry <= time.Duration(ins.CertExpiry.CriticalWithin) {
			certEvent.SetEventStatus(types.EventStatusCritical)
		} else if ins.CertExpiry.WarnWithin > 0 && timeUntilExpiry <= time.Duration(ins.CertExpiry.WarnWithin) {
			certEvent.SetEventStatus(types.EventStatusWarning)
		}

		certEvent.SetDescription(fmt.Sprintf(`[MD]
- **target**: %s
- **method**: %s
- **cert_expires_at**: %s
- **time_until_expiry**: %s
- **warn_within**: %s
- **critical_within**: %s
`, target, ins.GetMethod(),
			certExpiry.Format("2006-01-02 15:04:05"),
			timeUntilExpiry.Truncate(time.Minute).String(),
			ins.CertExpiry.WarnWithin.HumanString(),
			ins.CertExpiry.CriticalWithin.HumanString()))

		q.PushFront(certEvent)
	}

	var body []byte
	if resp.Body != nil {
		defer resp.Body.Close()
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			logger.Logger.Errorw("failed to read http response body", "error", err, "plugin", pluginName, "target", target)
			return
		}
	}

	statusCode := fmt.Sprint(resp.StatusCode)

	// status code check
	if len(ins.StatusCode.Expect) > 0 {
		scTR := ins.StatusCode.TitleRule
		if scTR == "" {
			scTR = "[check] [target]"
		}

		scEvent := types.BuildEvent(map[string]string{
			"check": "http::status_code",
		}, labels).SetTitleRule(scTR)

		if !ins.StatusCode.filter.Match(statusCode) {
			scEvent.SetEventStatus(ins.StatusCode.Severity)
		}

		scEvent.SetDescription(fmt.Sprintf(ExpectResponseStatusCodeDesc, target, ins.GetMethod(), statusCode, ins.StatusCode.Expect, string(body)))
		q.PushFront(scEvent)
	}

	// response body check
	if ins.ResponseBody.ExpectSubstring != "" {
		rbTR := ins.ResponseBody.TitleRule
		if rbTR == "" {
			rbTR = "[check] [target]"
		}

		rbEvent := types.BuildEvent(map[string]string{
			"check": "http::response_body",
		}, labels).SetTitleRule(rbTR)

		if !strings.Contains(string(body), ins.ResponseBody.ExpectSubstring) {
			rbEvent.SetEventStatus(ins.ResponseBody.Severity)
		}

		rbEvent.SetDescription(fmt.Sprintf(ExpectResponseSubstringDesc, target, ins.GetMethod(), statusCode, ins.ResponseBody.ExpectSubstring, string(body)))
		q.PushFront(rbEvent)
	}
}

var ExpectResponseStatusCodeDesc = `[MD]
- **target**: %s
- **method**: %s
- **status code**: %s
- **expect code**: %v

**response body**:

` + "```" + `
%s
` + "```" + `
`

var ExpectResponseSubstringDesc = `[MD]
- **target**: %s
- **method**: %s
- **status code**: %s
- **expect substring**: %v

**response body**:

` + "```" + `
%s
` + "```" + `
`
