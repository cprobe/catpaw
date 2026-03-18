// session.go manages remote session state (sessionManager, remoteSession, ConcurrencyLimiter)
// and provides Conn methods for sending session-related messages to the Server
// (sendSessionOutput, sendSessionError, sendACK).
package server

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/cprobe/digcore/logger"
)

// ConcurrencyLimiter controls how many concurrent remote sessions can run.
// Typically implemented by diagnose.DiagnoseEngine (shared semaphore).
type ConcurrencyLimiter interface {
	TrySem() bool
	ReleaseSem()
}

// StreamCallback receives streaming output during a remote diagnosis.
// Signature must stay in sync with diagnose.StreamCallback.
type StreamCallback func(delta, stage string, done bool, metadata map[string]any)

// DiagnoseRunner executes a streaming diagnosis. Implemented by diagnose.DiagnoseEngine.
type DiagnoseRunner interface {
	RunStreaming(ctx context.Context, mode, plugin, target string, params map[string]any, cb StreamCallback) (report string, err error)
}

// ChatRunner creates and manages remote chat sessions.
type ChatRunner interface {
	NewSession(ctx context.Context, opts ChatSessionOpts, cb StreamCallback) (ChatHandle, error)
}

// ChatSessionOpts configures a remote chat session.
type ChatSessionOpts struct {
	AllowShell bool
}

// ChatHandle is a handle to an active chat session.
type ChatHandle interface {
	HandleMessage(ctx context.Context, input string) (reply string, err error)
}

var (
	concurrencyLimiter ConcurrencyLimiter
	diagnoseRunner     DiagnoseRunner
	chatRunner         ChatRunner
)

// SetConcurrencyLimiter sets the global concurrency limiter for remote sessions.
// Must be called before any WebSocket connections are established.
func SetConcurrencyLimiter(l ConcurrencyLimiter) {
	concurrencyLimiter = l
}

// SetDiagnoseRunner sets the global diagnose runner for remote sessions.
func SetDiagnoseRunner(r DiagnoseRunner) {
	diagnoseRunner = r
}

// SetChatRunner sets the global chat runner for remote chat sessions.
func SetChatRunner(r ChatRunner) {
	chatRunner = r
}

// remoteSession tracks one active remote operation.
type remoteSession struct {
	sessionID   string
	sessionType string // "inspect", "diagnose", "chat"
	cancel      context.CancelFunc
	inputCh     chan string // chat user messages; nil for inspect/diagnose
}

// sessionManager tracks all active remote sessions on this Agent.
type sessionManager struct {
	mu       sync.Mutex
	sessions map[string]*remoteSession // sessionID → session
}

func newSessionManager() *sessionManager {
	return &sessionManager{
		sessions: make(map[string]*remoteSession),
	}
}

// add registers a new remote session. Returns false if sessionID already exists.
func (m *sessionManager) add(s *remoteSession) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[s.sessionID]; exists {
		return false
	}
	m.sessions[s.sessionID] = s
	return true
}

// remove unregisters a session and cancels its context.
func (m *sessionManager) remove(sessionID string) {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if ok && s.cancel != nil {
		s.cancel()
	}
}

// get returns the session, or nil if not found.
func (m *sessionManager) get(sessionID string) *remoteSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

// count returns the number of active sessions.
func (m *sessionManager) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// cancelAll cancels and removes all active sessions (e.g. on disconnect).
func (m *sessionManager) cancelAll() {
	m.mu.Lock()
	snapshot := make(map[string]*remoteSession, len(m.sessions))
	for k, v := range m.sessions {
		snapshot[k] = v
	}
	m.sessions = make(map[string]*remoteSession)
	m.mu.Unlock()

	for _, s := range snapshot {
		if s.cancel != nil {
			s.cancel()
		}
	}
	if len(snapshot) > 0 {
		logger.Logger.Infow("remote_sessions_cancelled", "count", len(snapshot))
	}
}

func (c *Conn) sendSessionOutput(sessionID, delta, stage string, done bool, report string, metadata map[string]any) {
	msg, err := newMessage(typeSessionOutput, sessionOutputPayload{
		SessionID: sessionID,
		Delta:     delta,
		Stage:     stage,
		Done:      done,
		Report:    report,
		Metadata:  metadata,
	})
	if err != nil {
		logger.Logger.Warnw("session_output_marshal_failed", "agent_id", c.agentID, "session_id", sessionID, "error", err)
		return
	}
	data, _ := json.Marshal(msg)
	select {
	case c.sendCh <- data:
	case <-c.done:
		logger.Logger.Debugw("session_output_dropped", "agent_id", c.agentID, "session_id", sessionID, "done", done)
	}
}

func (c *Conn) sendSessionError(sessionID, errMsg, code string) {
	msg, err := newMessage(typeSessionError, sessionErrorPayload{
		SessionID: sessionID,
		Error:     errMsg,
		Code:      code,
	})
	if err != nil {
		logger.Logger.Warnw("session_error_marshal_failed", "agent_id", c.agentID, "session_id", sessionID, "error", err)
		return
	}
	data, _ := json.Marshal(msg)
	select {
	case c.sendCh <- data:
	case <-c.done:
		logger.Logger.Debugw("session_error_dropped", "agent_id", c.agentID, "session_id", sessionID, "code", code)
	}
}

func (c *Conn) sendACK(refID string, ok bool, errMsg string) {
	msg := &Message{
		Type:  typeAck,
		ID:    newMsgID(),
		RefID: refID,
	}
	payload := ackPayload{OK: ok, Error: errMsg}
	raw, _ := json.Marshal(payload)
	msg.Payload = raw
	data, _ := json.Marshal(msg)
	select {
	case c.sendCh <- data:
	case <-c.done:
		logger.Logger.Debugw("ack_dropped", "agent_id", c.agentID, "ref_id", refID, "ok", ok)
	}
}
