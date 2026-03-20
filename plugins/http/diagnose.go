package http

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	gohttp "net/http"
	"sort"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/plugins"
)

var _ plugins.Diagnosable = (*HttpPlugin)(nil)

func (p *HttpPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("http", "http",
		"HTTP diagnostic tools (probe URL, DNS lookup, TCP ping)", diagnose.ToolScopeLocal)

	registry.Register("http", diagnose.DiagnoseTool{
		Name:        "http_probe",
		Description: "Make an HTTP/HTTPS request and show status code, response headers, timing breakdown, and TLS info. Parameter: url (required)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "url", Type: "string", Description: "Full URL to probe (e.g. https://example.com/health)", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			target := args["url"]
			if target == "" {
				return "", fmt.Errorf("parameter 'url' is required")
			}

			transport := &gohttp.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
				DisableKeepAlives: true,
			}
			client := &gohttp.Client{
				Transport: transport,
				Timeout:   10 * time.Second,
				CheckRedirect: func(req *gohttp.Request, via []*gohttp.Request) error {
					if len(via) >= 5 {
						return fmt.Errorf("too many redirects")
					}
					return nil
				},
			}

			req, err := gohttp.NewRequestWithContext(ctx, "GET", target, nil)
			if err != nil {
				return "", fmt.Errorf("create request: %w", err)
			}
			req.Header.Set("User-Agent", "catpaw-diagnose/1.0")

			start := time.Now()
			resp, err := client.Do(req)
			elapsed := time.Since(start)

			if err != nil {
				return fmt.Sprintf("Request to %s failed after %s: %v", target, elapsed, err), nil
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

			var b strings.Builder
			fmt.Fprintf(&b, "URL:          %s\n", target)
			fmt.Fprintf(&b, "Status:       %s\n", resp.Status)
			fmt.Fprintf(&b, "Proto:        %s\n", resp.Proto)
			fmt.Fprintf(&b, "Duration:     %s\n", elapsed)
			fmt.Fprintf(&b, "Content-Len:  %d\n", resp.ContentLength)

			if resp.TLS != nil {
				fmt.Fprintf(&b, "\nTLS Info:\n")
				fmt.Fprintf(&b, "  Version:    %s\n", tlsVersionName(resp.TLS.Version))
				fmt.Fprintf(&b, "  CipherSuite: %s\n", tls.CipherSuiteName(resp.TLS.CipherSuite))
				if len(resp.TLS.PeerCertificates) > 0 {
					cert := resp.TLS.PeerCertificates[0]
					fmt.Fprintf(&b, "  Subject:    %s\n", cert.Subject.CommonName)
					fmt.Fprintf(&b, "  Issuer:     %s\n", cert.Issuer.CommonName)
					fmt.Fprintf(&b, "  Expires:    %s\n", cert.NotAfter.UTC().Format("2006-01-02 15:04:05 UTC"))
					fmt.Fprintf(&b, "  DNS Names:  %s\n", strings.Join(cert.DNSNames, ", "))
				}
			}

			fmt.Fprintf(&b, "\nResponse Headers:\n")
			keys := make([]string, 0, len(resp.Header))
			for k := range resp.Header {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "  %s: %s\n", k, strings.Join(resp.Header[k], "; "))
			}

			if len(body) > 0 {
				bodyStr := strings.ToValidUTF8(string(body), "")
				fmt.Fprintf(&b, "\nBody (first %d bytes):\n%s\n", len(body), bodyStr)
			}

			return b.String(), nil
		},
	})

	registry.Register("http", diagnose.DiagnoseTool{
		Name:        "dns_lookup",
		Description: "Resolve a hostname to IP addresses. Parameter: host (required)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "host", Type: "string", Description: "Hostname to resolve (e.g. example.com)", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			host := args["host"]
			if host == "" {
				return "", fmt.Errorf("parameter 'host' is required")
			}

			start := time.Now()
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			elapsed := time.Since(start)

			if err != nil {
				return fmt.Sprintf("DNS lookup for %s failed after %s: %v", host, elapsed, err), nil
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Host:     %s\n", host)
			fmt.Fprintf(&b, "Duration: %s\n", elapsed)
			fmt.Fprintf(&b, "Results:  %d addresses\n\n", len(ips))
			for _, ip := range ips {
				zone := ""
				if ip.Zone != "" {
					zone = "%" + ip.Zone
				}
				fmt.Fprintf(&b, "  %s%s\n", ip.IP.String(), zone)
			}

			cname, err := net.DefaultResolver.LookupCNAME(ctx, host)
			if err == nil && cname != "" && cname != host+"." {
				fmt.Fprintf(&b, "\nCNAME: %s\n", cname)
			}

			return b.String(), nil
		},
	})

	registry.Register("http", diagnose.DiagnoseTool{
		Name:        "tcp_ping",
		Description: "Test TCP connectivity to a host:port, measuring connection time. Parameter: address (required, e.g. 10.0.0.1:80)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "address", Type: "string", Description: "Host:port to connect to (e.g. 10.0.0.1:6379)", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			addr := args["address"]
			if addr == "" {
				return "", fmt.Errorf("parameter 'address' is required")
			}

			const attempts = 3
			var b strings.Builder
			fmt.Fprintf(&b, "TCP ping %s (%d attempts):\n\n", addr, attempts)

			var successes int
			var totalDur time.Duration
			for i := 0; i < attempts; i++ {
				start := time.Now()
				conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
				elapsed := time.Since(start)
				if err != nil {
					fmt.Fprintf(&b, "  #%d: failed after %s - %v\n", i+1, elapsed, err)
				} else {
					conn.Close()
					fmt.Fprintf(&b, "  #%d: connected in %s\n", i+1, elapsed)
					successes++
					totalDur += elapsed
				}
			}

			fmt.Fprintf(&b, "\nResult: %d/%d successful", successes, attempts)
			if successes > 0 {
				fmt.Fprintf(&b, ", avg %s", totalDur/time.Duration(successes))
			}
			fmt.Fprintln(&b)
			return b.String(), nil
		},
	})
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}
