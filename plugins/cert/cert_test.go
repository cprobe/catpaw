package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cprobe/catpaw/config"
	clogger "github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
	"go.uber.org/zap"
)

func initTestConfig(t *testing.T) {
	t.Helper()
	if config.Config == nil {
		tmpDir := t.TempDir()
		config.Config = &config.ConfigType{
			ConfigDir: tmpDir,
			StateDir:  tmpDir,
		}
	}
	if clogger.Logger == nil {
		l, _ := zap.NewDevelopment()
		clogger.Logger = l.Sugar()
	}
}

// --- Certificate generation helpers ---

func generateCert(t *testing.T, notBefore, notAfter time.Time, dnsNames []string) (certPEM []byte, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-cert"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     dnsNames,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM
}

func writeCertFile(t *testing.T, dir, name string, certPEM []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, certPEM, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func startTLSServer(t *testing.T, certPEM, keyPEM []byte) (addr string, closeFn func()) {
	t.Helper()
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Complete the TLS handshake before closing
			tlsConn, ok := conn.(*tls.Conn)
			if ok {
				_ = tlsConn.Handshake()
			}
			conn.Close()
		}
	}()

	return ln.Addr().String(), func() { ln.Close() }
}

// --- Init tests ---

func TestInitValidation(t *testing.T) {
	initTestConfig(t)

	tests := []struct {
		name    string
		ins     *Instance
		wantErr string
	}{
		{
			name:    "both targets empty",
			ins:     &Instance{},
			wantErr: "cannot both be empty",
		},
		{
			name: "invalid starttls protocol",
			ins: &Instance{
				RemoteTargets: []string{"example.com:443"},
				StartTLS:      "xmpp",
			},
			wantErr: "unsupported starttls protocol",
		},
		{
			name: "critical_within >= warn_within for remote",
			ins: &Instance{
				RemoteTargets: []string{"example.com:443"},
				RemoteExpiry: ExpiryCheck{
					WarnWithin:     config.Duration(168 * time.Hour),
					CriticalWithin: config.Duration(720 * time.Hour),
				},
			},
			wantErr: "remote_expiry: critical_within",
		},
		{
			name: "critical_within >= warn_within for file",
			ins: &Instance{
				FileTargets: []string{"/tmp/cert.pem"},
				FileExpiry: ExpiryCheck{
					WarnWithin:     config.Duration(168 * time.Hour),
					CriticalWithin: config.Duration(168 * time.Hour),
				},
			},
			wantErr: "file_expiry: critical_within",
		},
		{
			name: "empty host in remote target",
			ins: &Instance{
				RemoteTargets: []string{":443"},
			},
			wantErr: "empty host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.ins.Init()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestInitDefaults(t *testing.T) {
	initTestConfig(t)

	ins := &Instance{
		RemoteTargets: []string{"example.com"},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	if ins.RemoteTargets[0] != "example.com:443" {
		t.Errorf("expected port normalization to example.com:443, got %s", ins.RemoteTargets[0])
	}
	if time.Duration(ins.Timeout) != 10*time.Second {
		t.Errorf("expected timeout 10s, got %s", time.Duration(ins.Timeout))
	}
	if ins.Concurrency != 10 {
		t.Errorf("expected concurrency 10, got %d", ins.Concurrency)
	}
	if ins.MaxFileTargets != 100 {
		t.Errorf("expected max_file_targets 100, got %d", ins.MaxFileTargets)
	}
	if time.Duration(ins.RemoteExpiry.WarnWithin) != 720*time.Hour {
		t.Errorf("expected default warn_within 720h, got %s", time.Duration(ins.RemoteExpiry.WarnWithin))
	}
	if time.Duration(ins.RemoteExpiry.CriticalWithin) != 168*time.Hour {
		t.Errorf("expected default critical_within 168h, got %s", time.Duration(ins.RemoteExpiry.CriticalWithin))
	}
}

func TestInitSNIParsing(t *testing.T) {
	initTestConfig(t)

	ins := &Instance{
		RemoteTargets: []string{
			"10.0.0.1:443@api.example.com",
			"example.com:443",
			"10.0.0.2:8443@dashboard.example.com",
		},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	if ins.RemoteTargets[0] != "10.0.0.1:443" {
		t.Errorf("expected normalized target 10.0.0.1:443, got %s", ins.RemoteTargets[0])
	}
	if ins.targetSNI["10.0.0.1:443"] != "api.example.com" {
		t.Errorf("expected SNI api.example.com for 10.0.0.1:443, got %s", ins.targetSNI["10.0.0.1:443"])
	}
	if _, ok := ins.targetSNI["example.com:443"]; ok {
		t.Error("example.com:443 should not have per-target SNI")
	}
	if ins.targetSNI["10.0.0.2:8443"] != "dashboard.example.com" {
		t.Errorf("expected SNI dashboard.example.com, got %s", ins.targetSNI["10.0.0.2:8443"])
	}
}

func TestInitFileTargetClassification(t *testing.T) {
	initTestConfig(t)

	ins := &Instance{
		FileTargets: []string{
			"/etc/ssl/certs/app.pem",
			"/etc/nginx/ssl/*.crt",
			"/etc/letsencrypt/live/*/fullchain.pem",
		},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	if len(ins.explicitFilePaths) != 1 || ins.explicitFilePaths[0] != "/etc/ssl/certs/app.pem" {
		t.Errorf("expected 1 explicit path, got %v", ins.explicitFilePaths)
	}
	if len(ins.fileGlobPatterns) != 2 {
		t.Errorf("expected 2 glob patterns, got %v", ins.fileGlobPatterns)
	}
}

// --- DetermineSNI tests ---

func TestDetermineSNI(t *testing.T) {
	initTestConfig(t)

	ins := &Instance{
		RemoteTargets: []string{"10.0.0.1:443@api.example.com", "example.com:443"},
		ServerName:    "global.example.com",
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	// Per-target SNI takes priority
	if sni := ins.determineSNI("10.0.0.1:443"); sni != "api.example.com" {
		t.Errorf("expected per-target SNI, got %s", sni)
	}
	// Instance-level ServerName as fallback
	if sni := ins.determineSNI("example.com:443"); sni != "global.example.com" {
		t.Errorf("expected instance-level SNI, got %s", sni)
	}

	// Without ServerName, extract from hostname
	ins2 := &Instance{
		RemoteTargets: []string{"example.com:443"},
	}
	if err := ins2.Init(); err != nil {
		t.Fatal(err)
	}
	if sni := ins2.determineSNI("example.com:443"); sni != "example.com" {
		t.Errorf("expected hostname-extracted SNI, got %s", sni)
	}
}

// --- parseCerts tests ---

func TestParseCertsPEM(t *testing.T) {
	certPEM, _ := generateCert(t,
		time.Now().Add(-time.Hour),
		time.Now().Add(24*time.Hour),
		[]string{"test.example.com"})

	certs, err := parseCerts(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(certs))
	}
	if certs[0].Subject.CommonName != "test-cert" {
		t.Errorf("expected CN=test-cert, got %s", certs[0].Subject.CommonName)
	}
}

func TestParseCertsDER(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "der-cert"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certs, err := parseCerts(derBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(certs))
	}
	if certs[0].Subject.CommonName != "der-cert" {
		t.Errorf("expected CN=der-cert, got %s", certs[0].Subject.CommonName)
	}
}

func TestParseCertsChain(t *testing.T) {
	cert1PEM, _ := generateCert(t,
		time.Now().Add(-time.Hour),
		time.Now().Add(48*time.Hour),
		nil)
	cert2PEM, _ := generateCert(t,
		time.Now().Add(-time.Hour),
		time.Now().Add(24*time.Hour),
		nil)

	chain := append(cert1PEM, cert2PEM...)
	certs, err := parseCerts(chain)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 2 {
		t.Fatalf("expected 2 certs, got %d", len(certs))
	}
}

func TestParseCertsInvalid(t *testing.T) {
	_, err := parseCerts([]byte("this is not a certificate"))
	if err == nil {
		t.Fatal("expected error for invalid data")
	}
}

// --- earliestExpiry tests ---

func TestEarliestExpiry(t *testing.T) {
	cert1PEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(48*time.Hour), nil)
	cert2PEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), nil)

	certs1, _ := parseCerts(cert1PEM)
	certs2, _ := parseCerts(cert2PEM)
	chain := append(certs1, certs2...)

	cert, idx := earliestExpiry(chain)
	if idx != 1 {
		t.Errorf("expected earliest at index 1, got %d", idx)
	}
	if cert != chain[1] {
		t.Error("returned cert doesn't match expected")
	}
}

// --- evaluateExpiry tests ---

func TestEvaluateExpiryOk(t *testing.T) {
	initTestConfig(t)
	certPEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour), nil)
	certs, _ := parseCerts(certPEM)

	ins := &Instance{}
	event := types.BuildEvent(map[string]string{"check": "cert::file_expiry", "target": "test"})
	ins.evaluateExpiry(event, certs[0], 0, 1, ExpiryCheck{
		WarnWithin:     config.Duration(720 * time.Hour),
		CriticalWithin: config.Duration(168 * time.Hour),
	})

	if event.EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok, got %s: %s", event.EventStatus, event.Description)
	}
	if !strings.Contains(event.Description, "everything is ok") {
		t.Errorf("expected 'everything is ok' in description, got: %s", event.Description)
	}
}

