package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
)

const (
	heartbeatInterval = 30 * time.Second
	writeTimeout      = 10 * time.Second
	readTimeout       = 90 * time.Second
	ackTimeout        = 10 * time.Second
	sendChSize        = 16
)

// Conn manages one WebSocket connection to catpaw-server.
type Conn struct {
	cfg            config.ServerConfig
	agentID        uuid.UUID
	ws             *websocket.Conn
	startTime      time.Time
	plugins        []string
	sendCh         chan []byte // TODO(Phase4/5): replace with priority dual-queue (session > heartbeat/alert)
	done           chan struct{}
	cancel         context.CancelFunc
	closeOnce      sync.Once
	retryAfterSec  int // set by handleServerMessage on disconnect
}

// errAuthFailed signals that the Server rejected the tenant_token (401).
// RunForever uses this to apply the slower auth-failure backoff.
var errAuthFailed = errors.New("authentication failed")

// disconnectError wraps a server-requested disconnect with retry_after_sec.
type disconnectError struct {
	retryAfterSec int
}

func (e *disconnectError) Error() string {
	return fmt.Sprintf("server requested disconnect (retry_after_sec=%d)", e.retryAfterSec)
}

// Run performs a single connection lifecycle: dial → register → ack → loops.
// Returns nil only when ctx is cancelled (clean shutdown).
func Run(ctx context.Context, startTime time.Time, plugins []string) error {
	cfg := config.Config.Server
	if !cfg.Enabled || cfg.URL == "" {
		return nil
	}

	agentID, err := loadOrCreateAgentID()
	if err != nil {
		return fmt.Errorf("load agent_id: %w", err)
	}

	logger.Logger.Infow("server_connecting", "url", cfg.URL, "agent_id", agentID)

	dialOpts := &websocket.DialOptions{
		HTTPHeader: buildHeaders(cfg, agentID),
	}
	if hc, err := buildHTTPClient(cfg); err != nil {
		return fmt.Errorf("tls config: %w", err)
	} else if hc != nil {
		dialOpts.HTTPClient = hc
	}

	ws, resp, err := websocket.Dial(ctx, cfg.URL, dialOpts)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w: %v", errAuthFailed, err)
		}
		return fmt.Errorf("ws dial: %w", err)
	}
	ws.SetReadLimit(1 << 20) // 1 MiB

	c := &Conn{
		cfg:       cfg,
		agentID:   agentID,
		ws:        ws,
		startTime: startTime,
		plugins:   plugins,
		sendCh:    make(chan []byte, sendChSize),
		done:      make(chan struct{}),
	}

	logger.Logger.Infow("server_connected", "agent_id", agentID)

	if err := c.sendRegister(ctx); err != nil {
		ws.Close(websocket.StatusInternalError, "register failed")
		return fmt.Errorf("register: %w", err)
	}

	ack, err := c.recvAck(ctx)
	if err != nil {
		ws.Close(websocket.StatusInternalError, "ack read failed")
		return fmt.Errorf("register ack: %w", err)
	}
	if !ack.OK {
		ws.Close(websocket.StatusNormalClosure, "register rejected")
		return fmt.Errorf("register rejected: %s", ack.Error)
	}
	if ack.Warning != "" {
		logger.Logger.Warnw("server_register_warning", "warning", ack.Warning)
	}

	logger.Logger.Infow("server_registered", "agent_id", agentID)

	connCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.writeLoop(connCtx) }()
	go func() { defer wg.Done(); c.heartbeatLoop(connCtx) }()

	c.readLoop(connCtx)

	cancel()
	c.closeOnce.Do(func() { close(c.done) })
	wg.Wait()

	ws.Close(websocket.StatusNormalClosure, "")
	logger.Logger.Infow("server_disconnected", "agent_id", agentID)

	if c.retryAfterSec > 0 {
		return &disconnectError{retryAfterSec: c.retryAfterSec}
	}
	return fmt.Errorf("connection lost")
}

// RunForever wraps Run with reconnect logic. It blocks until ctx is cancelled.
// Backoff strategy per proto.md §7:
//   - Normal disconnect: 1s → 2s → ... → 300s, ±25% jitter
//   - Auth failure (401): 60s → 120s → ... → 1800s, ±25% jitter
func RunForever(ctx context.Context, startTime time.Time, plugins []string) {
	cfg := config.Config.Server
	if !cfg.Enabled || cfg.URL == "" {
		return
	}

	const (
		normalMin = 1 * time.Second
		normalMax = 300 * time.Second
		authMin   = 60 * time.Second
		authMax   = 1800 * time.Second
	)
	backoff := normalMin

	for {
		err := Run(ctx, startTime, plugins)
		if ctx.Err() != nil {
			return
		}

		var de *disconnectError
		var wait time.Duration

		switch {
		case errors.As(err, &de):
			wait = time.Duration(de.retryAfterSec) * time.Second
			if wait < normalMin {
				wait = normalMin
			}
			logger.Logger.Infow("server_disconnect_retry", "error", err, "retry_in", wait)
			backoff = normalMin
		case errors.Is(err, errAuthFailed):
			if backoff < authMin {
				backoff = authMin
			}
			wait = backoff
			logger.Logger.Errorw("server_auth_failed", "error", err, "retry_in", wait)
			backoff = clampBackoff(backoff*2, authMin, authMax)
		case err != nil:
			wait = backoff
			logger.Logger.Warnw("server_disconnected", "error", err, "retry_in", wait)
			backoff = clampBackoff(backoff*2, normalMin, normalMax)
		}

		jittered := jitter(wait, 0.25)
		select {
		case <-ctx.Done():
			return
		case <-time.After(jittered):
		}
	}
}

