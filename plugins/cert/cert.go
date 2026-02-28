package cert

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/filter"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const (
	pluginName      = "cert"
	maxCertFileSize = 1 << 20 // 1MB
)

type starttlsHandler func(conn net.Conn, timeout time.Duration) error

var starttlsHandlers = map[string]starttlsHandler{
	"smtp": smtpStartTLS,
}

type ExpiryCheck struct {
	WarnWithin     config.Duration `toml:"warn_within"`
	CriticalWithin config.Duration `toml:"critical_within"`
	TitleRule      string          `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	RemoteTargets  []string        `toml:"remote_targets"`
	FileTargets    []string        `toml:"file_targets"`
	Timeout        config.Duration `toml:"timeout"`
	Concurrency    int             `toml:"concurrency"`
	MaxFileTargets int             `toml:"max_file_targets"`
	StartTLS       string          `toml:"starttls"`
	ServerName     string          `toml:"server_name"`

	RemoteExpiry ExpiryCheck `toml:"remote_expiry"`
	FileExpiry   ExpiryCheck `toml:"file_expiry"`

	tlsConfig         *tls.Config
	targetSNI         map[string]string
	explicitFilePaths []string
	fileGlobPatterns  []string
}

type CertPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *CertPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &CertPlugin{}
	})
}

func (ins *Instance) Init() error {
	if len(ins.RemoteTargets) == 0 && len(ins.FileTargets) == 0 {
		return nil
	}

	// Parse remote targets: extract per-target SNI and normalize host:port
	ins.targetSNI = make(map[string]string)
	for i, raw := range ins.RemoteTargets {
		target, sni := raw, ""
		if idx := strings.LastIndex(raw, "@"); idx > 0 {
			target = raw[:idx]
			sni = raw[idx+1:]
		}

		host, port, err := net.SplitHostPort(target)
		if err != nil {
			host = strings.TrimRight(strings.TrimLeft(target, "["), "]")
			port = "443"
		}
		if host == "" {
			return fmt.Errorf("remote_targets[%d] %q: empty host", i, raw)
		}

		normalized := net.JoinHostPort(host, port)
		ins.RemoteTargets[i] = normalized

		if sni != "" {
			ins.targetSNI[normalized] = sni
		}
	}

	// Validate starttls
	if ins.StartTLS != "" {
		if _, ok := starttlsHandlers[ins.StartTLS]; !ok {
			supported := make([]string, 0, len(starttlsHandlers))
			for k := range starttlsHandlers {
				supported = append(supported, k)
			}
			return fmt.Errorf("unsupported starttls protocol: %q (supported: %v)", ins.StartTLS, supported)
		}
	}

	// Validate and fill remote_expiry thresholds
	if err := validateAndFillThresholds(&ins.RemoteExpiry, len(ins.RemoteTargets) > 0); err != nil {
		return fmt.Errorf("remote_expiry: %v", err)
	}

	// Validate and fill file_expiry thresholds
	if err := validateAndFillThresholds(&ins.FileExpiry, len(ins.FileTargets) > 0); err != nil {
		return fmt.Errorf("file_expiry: %v", err)
	}

	if ins.Timeout <= 0 {
		ins.Timeout = config.Duration(10 * time.Second)
	}
	if ins.Concurrency <= 0 {
		ins.Concurrency = 10
	}
	if ins.MaxFileTargets <= 0 {
		ins.MaxFileTargets = 100
	}

	// Build TLS config
	ins.tlsConfig = &tls.Config{
		InsecureSkipVerify: true,
	}
	if ins.ServerName != "" {
		ins.tlsConfig.ServerName = ins.ServerName
	}

	// Separate file_targets into explicit paths and glob patterns
	for _, ft := range ins.FileTargets {
		if filter.HasMeta(ft) {
			ins.fileGlobPatterns = append(ins.fileGlobPatterns, ft)
		} else {
			ins.explicitFilePaths = append(ins.explicitFilePaths, ft)
		}
	}

	return nil
}

func validateAndFillThresholds(check *ExpiryCheck, hasTargets bool) error {
	w := time.Duration(check.WarnWithin)
	c := time.Duration(check.CriticalWithin)

	if w > 0 && c > 0 && c >= w {
		return fmt.Errorf("critical_within(%s) must be less than warn_within(%s)", c, w)
	}

	if hasTargets && w == 0 && c == 0 {
		check.WarnWithin = config.Duration(720 * time.Hour)
		check.CriticalWithin = config.Duration(168 * time.Hour)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if len(ins.RemoteTargets) == 0 && len(ins.FileTargets) == 0 {
		return
	}

	wg := new(sync.WaitGroup)
	se := semaphore.NewSemaphore(ins.Concurrency)

	for _, target := range ins.RemoteTargets {
		wg.Add(1)
		go func(target string) {
			se.Acquire()
			defer func() {
				if r := recover(); r != nil {
					logger.Logger.Errorw("panic in cert checkRemote", "target", target, "recover", r)
					q.PushFront(ins.buildRemoteEvent(target).
						SetEventStatus(types.EventStatusCritical).
						SetDescription(fmt.Sprintf("panic during check: %v", r)))
				}
				se.Release()
				wg.Done()
			}()
			ins.checkRemote(q, target)
		}(target)
	}

	resolvedFiles := ins.resolveFileTargets()

	if len(resolvedFiles) > ins.MaxFileTargets {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "cert::file_expiry",
			"target": "glob",
		}).SetTitleRule("[check]").
			SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("file_targets resolved to %d files, exceeding max_file_targets(%d), only checking the first %d",
				len(resolvedFiles), ins.MaxFileTargets, ins.MaxFileTargets)))
		resolvedFiles = resolvedFiles[:ins.MaxFileTargets]
	}

	for _, filePath := range resolvedFiles {
		wg.Add(1)
		go func(filePath string) {
			se.Acquire()
			defer func() {
				if r := recover(); r != nil {
					logger.Logger.Errorw("panic in cert checkFile", "file", filePath, "recover", r)
					q.PushFront(ins.buildFileEvent(filePath).
						SetEventStatus(types.EventStatusCritical).
						SetDescription(fmt.Sprintf("panic during check: %v", r)))
				}
				se.Release()
				wg.Done()
			}()
			ins.checkFile(q, filePath)
		}(filePath)
	}

	wg.Wait()
}

func (ins *Instance) resolveFileTargets() []string {
	seen := make(map[string]bool)
	var files []string

	for _, p := range ins.explicitFilePaths {
		if !seen[p] {
			files = append(files, p)
			seen[p] = true
		}
	}

	for _, pattern := range ins.fileGlobPatterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			logger.Logger.Warnw("glob pattern error", "pattern", pattern, "error", err)
			continue
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || info.IsDir() {
				continue
			}
			if !seen[m] {
				files = append(files, m)
				seen[m] = true
			}
		}
	}

	return files
}

// --- Remote check ---

func (ins *Instance) checkRemote(q *safe.Queue[*types.Event], target string) {
	event := ins.buildRemoteEvent(target)
	timeout := time.Duration(ins.Timeout)

	// Determine SNI
	sni := ins.determineSNI(target)
	tlsCfg := ins.tlsConfig.Clone()
	tlsCfg.ServerName = sni
	event.Labels[types.AttrPrefix+"cert_sni"] = sni

	// TCP connect
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("TLS connection to %s failed: %v", target, err)))
		return
	}
	defer conn.Close()

	// STARTTLS negotiation
	if ins.StartTLS != "" {
		handler := starttlsHandlers[ins.StartTLS]
		if err := handler(conn, timeout); err != nil {
			q.PushFront(event.SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("%s STARTTLS negotiation with %s failed: %v",
					strings.ToUpper(ins.StartTLS), target, err)))
			return
		}
	}

	// TLS handshake
	tlsConn := tls.Client(conn, tlsCfg)
	_ = tlsConn.SetDeadline(time.Now().Add(timeout))
	if err := tlsConn.Handshake(); err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("TLS handshake with %s failed: %v", target, err)))
		return
	}
	defer tlsConn.Close()

	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("no peer certificates from %s", target)))
		return
	}

	cert, certIdx := earliestExpiry(certs)
	ins.evaluateExpiry(event, cert, certIdx, len(certs), ins.RemoteExpiry)
	q.PushFront(event)
}

func (ins *Instance) determineSNI(target string) string {
	if sni, ok := ins.targetSNI[target]; ok {
		return sni
	}
	if ins.ServerName != "" {
		return ins.ServerName
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return target
	}
	return host
}

// --- File check ---

func (ins *Instance) checkFile(q *safe.Queue[*types.Event], target string) {
	event := ins.buildFileEvent(target)

	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			q.PushFront(event.SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("certificate file not found: %s", target)))
		} else {
			q.PushFront(event.SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to stat %s: %v", target, err)))
		}
		return
	}

	if info.Size() > maxCertFileSize {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("file too large (%d bytes), likely not a certificate: %s", info.Size(), target)))
		return
	}

	data, err := os.ReadFile(target)
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read %s: %v", target, err)))
		return
	}

	certs, err := parseCerts(data)
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("no valid certificates found in %s: %v", target, err)))
		return
	}
	if len(certs) == 0 {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("no valid certificates found in %s", target)))
		return
	}

	cert, certIdx := earliestExpiry(certs)
	ins.evaluateExpiry(event, cert, certIdx, len(certs), ins.FileExpiry)
	q.PushFront(event)
}

func parseCerts(data []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate

	// Try PEM first
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		certs = append(certs, cert)
	}

	if len(certs) > 0 {
		return certs, nil
	}

	// Try DER
	cert, err := x509.ParseCertificate(data)
	if err != nil {
		return nil, fmt.Errorf("neither PEM nor DER: %v", err)
	}
	return []*x509.Certificate{cert}, nil
}

// --- Expiry evaluation ---

func (ins *Instance) evaluateExpiry(event *types.Event, cert *x509.Certificate, certIdx, chainLen int, check ExpiryCheck) {
	expiry := cert.NotAfter
	timeUntil := time.Until(expiry)
	now := time.Now()

	// Populate _attr_ labels
	event.Labels[types.AttrPrefix+"cert_subject"] = cert.Subject.String()
	event.Labels[types.AttrPrefix+"cert_issuer"] = cert.Issuer.String()
	event.Labels[types.AttrPrefix+"cert_serial"] = formatSerial(cert.SerialNumber)
	event.Labels[types.AttrPrefix+"cert_sha256"] = sha256Fingerprint(cert.Raw)
	event.Labels[types.AttrPrefix+"cert_not_before"] = cert.NotBefore.UTC().Format("2006-01-02 15:04:05")
	event.Labels[types.AttrPrefix+"cert_expires_at"] = expiry.UTC().Format("2006-01-02 15:04:05")
	if timeUntil >= 0 {
		event.Labels[types.AttrPrefix+"time_until_expiry"] = humanDuration(timeUntil)
	} else {
		event.Labels[types.AttrPrefix+"time_until_expiry"] = "-" + humanDuration(-timeUntil)
	}
	if len(cert.DNSNames) > 0 {
		event.Labels[types.AttrPrefix+"cert_dns_names"] = strings.Join(cert.DNSNames, ", ")
	}
	event.Labels[types.AttrPrefix+"cert_chain_count"] = strconv.Itoa(chainLen)
	event.Labels[types.AttrPrefix+"warn_within"] = humanDuration(time.Duration(check.WarnWithin))
	event.Labels[types.AttrPrefix+"critical_within"] = humanDuration(time.Duration(check.CriticalWithin))

	isIntermediate := certIdx > 0
	warnWithin := time.Duration(check.WarnWithin)
	criticalWithin := time.Duration(check.CriticalWithin)

	intermediatePrefix := ""
	if isIntermediate {
		intermediatePrefix = fmt.Sprintf("intermediate cert %s ", cert.Subject.String())
	}

	if now.Before(cert.NotBefore) {
		event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("%scert not yet valid (starts at %s)",
				intermediatePrefix, cert.NotBefore.UTC().Format("2006-01-02 15:04:05")))
	} else if timeUntil < 0 {
		event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("%sexpired %s ago (expired at %s)",
				intermediatePrefix, humanDuration(-timeUntil), expiry.UTC().Format("2006-01-02 15:04:05")))
	} else if criticalWithin > 0 && timeUntil <= criticalWithin {
		event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("%sexpires in %s, within critical threshold %s",
				intermediatePrefix, humanDuration(timeUntil), humanDuration(criticalWithin)))
	} else if warnWithin > 0 && timeUntil <= warnWithin {
		event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("%sexpires in %s, within warning threshold %s",
				intermediatePrefix, humanDuration(timeUntil), humanDuration(warnWithin)))
	} else {
		event.SetDescription(fmt.Sprintf("cert expires at %s, everything is ok",
			expiry.UTC().Format("2006-01-02 15:04:05")))
	}
}

func earliestExpiry(certs []*x509.Certificate) (*x509.Certificate, int) {
	earliest := certs[0]
	idx := 0
	for i := 1; i < len(certs); i++ {
		if certs[i].NotAfter.Before(earliest.NotAfter) {
			earliest = certs[i]
			idx = i
		}
	}
	return earliest, idx
}

// --- Event builders ---

func (ins *Instance) buildRemoteEvent(target string) *types.Event {
	tr := ins.RemoteExpiry.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}
	return types.BuildEvent(map[string]string{
		"check":  "cert::remote_expiry",
		"target": target,
	}).SetTitleRule(tr)
}

func (ins *Instance) buildFileEvent(target string) *types.Event {
	tr := ins.FileExpiry.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}
	return types.BuildEvent(map[string]string{
		"check":  "cert::file_expiry",
		"target": target,
	}).SetTitleRule(tr)
}

// --- Formatting helpers ---

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24

	if days > 0 {
		return fmt.Sprintf("%d days %d hours", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%d hours %d minutes", hours, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

func formatSerial(n *big.Int) string {
	if n == nil {
		return ""
	}
	b := n.Bytes()
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprintf("%02X", v)
	}
	return strings.Join(parts, ":")
}

func sha256Fingerprint(raw []byte) string {
	sum := sha256.Sum256(raw)
	parts := make([]string, len(sum))
	for i, v := range sum {
		parts[i] = fmt.Sprintf("%02X", v)
	}
	return strings.Join(parts, ":")
}

// --- SMTP STARTTLS ---

func smtpStartTLS(conn net.Conn, timeout time.Duration) error {
	reader := bufio.NewReader(conn)

	// Read banner (220)
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	banner, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read SMTP banner: %v", err)
	}
	if !strings.HasPrefix(banner, "220") {
		return fmt.Errorf("unexpected SMTP banner: %s", strings.TrimSpace(banner))
	}

	// Send EHLO
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(conn, "EHLO catpaw\r\n"); err != nil {
		return fmt.Errorf("failed to send EHLO: %v", err)
	}

	// Read EHLO response (multi-line: 250- ... 250 )
	for {
		if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read EHLO response: %v", err)
		}
		if len(line) < 4 {
			return fmt.Errorf("unexpected EHLO response: %s", strings.TrimSpace(line))
		}
		if !strings.HasPrefix(line, "250") {
			return fmt.Errorf("EHLO rejected: %s", strings.TrimSpace(line))
		}
		// "250 " (space) indicates last line
		if line[3] == ' ' {
			break
		}
	}

	// Send STARTTLS
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(conn, "STARTTLS\r\n"); err != nil {
		return fmt.Errorf("failed to send STARTTLS: %v", err)
	}

	// Read STARTTLS response (220)
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	resp, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read STARTTLS response: %v", err)
	}
	if !strings.HasPrefix(resp, "220") {
		return fmt.Errorf("STARTTLS rejected: %s", strings.TrimSpace(resp))
	}

	// Reset deadline for TLS handshake
	_ = conn.SetDeadline(time.Time{})
	return nil
}