func TestEvaluateExpiryWarning(t *testing.T) {
	initTestConfig(t)
	certPEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(20*24*time.Hour), nil)
	certs, _ := parseCerts(certPEM)

	ins := &Instance{}
	event := types.BuildEvent(map[string]string{"check": "cert::file_expiry", "target": "test"})
	ins.evaluateExpiry(event, certs[0], 0, 1, ExpiryCheck{
		WarnWithin:     config.Duration(720 * time.Hour),
		CriticalWithin: config.Duration(168 * time.Hour),
	})

	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning, got %s: %s", event.EventStatus, event.Description)
	}
	if !strings.Contains(event.Description, "warning threshold") {
		t.Errorf("expected 'warning threshold' in description, got: %s", event.Description)
	}
}

func TestEvaluateExpiryCritical(t *testing.T) {
	initTestConfig(t)
	certPEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour), nil)
	certs, _ := parseCerts(certPEM)

	ins := &Instance{}
	event := types.BuildEvent(map[string]string{"check": "cert::file_expiry", "target": "test"})
	ins.evaluateExpiry(event, certs[0], 0, 1, ExpiryCheck{
		WarnWithin:     config.Duration(720 * time.Hour),
		CriticalWithin: config.Duration(168 * time.Hour),
	})

	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical, got %s: %s", event.EventStatus, event.Description)
	}
	if !strings.Contains(event.Description, "critical threshold") {
		t.Errorf("expected 'critical threshold' in description, got: %s", event.Description)
	}
}

