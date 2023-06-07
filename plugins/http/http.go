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

type Expect struct {
	ResponseSubstring        string          `toml:"response_substring"`
	ResponseStatusCode       []string        `toml:"response_status_code"`
	ResponseStatusCodeFilter filter.Filter   `toml:"-"` // compiled filter
	CertExpireThreshold      config.Duration `toml:"cert_expire_threshold"`
}

type Instance struct {
	config.InternalConfig

	Targets     []string `toml:"targets"`
	Concurrency int      `toml:"concurrency"`
	Expect      Expect   `toml:"expect"`

	Interface       string          `toml:"interface"`
	Method          string          `toml:"method"`
	Timeout         config.Duration `toml:"timeout"`
	FollowRedirects bool            `toml:"follow_redirects"`
	BasicAuthUser   string          `toml:"basic_auth_user"`
	BasicAuthPass   string          `toml:"basic_auth_pass"`
	Headers         []string        `toml:"headers"`
	Payload         string          `toml:"payload"`

	config.HTTPConfig
	client httpClient
}

type Http struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &Http{}
	})
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func (ins *Instance) Init() error {
	if ins.Concurrency == 0 {
		ins.Concurrency = 10
	}

	var err error
	if len(ins.Expect.ResponseStatusCode) > 0 {
		ins.Expect.ResponseStatusCodeFilter, err = filter.Compile(ins.Expect.ResponseStatusCode)
		if err != nil {
			return err
		}
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

	if !ins.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	return client, nil
}

func (h *Http) IsSystemPlugin() bool {
	return false
}

func (h *Http) GetInstances() []plugins.Instance {
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

	if !ins.GetInitialized() {
		if err := ins.Init(); err != nil {
			logger.Logger.Errorf("failed to init http instance: %v", err)
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
	logger.Logger.Debug("http target: ", target)

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

	// check connection
	resp, err := ins.client.Do(request)

	e := types.BuildEvent(map[string]string{
		"check": "HTTP check failed",
	}, labels)

	errString := "null. everything is ok"
	if err != nil {
		e.SetEventStatus(ins.Alerting.Severity)
		errString = err.Error()
	}

	e.SetTitleRule("$check").SetDescription(`
- **target**: ` + target + `
- **method**: ` + ins.GetMethod() + `
- **error**: ` + errString + `
	`)

	q.PushFront(e)

	if err != nil {
		logger.Logger.Errorw("failed to send http request", "error", err, "plugin", pluginName, "target", target)
		return
	}

	// check tls cert
	if ins.Expect.CertExpireThreshold > 0 && strings.HasPrefix(target, "https://") && resp.TLS != nil {
		e := types.BuildEvent(map[string]string{
			"check": "TLS cert will expire soon",
		}, labels)

		certExpireTimestamp := getEarliestCertExpiry(resp.TLS).Unix()
		if certExpireTimestamp < time.Now().Add(time.Duration(ins.Expect.CertExpireThreshold)).Unix() {
			e.SetEventStatus(ins.Alerting.Severity)
		}

		e.SetTitleRule("$check").SetDescription(`
- **target**: ` + target + `
- **method**: ` + ins.GetMethod() + `
- **cert expire threshold**: ` + ins.Expect.CertExpireThreshold.HumanString() + `
- **cert expire at**: ` + time.Unix(certExpireTimestamp, 0).Format("2006-01-02 15:04:05") + `
			`)

		q.PushFront(e)
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

	if len(ins.Expect.ResponseStatusCode) > 0 {
		e := types.BuildEvent(map[string]string{
			"check": "HTTP response status code not match",
		}, labels)

		if !ins.Expect.ResponseStatusCodeFilter.Match(statusCode) {
			e.SetEventStatus(ins.Alerting.Severity)
		}

		e.SetTitleRule("$check").SetDescription(fmt.Sprintf(ExpectResponseStatusCodeDesn, target, ins.GetMethod(), statusCode, ins.Expect.ResponseStatusCode, string(body)))
		q.PushFront(e)
	}

	if len(ins.Expect.ResponseSubstring) > 0 {
		e := types.BuildEvent(map[string]string{
			"check": "HTTP response body not match",
		}, labels)

		if !strings.Contains(string(body), ins.Expect.ResponseSubstring) {
			e.SetEventStatus(ins.Alerting.Severity)
		}

		e.SetTitleRule("$check").SetDescription(fmt.Sprintf(ExpectResponseSubstringDesn, target, ins.GetMethod(), statusCode, ins.Expect.ResponseSubstring, string(body)))
		q.PushFront(e)
	}
}

var ExpectResponseStatusCodeDesn = `
- **target**: %s
- **method**: %s
- **status code**: %s
- **expect code**: %v

**response body**:

` + "```" + `
%s
` + "```" + `
`

var ExpectResponseSubstringDesn = `
- **target**: %s
- **method**: %s
- **status code**: %s
- **expect substring**: %v

**response body**:

` + "```" + `
%s
` + "```" + `
`
