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
	"github.com/cprobe/catpaw/types"
)

const (
	heartbeatInterval  = 30 * time.Second
	writeTimeout       = 10 * time.Second
	readTimeout        = 90 * time.Second
	ackTimeout         = 10 * time.Second
	sendChSize         = 64
	alertFlushInterval = 1 * time.Second
	alertFlushBatch    = 100
)

// alertRing is the package-level ring buffer shared across connection attempts.
// Events survive disconnects and are flushed after reconnection.
var alertRing *RingBuffer

// InitAlertBuffer creates the package-level alert ring buffer.
// Must be called once before RunForever.
func InitAlertBuffer(capacity int) {
	alertRing = NewRingBuffer(capacity)
}

// SendAlertEvent enqueues an alert event into the ring buffer.
// Safe to call even when the WebSocket connection is down.
// NOTE: the event pointer is stored as-is; callers must not reuse/mutate
// the Event after this call. The current engine always creates fresh Events.
func SendAlertEvent(event *types.Event) {
	if alertRing == nil {
		return
	}
	alertRing.Push(event)
}

// Conn manages one WebSocket connection to catpaw-server.
type Conn struct {
	cfg           config.ServerConfig
	agentID       uuid.UUID
	ws            *websocket.Conn
	startTime     time.Time
	plugins       []string
	sendCh        chan []byte // TODO(Phase4/5): replace with priority dual-queue (session > heartbeat/alert)
	done          chan struct{}
	cancel        context.CancelFunc
	closeOnce     sync.Once
	retryAfterSec int // set by handleServerMessage on disconnect
	sessions      *sessionManager
}

// errAuthFailed signals that the Server rejected the agent token (401).
// RunForever uses this to apply the slower auth-failure backoff.
var errAuthFailed = errors.New("authentication failed")

var errConnectionLost = errors.New("connection lost")

// disconnectError wraps a server-requested disconnect with retry_after_sec.
type disconnectError struct {
	retryAfterSec int
}

func (e *disconnectError) Error() string {
	return fmt.Sprintf("server requested disconnect (retry_after_sec=%d)", e.retryAfterSec)
}

// reconnectError annotates retry behavior for RunForever while preserving
// the original error text for logging.
type reconnectError struct {
	err          error
	resetBackoff bool
}

func (e *reconnectError) Error() string {
	return e.err.Error()
}

func (e *reconnectError) Unwrap() error {
	return e.err
}

// Run performs a single connection lifecycle: dial → register → ack → loops.
// Returns nil only when ctx is cancelled (clean shutdown).
func Run(ctx context.Context, startTime time.Time, plugins []string) error {
	cfg := config.Config.Server
	if !cfg.Enabled || cfg.Address == "" {
		return nil
	}
	wsURL, err := cfg.WebSocketURL()
	if err != nil {
		return fmt.Errorf("resolve server websocket url: %w", err)
	}

	agentID, err := loadOrCreateAgentID()
	if err != nil {
		return fmt.Errorf("load agent_id: %w", err)
	}

	logger.Logger.Infow("server_connecting", "url", wsURL, "agent_id", agentID)

	dialOpts := &websocket.DialOptions{
		HTTPHeader: buildHeaders(cfg, agentID),
	}
	if hc, err := buildHTTPClient(cfg); err != nil {
		return fmt.Errorf("tls config: %w", err)
	} else if hc != nil {
		dialOpts.HTTPClient = hc
	}

	ws, resp, err := websocket.Dial(ctx, wsURL, dialOpts)
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
		sessions:  newSessionManager(),
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
	wg.Add(3)
	go func() { defer wg.Done(); c.writeLoop(connCtx) }()
	go func() { defer wg.Done(); c.heartbeatLoop(connCtx) }()
	go func() { defer wg.Done(); c.alertFlushLoop(connCtx) }()

	c.readLoop(connCtx)

	cancel()
	c.sessions.cancelAll()
	c.closeOnce.Do(func() { close(c.done) })
	wg.Wait()

	ws.Close(websocket.StatusNormalClosure, "")
	logger.Logger.Infow("server_disconnected", "agent_id", agentID)

	if c.retryAfterSec > 0 {
		return &disconnectError{retryAfterSec: c.retryAfterSec}
	}
	return &reconnectError{err: errConnectionLost, resetBackoff: true}
}