func TestEvaluateExpiryExpired(t *testing.T) {
	initTestConfig(t)
	certPEM, _ := generateCert(t, time.Now().Add(-48*time.Hour), time.Now().Add(-1*time.Hour), nil)
	certs, _ := parseCerts(certPEM)

	ins := &Instance{}
	event := types.BuildEvent(map[string]string{"check": "cert::file_expiry", "target": "test"})
	ins.evaluateExpiry(event, certs[0], 0, 1, ExpiryCheck{
		WarnWithin:     config.Duration(720 * time.Hour),
		CriticalWithin: config.Duration(168 * time.Hour),
	})

	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical for expired cert, got %s", event.EventStatus)
	}
	if !strings.Contains(event.Description, "expired") {
		t.Errorf("expected 'expired' in description, got: %s", event.Description)
	}
	// Check _attr_time_until_expiry has negative prefix
	tueVal := event.Labels[types.AttrPrefix+"time_until_expiry"]
	if !strings.HasPrefix(tueVal, "-") {
		t.Errorf("expected negative time_until_expiry for expired cert, got: %s", tueVal)
	}
}

func TestEvaluateExpiryNotYetValid(t *testing.T) {
	initTestConfig(t)
	certPEM, _ := generateCert(t, time.Now().Add(24*time.Hour), time.Now().Add(90*24*time.Hour), nil)
	certs, _ := parseCerts(certPEM)

	ins := &Instance{}
	event := types.BuildEvent(map[string]string{"check": "cert::file_expiry", "target": "test"})
	ins.evaluateExpiry(event, certs[0], 0, 1, ExpiryCheck{
		WarnWithin:     config.Duration(720 * time.Hour),
		CriticalWithin: config.Duration(168 * time.Hour),
	})

	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical for not-yet-valid cert, got %s", event.EventStatus)
	}
	if !strings.Contains(event.Description, "not yet valid") {
		t.Errorf("expected 'not yet valid' in description, got: %s", event.Description)
	}
}

