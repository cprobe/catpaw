package config

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"flashcat.cloud/catpaw/pkg/tls"
)

type HTTPConfig struct {
	Interface       string   `toml:"interface"`
	Method          string   `toml:"method"`
	Timeout         Duration `toml:"timeout"`
	FollowRedirects bool     `toml:"follow_redirects"`
	BasicAuthUser   string   `toml:"basic_auth_user"`
	BasicAuthPass   string   `toml:"basic_auth_pass"`
	Headers         []string `toml:"headers"`
	Payload         string   `toml:"payload"`
	HTTPProxy       string   `toml:"http_proxy"`
	tls.ClientConfig
}

type proxyFunc func(req *http.Request) (*url.URL, error)

func (hc *HTTPConfig) Proxy() (proxyFunc, error) {
	if len(hc.HTTPProxy) > 0 {
		address, err := url.Parse(hc.HTTPProxy)
		if err != nil {
			return nil, fmt.Errorf("error parsing proxy url %q: %w", hc.HTTPProxy, err)
		}
		return http.ProxyURL(address), nil
	}
	return http.ProxyFromEnvironment, nil
}

func (hc *HTTPConfig) GetTimeout() Duration {
	if hc.Timeout == 0 {
		return Duration(time.Second * 5)
	}
	return hc.Timeout
}

func (hc *HTTPConfig) GetMethod() string {
	if hc.Method == "" {
		return "GET"
	}
	return hc.Method
}