// RunForever wraps Run with reconnect logic. It blocks until ctx is cancelled.
// Backoff strategy per proto.md §7:
//   - Normal disconnect: 1s → 2s → ... → 300s, ±25% jitter
//   - Auth failure (401): 60s → 120s → ... → 1800s, ±25% jitter
func RunForever(ctx context.Context, startTime time.Time, plugins []string) {
	cfg := config.Config.Server
	if !cfg.Enabled || cfg.Address == "" {
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

		wait, nextBackoff, logFn := nextReconnectState(err, backoff, normalMin, normalMax, authMin, authMax)
		if logFn != nil {
			logFn(err, wait)
		}
		backoff = nextBackoff

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
		logger.Logger.Debugw("ws_unexpected_ack", "agent_id", c.agentID, "ref_id", msg.RefID)

	case typePing:
		// Server keepalive — no action needed; the read itself resets the deadline.

	case typeSessionStart:
		c.handleSessionStart(ctx, msg)
	case typeSessionInput:
		c.handleSessionInput(msg)
	case typeSessionCancel:
		c.handleSessionCancel(msg)

	default:
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
				ActiveSessions: c.sessions.count(),
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

// alertFlushLoop drains the shared alert ring buffer every 1s and sends
// batched alert_events messages via sendCh.
func (c *Conn) alertFlushLoop(ctx context.Context) {
	if alertRing == nil {
		return
	}

	ticker := time.NewTicker(alertFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.flushAlertEvents()
		case <-ctx.Done():
			return
		}
	}
}

func (c *Conn) flushAlertEvents() {
	for {
		events := alertRing.Drain(alertFlushBatch)
		if len(events) == 0 {
			return
		}

		items := make([]alertEventItem, len(events))
		for i, e := range events {
			items[i] = alertEventItem{
				EventTime:         e.EventTime,
				EventStatus:       e.EventStatus,
				AlertKey:          e.AlertKey,
				Labels:            e.Labels,
				Attrs:             e.Attrs,
				Description:       e.Description,
				DescriptionFormat: e.DescriptionFormat,
			}
		}

		msg, err := newMessage(typeAlertEvents, alertEventsPayload{Events: items})
		if err != nil {
			logger.Logger.Warnw("alert_events_marshal_failed", "error", err)
			return
		}
		data, err := json.Marshal(msg)
		if err != nil {
			return
		}

		select {
		case c.sendCh <- data:
		case <-c.done:
			return
		}
	}
}

func (c *Conn) sendRegister(ctx context.Context) error {
	hostname, _ := os.Hostname()
	ip := config.AgentIP()

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
	h.Set("X-Agent-Token", cfg.AgentToken)
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

func nextReconnectState(
	err error,
	backoff time.Duration,
	normalMin time.Duration,
	normalMax time.Duration,
	authMin time.Duration,
	authMax time.Duration,
) (time.Duration, time.Duration, func(error, time.Duration)) {
	if err == nil {
		return 0, backoff, nil
	}

	var de *disconnectError
	if errors.As(err, &de) {
		wait := time.Duration(de.retryAfterSec) * time.Second
		if wait < normalMin {
			wait = normalMin
		}
		return wait, normalMin, func(err error, wait time.Duration) {
			logger.Logger.Infow("server_disconnect_retry", "error", err, "retry_in", wait)
		}
	}

	if errors.Is(err, errAuthFailed) {
		if backoff < authMin {
			backoff = authMin
		}
		wait := backoff
		return wait, clampBackoff(backoff*2, authMin, authMax), func(err error, wait time.Duration) {
			logger.Logger.Errorw("server_auth_failed", "error", err, "retry_in", wait)
		}
	}

	var re *reconnectError
	if errors.As(err, &re) && re.resetBackoff {
		backoff = normalMin
	}

	wait := backoff
	return wait, clampBackoff(backoff*2, normalMin, normalMax), func(err error, wait time.Duration) {
		logger.Logger.Warnw("server_disconnected", "error", err, "retry_in", wait)
	}
}

func jitter(d time.Duration, pct float64) time.Duration {
	delta := float64(d) * pct
	return d + time.Duration((rand.Float64()*2-1)*delta)
}