func TestEvaluateExpiryIntermediateCert(t *testing.T) {
	initTestConfig(t)
	certPEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour), nil)
	certs, _ := parseCerts(certPEM)

	ins := &Instance{}
	event := types.BuildEvent(map[string]string{"check": "cert::file_expiry", "target": "test"})
	ins.evaluateExpiry(event, certs[0], 1, 3, ExpiryCheck{
		WarnWithin:     config.Duration(720 * time.Hour),
		CriticalWithin: config.Duration(168 * time.Hour),
	})

	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical, got %s", event.EventStatus)
	}
	if !strings.Contains(event.Description, "intermediate cert") {
		t.Errorf("expected 'intermediate cert' prefix, got: %s", event.Description)
	}
}

// --- File check integration tests ---

func TestCheckFileOk(t *testing.T) {
	initTestConfig(t)
	dir := t.TempDir()
	certPEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour),
		[]string{"test.example.com"})
	path := writeCertFile(t, dir, "ok.pem", certPEM)

	ins := &Instance{
		FileTargets: []string{path},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkFile(q, path)

	events := drainQueue(q)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok, got %s: %s", events[0].EventStatus, events[0].Description)
	}

	// Verify _attr_ labels
	if events[0].Labels[types.AttrPrefix+"cert_subject"] == "" {
		t.Error("expected cert_subject label")
	}
	if events[0].Labels[types.AttrPrefix+"cert_sha256"] == "" {
		t.Error("expected cert_sha256 label")
	}
	if events[0].Labels[types.AttrPrefix+"cert_dns_names"] != "test.example.com" {
		t.Errorf("expected dns_names=test.example.com, got %s",
			events[0].Labels[types.AttrPrefix+"cert_dns_names"])
	}
}

func TestCheckFileNotFound(t *testing.T) {
	initTestConfig(t)

	ins := &Instance{
		FileTargets: []string{"/nonexistent/path/cert.pem"},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkFile(q, "/nonexistent/path/cert.pem")

	events := drainQueue(q)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical, got %s", events[0].EventStatus)
	}
	if !strings.Contains(events[0].Description, "certificate file not found") {
		t.Errorf("expected 'certificate file not found', got: %s", events[0].Description)
	}
}

func TestCheckFileInvalidContent(t *testing.T) {
	initTestConfig(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0644); err != nil {
		t.Fatal(err)
	}

	ins := &Instance{
		FileTargets: []string{path},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkFile(q, path)

	events := drainQueue(q)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical, got %s", events[0].EventStatus)
	}
	if !strings.Contains(events[0].Description, "no valid certificates found") {
		t.Errorf("expected parse error message, got: %s", events[0].Description)
	}
}