// readLoop reads Server messages until connection loss or context cancellation.
func (c *Conn) readLoop(ctx context.Context) {
	for {
		readCtx, cancel := context.WithTimeout(ctx, readTimeout)
		_, data, err := c.ws.Read(readCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				logger.Logger.Warnw("ws_read_error", "agent_id", c.agentID, "error", err)
			}
			return
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			logger.Logger.Warnw("ws_invalid_json", "agent_id", c.agentID, "error", err)
			continue
		}

		c.handleServerMessage(ctx, &msg)
	}
}

func (c *Conn) handleServerMessage(ctx context.Context, msg *Message) {
	switch msg.Type {
	case typeDisconnect:
		var payload disconnectPayload
		if err := msg.decodePayload(&payload); err == nil {
			c.retryAfterSec = payload.RetryAfterSec
			logger.Logger.Infow("server_requested_disconnect",
				"agent_id", c.agentID,
				"reason", payload.Reason,
				"retry_after_sec", payload.RetryAfterSec,
			)
		}
		c.cancel()
	case typeAck:
		// Unexpected ack (not from register flow); log and ignore.
		logger.Logger.Debugw("ws_unexpected_ack", "agent_id", c.agentID, "ref_id", msg.RefID)
	default:
		// TODO(Phase5): handle session_start, session_cancel, etc.
		logger.Logger.Debugw("ws_unhandled_type", "agent_id", c.agentID, "type", msg.Type)
	}
}

// writeLoop drains sendCh and writes to the WebSocket.
func (c *Conn) writeLoop(ctx context.Context) {
	for {
		select {
		case data := <-c.sendCh:
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				logger.Logger.Warnw("ws_write_failed", "agent_id", c.agentID, "error", err)
				c.cancel()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// heartbeatLoop sends a heartbeat message every 30s.
func (c *Conn) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			msg, err := newMessage(typeHeartbeat, heartbeatPayload{
				// TODO(Phase5): fill active_sessions from session manager
				// TODO(Phase4): fill cpu_pct, mem_pct from local metrics
				ActiveAlerts: 0,
			})
			if err != nil {
				logger.Logger.Warnw("heartbeat_marshal_failed", "error", err)
				continue
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}

			select {
			case c.sendCh <- data:
			case <-c.done:
				return
			default:
				logger.Logger.Warnw("heartbeat_send_buffer_full", "agent_id", c.agentID)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Conn) sendRegister(ctx context.Context) error {
	hostname, _ := os.Hostname()
	ip := config.DetectIP()

	msg, err := newMessage(typeRegister, registerPayload{
		Hostname:     hostname,
		IP:           ip,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Labels:       config.Config.Global.Labels,
		Plugins:      c.plugins,
		AgentVersion: config.Version,
		UptimeSec:    int64(time.Since(c.startTime).Seconds()),
	})
	if err != nil {
		return err
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal register: %w", err)
	}
	return c.ws.Write(ctx, websocket.MessageText, data)
}

func (c *Conn) recvAck(ctx context.Context) (*ackPayload, error) {
	readCtx, cancel := context.WithTimeout(ctx, ackTimeout)
	defer cancel()

	_, data, err := c.ws.Read(readCtx)
	if err != nil {
		return nil, fmt.Errorf("read ack: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal ack: %w", err)
	}
	if msg.Type != typeAck {
		return nil, fmt.Errorf("expected ack, got %q", msg.Type)
	}

	var payload ackPayload
	if err := msg.decodePayload(&payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func buildHeaders(cfg config.ServerConfig, agentID uuid.UUID) http.Header {
	h := http.Header{}
	h.Set("X-Tenant-Token", cfg.TenantToken)
	h.Set("X-Agent-ID", agentID.String())
	h.Set("X-Proto-Version", "1")
	return h
}

// buildHTTPClient returns an *http.Client with custom TLS when ca_file or
// tls_skip_verify is configured. Returns (nil, nil) when no custom TLS needed.
func buildHTTPClient(cfg config.ServerConfig) (*http.Client, error) {
	if cfg.CAFile == "" && !cfg.TLSSkipVerify {
		return nil, nil
	}

	tlsCfg := &tls.Config{
		InsecureSkipVerify: cfg.TLSSkipVerify, //nolint:gosec // user explicitly opted in
	}

	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca_file %s: %w", cfg.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %s contains no valid certificates", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}

	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, nil
}

func clampBackoff(d, min, max time.Duration) time.Duration {
	if d < min {
		return min
	}
	if d > max {
		return max
	}
	return d
}

func jitter(d time.Duration, pct float64) time.Duration {
	delta := float64(d) * pct
	return d + time.Duration((rand.Float64()*2-1)*delta)
}
