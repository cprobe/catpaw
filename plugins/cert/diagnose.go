package cert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/cprobe/digcore/diagnose"
	"github.com/cprobe/digcore/plugins"
)

var _ plugins.Diagnosable = (*CertPlugin)(nil)

func (p *CertPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("cert", "cert",
		"Certificate diagnostic tools (inspect remote TLS certs, parse local cert files)", diagnose.ToolScopeLocal)

	registry.Register("cert", diagnose.DiagnoseTool{
		Name:        "cert_inspect_remote",
		Description: "Connect to a TLS endpoint and show full certificate chain details (subject, issuer, expiry, SANs, serial). Parameter: address (required, e.g. example.com:443)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "address", Type: "string", Description: "Host:port to connect (e.g. example.com:443)", Required: true},
			{Name: "sni", Type: "string", Description: "Optional SNI hostname (defaults to host from address)", Required: false},
		},
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			addr := args["address"]
			if addr == "" {
				return "", fmt.Errorf("parameter 'address' is required")
			}

			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
				addr = addr + ":443"
			}

			sni := args["sni"]
			if sni == "" {
				sni = host
			}

			dialer := &net.Dialer{Timeout: 10 * time.Second}
			conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
				InsecureSkipVerify: true,
				ServerName:         sni,
			})
			if err != nil {
				return fmt.Sprintf("TLS connection to %s failed: %v", addr, err), nil
			}
			defer conn.Close()

			state := conn.ConnectionState()
			certs := state.PeerCertificates
			if len(certs) == 0 {
				return fmt.Sprintf("Connected to %s but no peer certificates found", addr), nil
			}

			var b strings.Builder
			fmt.Fprintf(&b, "TLS connection to %s (SNI: %s)\n", addr, sni)
			fmt.Fprintf(&b, "TLS Version: %s\n", tlsVersionStr(state.Version))
			fmt.Fprintf(&b, "Cipher Suite: %s\n", tls.CipherSuiteName(state.CipherSuite))
			fmt.Fprintf(&b, "Certificate chain: %d certificates\n\n", len(certs))

			for i, cert := range certs {
				label := "Leaf"
				if i > 0 {
					label = "Intermediate"
				}
				if cert.IsCA {
					label = "CA"
				}

				timeUntilExpiry := time.Until(cert.NotAfter)

				fmt.Fprintf(&b, "--- Certificate #%d (%s) ---\n", i+1, label)
				fmt.Fprintf(&b, "  Subject:    %s\n", cert.Subject.String())
				fmt.Fprintf(&b, "  Issuer:     %s\n", cert.Issuer.String())
				fmt.Fprintf(&b, "  Serial:     %s\n", formatSerial(cert.SerialNumber))
				fmt.Fprintf(&b, "  Not Before: %s\n", cert.NotBefore.UTC().Format("2006-01-02 15:04:05 UTC"))
				fmt.Fprintf(&b, "  Not After:  %s\n", cert.NotAfter.UTC().Format("2006-01-02 15:04:05 UTC"))
				if timeUntilExpiry > 0 {
					fmt.Fprintf(&b, "  Expires In: %s\n", humanDuration(timeUntilExpiry))
				} else {
					fmt.Fprintf(&b, "  EXPIRED:    %s ago\n", humanDuration(-timeUntilExpiry))
				}
				if len(cert.DNSNames) > 0 {
					fmt.Fprintf(&b, "  DNS SANs:   %s\n", strings.Join(cert.DNSNames, ", "))
				}
				if len(cert.IPAddresses) > 0 {
					var ips []string
					for _, ip := range cert.IPAddresses {
						ips = append(ips, ip.String())
					}
					fmt.Fprintf(&b, "  IP SANs:    %s\n", strings.Join(ips, ", "))
				}
				fmt.Fprintf(&b, "  Key Algo:   %s\n", cert.PublicKeyAlgorithm.String())
				fmt.Fprintf(&b, "  Sig Algo:   %s\n", cert.SignatureAlgorithm.String())
				fmt.Fprintf(&b, "  SHA256:     %s\n", sha256Fingerprint(cert.Raw))
				fmt.Fprintln(&b)
			}

			if err := verifyChain(certs, sni); err != nil {
				fmt.Fprintf(&b, "Chain verification: FAILED - %v\n", err)
			} else {
				fmt.Fprintf(&b, "Chain verification: OK\n")
			}

			return b.String(), nil
		},
	})

	registry.Register("cert", diagnose.DiagnoseTool{
		Name:        "cert_inspect_file",
		Description: "Parse a local PEM or DER certificate file and show details. Parameter: path (required)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "path", Type: "string", Description: "Absolute path to the certificate file", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			path := args["path"]
			if path == "" {
				return "", fmt.Errorf("parameter 'path' is required")
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Sprintf("Cannot read file %s: %v", path, err), nil
			}
			if len(data) > maxCertFileSize {
				return fmt.Sprintf("File too large (%d bytes), likely not a certificate", len(data)), nil
			}

			certs, err := parseCerts(data)
			if err != nil {
				return fmt.Sprintf("No valid certificates in %s: %v", path, err), nil
			}

			var b strings.Builder
			fmt.Fprintf(&b, "File: %s\n", path)
			fmt.Fprintf(&b, "Certificates found: %d\n\n", len(certs))

			for i, cert := range certs {
				timeUntilExpiry := time.Until(cert.NotAfter)

				fmt.Fprintf(&b, "--- Certificate #%d ---\n", i+1)
				fmt.Fprintf(&b, "  Subject:    %s\n", cert.Subject.String())
				fmt.Fprintf(&b, "  Issuer:     %s\n", cert.Issuer.String())
				fmt.Fprintf(&b, "  Serial:     %s\n", formatSerial(cert.SerialNumber))
				fmt.Fprintf(&b, "  Not Before: %s\n", cert.NotBefore.UTC().Format("2006-01-02 15:04:05 UTC"))
				fmt.Fprintf(&b, "  Not After:  %s\n", cert.NotAfter.UTC().Format("2006-01-02 15:04:05 UTC"))
				if timeUntilExpiry > 0 {
					fmt.Fprintf(&b, "  Expires In: %s\n", humanDuration(timeUntilExpiry))
				} else {
					fmt.Fprintf(&b, "  EXPIRED:    %s ago\n", humanDuration(-timeUntilExpiry))
				}
				if len(cert.DNSNames) > 0 {
					fmt.Fprintf(&b, "  DNS SANs:   %s\n", strings.Join(cert.DNSNames, ", "))
				}
				fmt.Fprintf(&b, "  Is CA:      %v\n", cert.IsCA)
				fmt.Fprintf(&b, "  SHA256:     %s\n", sha256Fingerprint(cert.Raw))
				fmt.Fprintln(&b)
			}

			return b.String(), nil
		},
	})
}

func verifyChain(certs []*x509.Certificate, serverName string) error {
	if len(certs) == 0 {
		return fmt.Errorf("empty certificate chain")
	}

	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}

	_, err := certs[0].Verify(x509.VerifyOptions{
		DNSName:       serverName,
		Intermediates: intermediates,
	})
	return err
}

func tlsVersionStr(v uint16) string {
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