func TestCheckFileTooLarge(t *testing.T) {
	initTestConfig(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.pem")
	if err := os.WriteFile(path, make([]byte, maxCertFileSize+1), 0644); err != nil {
		t.Fatal(err)
	}

	ins := &Instance{
		FileTargets: []string{path},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkFile(q, path)

	events := drainQueue(q)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical, got %s", events[0].EventStatus)
	}
	if !strings.Contains(events[0].Description, "file too large") {
		t.Errorf("expected 'file too large', got: %s", events[0].Description)
	}
}

func TestCheckFileChainEarliestExpiry(t *testing.T) {
	initTestConfig(t)
	dir := t.TempDir()

	// leaf: 90 days, intermediate: 3 days (should trigger critical)
	leafPEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour), nil)
	intermediatePEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour), nil)
	chain := append(leafPEM, intermediatePEM...)
	path := writeCertFile(t, dir, "chain.pem", chain)

	ins := &Instance{
		FileTargets: []string{path},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkFile(q, path)

	events := drainQueue(q)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical for expiring intermediate, got %s: %s",
			events[0].EventStatus, events[0].Description)
	}
	if !strings.Contains(events[0].Description, "intermediate cert") {
		t.Errorf("expected 'intermediate cert' in description, got: %s", events[0].Description)
	}
}

// --- Remote check integration test ---

func TestCheckRemoteOk(t *testing.T) {
	initTestConfig(t)

	certPEM, keyPEM := generateCert(t,
		time.Now().Add(-time.Hour),
		time.Now().Add(90*24*time.Hour),
		[]string{"localhost"})

	addr, closeFn := startTLSServer(t, certPEM, keyPEM)
	defer closeFn()

	ins := &Instance{
		RemoteTargets: []string{addr},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkRemote(q, ins.RemoteTargets[0])

	events := drainQueue(q)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok, got %s: %s", events[0].EventStatus, events[0].Description)
	}
	if events[0].Labels[types.AttrPrefix+"cert_sni"] == "" {
		t.Error("expected cert_sni label to be set")
	}
}

func TestCheckRemoteExpired(t *testing.T) {
	initTestConfig(t)

	certPEM, keyPEM := generateCert(t,
		time.Now().Add(-48*time.Hour),
		time.Now().Add(-1*time.Hour),
		[]string{"localhost"})

	addr, closeFn := startTLSServer(t, certPEM, keyPEM)
	defer closeFn()

	ins := &Instance{
		RemoteTargets: []string{addr},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkRemote(q, ins.RemoteTargets[0])

	events := drainQueue(q)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical for expired cert, got %s: %s",
			events[0].EventStatus, events[0].Description)
	}
}

func TestCheckRemoteConnectionFailed(t *testing.T) {
	initTestConfig(t)

	// Use a port that is not listening
	ins := &Instance{
		RemoteTargets: []string{"127.0.0.1:1"},
		Timeout:       config.Duration(1 * time.Second),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkRemote(q, ins.RemoteTargets[0])

	events := drainQueue(q)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical, got %s", events[0].EventStatus)
	}
	if !strings.Contains(events[0].Description, "failed") {
		t.Errorf("expected 'failed' in description, got: %s", events[0].Description)
	}
}

// --- Gather integration test ---

func TestGatherCombined(t *testing.T) {
	initTestConfig(t)
	dir := t.TempDir()

	certPEM, keyPEM := generateCert(t,
		time.Now().Add(-time.Hour),
		time.Now().Add(90*24*time.Hour),
		[]string{"localhost"})

	addr, closeFn := startTLSServer(t, certPEM, keyPEM)
	defer closeFn()

	filePath := writeCertFile(t, dir, "test.pem", certPEM)

	ins := &Instance{
		RemoteTargets: []string{addr},
		FileTargets:   []string{filePath},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	events := drainQueue(q)
	if len(events) != 2 {
		t.Fatalf("expected 2 events (1 remote + 1 file), got %d", len(events))
	}

	var remoteFound, fileFound bool
	for _, e := range events {
		if e.Labels["check"] == "cert::remote_expiry" {
			remoteFound = true
		}
		if e.Labels["check"] == "cert::file_expiry" {
			fileFound = true
		}
	}
	if !remoteFound {
		t.Error("expected remote_expiry event")
	}
	if !fileFound {
		t.Error("expected file_expiry event")
	}
}

// --- Glob support test ---

func TestGatherFileGlob(t *testing.T) {
	initTestConfig(t)
	dir := t.TempDir()

	certPEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour), nil)
	writeCertFile(t, dir, "a.pem", certPEM)
	writeCertFile(t, dir, "b.pem", certPEM)
	writeCertFile(t, dir, "c.txt", []byte("not a cert"))

	ins := &Instance{
		FileTargets: []string{filepath.Join(dir, "*.pem")},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	events := drainQueue(q)
	if len(events) != 2 {
		t.Fatalf("expected 2 file events from glob, got %d", len(events))
	}
	for _, e := range events {
		if e.EventStatus != types.EventStatusOk {
			t.Errorf("expected Ok, got %s: %s", e.EventStatus, e.Description)
		}
	}
}

func TestGatherMaxFileTargets(t *testing.T) {
	initTestConfig(t)
	dir := t.TempDir()

	certPEM, _ := generateCert(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour), nil)
	for i := 0; i < 5; i++ {
		writeCertFile(t, dir, fmt.Sprintf("cert%d.pem", i), certPEM)
	}

	ins := &Instance{
		FileTargets:    []string{filepath.Join(dir, "*.pem")},
		MaxFileTargets: 3,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	events := drainQueue(q)
	// 3 file check events + 1 warning about exceeding max
	warnCount := 0
	fileCount := 0
	for _, e := range events {
		if e.Labels["target"] == "glob" {
			warnCount++
			if !strings.Contains(e.Description, "exceeding max_file_targets") {
				t.Errorf("expected overflow warning, got: %s", e.Description)
			}
		} else {
			fileCount++
		}
	}
	if warnCount != 1 {
		t.Errorf("expected 1 overflow warning, got %d", warnCount)
	}
	if fileCount != 3 {
		t.Errorf("expected 3 file check events, got %d", fileCount)
	}
}

// --- Formatting helper tests ---

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{90*24*time.Hour + 5*time.Hour, "90 days 5 hours"},
		{3*time.Hour + 30*time.Minute, "3 hours 30 minutes"},
		{45 * time.Minute, "45 minutes"},
		{0, "0 minutes"},
	}
	for _, tt := range tests {
		got := humanDuration(tt.d)
		if got != tt.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestFormatSerial(t *testing.T) {
	n := new(big.Int)
	n.SetBytes([]byte{0x03, 0xA1, 0x45, 0x9B})
	got := formatSerial(n)
	if got != "03:A1:45:9B" {
		t.Errorf("formatSerial = %q, want 03:A1:45:9B", got)
	}

	if formatSerial(nil) != "" {
		t.Error("formatSerial(nil) should return empty string")
	}
}

func TestSha256Fingerprint(t *testing.T) {
	fp := sha256Fingerprint([]byte("test"))
	if len(fp) == 0 {
		t.Error("expected non-empty fingerprint")
	}
	// SHA-256 produces 32 bytes → 32 hex pairs with colons = 32*3-1 = 95 chars
	if len(fp) != 95 {
		t.Errorf("expected fingerprint length 95, got %d", len(fp))
	}
}

// --- SMTP STARTTLS tests ---

func TestSmtpStartTLS(t *testing.T) {
	// Simulate an SMTP server that supports STARTTLS
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		fmt.Fprintf(conn, "220 mail.example.com ESMTP\r\n")

		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		if strings.HasPrefix(string(buf[:n]), "EHLO") {
			fmt.Fprintf(conn, "250-mail.example.com\r\n")
			fmt.Fprintf(conn, "250 STARTTLS\r\n")
		}

		n, _ = conn.Read(buf)
		if strings.HasPrefix(string(buf[:n]), "STARTTLS") {
			fmt.Fprintf(conn, "220 Ready to start TLS\r\n")
		}
	}()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := smtpStartTLS(conn, 2*time.Second); err != nil {
		t.Errorf("smtpStartTLS failed: %v", err)
	}
}

func TestSmtpStartTLSRejected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		fmt.Fprintf(conn, "220 mail.example.com ESMTP\r\n")

		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		if strings.HasPrefix(string(buf[:n]), "EHLO") {
			fmt.Fprintf(conn, "250 OK\r\n")
		}

		n, _ = conn.Read(buf)
		if strings.HasPrefix(string(buf[:n]), "STARTTLS") {
			fmt.Fprintf(conn, "454 TLS not available\r\n")
		}
	}()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := smtpStartTLS(conn, 2*time.Second); err == nil {
		t.Error("expected error for STARTTLS rejection")
	} else if !strings.Contains(err.Error(), "454") {
		t.Errorf("expected '454' in error, got: %v", err)
	}
}

// --- validateAndFillThresholds tests ---

func TestValidateAndFillThresholds(t *testing.T) {
	// Both zero with targets → fill defaults
	check := ExpiryCheck{}
	if err := validateAndFillThresholds(&check, true); err != nil {
		t.Fatal(err)
	}
	if time.Duration(check.WarnWithin) != 720*time.Hour {
		t.Errorf("expected default warn 720h, got %s", time.Duration(check.WarnWithin))
	}

	// Both zero without targets → don't fill
	check2 := ExpiryCheck{}
	if err := validateAndFillThresholds(&check2, false); err != nil {
		t.Fatal(err)
	}
	if time.Duration(check2.WarnWithin) != 0 {
		t.Errorf("expected no default fill without targets, got %s", time.Duration(check2.WarnWithin))
	}

	// Invalid: critical >= warn
	check3 := ExpiryCheck{
		WarnWithin:     config.Duration(100 * time.Hour),
		CriticalWithin: config.Duration(200 * time.Hour),
	}
	if err := validateAndFillThresholds(&check3, true); err == nil {
		t.Error("expected error for critical >= warn")
	}
}

// --- resolveFileTargets tests ---

func TestResolveFileTargets(t *testing.T) {
	initTestConfig(t)
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "a.pem"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.pem"), []byte("b"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	ins := &Instance{
		explicitFilePaths: []string{filepath.Join(dir, "a.pem"), "/nonexistent/cert.pem"},
		fileGlobPatterns:  []string{filepath.Join(dir, "*.pem")},
	}

	files := ins.resolveFileTargets()

	// Should have: a.pem (explicit), /nonexistent/cert.pem (explicit, even if missing),
	// b.pem (from glob), a.pem deduplicated
	expected := map[string]bool{
		filepath.Join(dir, "a.pem"): true,
		"/nonexistent/cert.pem":     true,
		filepath.Join(dir, "b.pem"): true,
	}

	if len(files) != len(expected) {
		t.Errorf("expected %d files, got %d: %v", len(expected), len(files), files)
	}
	for _, f := range files {
		if !expected[f] {
			t.Errorf("unexpected file in resolved list: %s", f)
		}
	}
}

// --- Helper ---

func drainQueue(q *safe.Queue[*types.Event]) []*types.Event {
	var events []*types.Event
	for {
		e := q.PopBack()
		if e == nil {
			break
		}
		events = append(events, *e)
	}
	return events
}
