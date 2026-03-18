// session_handler.go handles session_start / session_input / session_cancel
// messages from the Server and dispatches session execution (inspect, diagnose, chat).
package server

import (
	"context"
	"time"

	"github.com/cprobe/digcore/logger"
)

func (c *Conn) handleSessionStart(ctx context.Context, msg *Message) {
	var payload sessionStartPayload
	if err := msg.decodePayload(&payload); err != nil {
		logger.Logger.Warnw("session_start_acked",
			"agent_id", c.agentID, "ref_id", msg.ID,
			"ok", false, "reason", "invalid_payload", "error", err,
		)
		c.sendACK(msg.ID, false, "invalid payload")
		return
	}

	if payload.SessionID == "" {
		logger.Logger.Warnw("session_start_acked",
			"agent_id", c.agentID, "ref_id", msg.ID,
			"ok", false, "reason", "missing_session_id",
		)
		c.sendACK(msg.ID, false, "missing session_id")
		return
	}

	if concurrencyLimiter == nil {
		logger.Logger.Warnw("session_start_acked",
			"agent_id", c.agentID, "ref_id", msg.ID,
			"session_id", payload.SessionID, "session_type", payload.SessionType,
			"ok", false, "reason", "engine_unavailable",
		)
		c.sendACK(msg.ID, false, "diagnose engine not available")
		return
	}

	if !concurrencyLimiter.TrySem() {
		logger.Logger.Warnw("session_start_acked",
			"agent_id", c.agentID, "ref_id", msg.ID,
			"session_id", payload.SessionID, "session_type", payload.SessionType,
			"ok", false, "reason", "max_concurrent",
		)
		c.sendACK(msg.ID, false, "max concurrent sessions reached")
		return
	}

	sessCtx, cancel := context.WithCancel(ctx)
	sess := &remoteSession{
		sessionID:   payload.SessionID,
		sessionType: payload.SessionType,
		cancel:      cancel,
	}
	if payload.SessionType == "chat" {
		sess.inputCh = make(chan string, 8)
	}

	if !c.sessions.add(sess) {
		cancel()
		concurrencyLimiter.ReleaseSem()
		logger.Logger.Warnw("session_start_acked",
			"agent_id", c.agentID, "ref_id", msg.ID,
			"session_id", payload.SessionID,
			"ok", false, "reason", "duplicate_session_id",
		)
		c.sendACK(msg.ID, false, "duplicate session_id")
		return
	}

	logger.Logger.Infow("session_start_acked",
		"agent_id", c.agentID, "ref_id", msg.ID,
		"session_id", payload.SessionID, "session_type", payload.SessionType,
		"user", payload.UserName, "ok", true,
	)
	c.sendACK(msg.ID, true, "")

	go func() {
		defer concurrencyLimiter.ReleaseSem()
		defer c.sessions.remove(payload.SessionID)
		c.runRemoteSession(sessCtx, sess, &payload)
	}()
}

func (c *Conn) handleSessionInput(msg *Message) {
	var payload sessionInputPayload
	if err := msg.decodePayload(&payload); err != nil {
		logger.Logger.Warnw("session_input_decode_failed", "agent_id", c.agentID, "error", err)
		return
	}

	sess := c.sessions.get(payload.SessionID)
	if sess == nil {
		logger.Logger.Warnw("session_input_no_session", "agent_id", c.agentID, "session_id", payload.SessionID)
		return
	}
	if sess.inputCh == nil {
		logger.Logger.Warnw("session_input_not_chat", "agent_id", c.agentID, "session_id", payload.SessionID)
		return
	}

	if payload.Message == "" {
		return
	}

	select {
	case sess.inputCh <- payload.Message:
		logger.Logger.Debugw("session_input_delivered",
			"agent_id", c.agentID, "session_id", payload.SessionID,
			"message_len", len(payload.Message))
	default:
		logger.Logger.Warnw("session_input_buffer_full", "agent_id", c.agentID, "session_id", payload.SessionID)
	}
}

func (c *Conn) handleSessionCancel(msg *Message) {
	var payload sessionCancelPayload
	if err := msg.decodePayload(&payload); err != nil {
		logger.Logger.Warnw("session_cancel_decode_failed", "agent_id", c.agentID, "error", err)
		return
	}

	found := c.sessions.get(payload.SessionID) != nil
	c.sessions.remove(payload.SessionID)
	logger.Logger.Infow("session_cancel_handled",
		"agent_id", c.agentID, "ref_id", msg.ID,
		"session_id", payload.SessionID, "reason", payload.Reason,
		"found", found,
	)
}

// runRemoteSession dispatches to the appropriate handler based on session type.
func (c *Conn) runRemoteSession(ctx context.Context, sess *remoteSession, payload *sessionStartPayload) {
	start := time.Now()
	outcome := "ok"
	doneSent := false

	logger.Logger.Infow("remote_session_started",
		"agent_id", c.agentID,
		"session_id", sess.sessionID,
		"session_type", sess.sessionType,
	)

	defer func() {
		if !doneSent {
			c.sendSessionOutput(sess.sessionID, "", "", true, "", nil)
		}
		logger.Logger.Infow("remote_session_ended",
			"agent_id", c.agentID,
			"session_id", sess.sessionID,
			"session_type", sess.sessionType,
			"outcome", outcome,
			"duration_sec", time.Since(start).Seconds(),
		)
	}()

	select {
	case <-ctx.Done():
		outcome = "cancelled"
		return
	default:
	}

	switch sess.sessionType {
	case "inspect", "diagnose":
		var report string
		outcome, report = c.runInspectOrDiagnose(ctx, sess, payload)
		if report != "" {
			c.sendSessionOutput(sess.sessionID, "", "", true, report, nil)
			doneSent = true
		}
	case "chat":
		outcome = c.runChat(ctx, sess, payload)
	default:
		outcome = "error"
		c.sendSessionError(sess.sessionID, "unknown session type: "+sess.sessionType, "invalid_type")
	}
}

// runInspectOrDiagnose executes a remote inspect or diagnose session.
// Returns the outcome string and final report (empty on error/cancel).
func (c *Conn) runInspectOrDiagnose(ctx context.Context, sess *remoteSession, payload *sessionStartPayload) (string, string) {
	if diagnoseRunner == nil {
		c.sendSessionError(sess.sessionID, "diagnose engine not available", "engine_unavailable")
		return "error", ""
	}

	params := payload.Params
	if params == nil {
		params = make(map[string]any)
	}

	plugin, _ := params["plugin"].(string)
	target, _ := params["target"].(string)

	if sess.sessionType == "inspect" && plugin == "" {
		c.sendSessionError(sess.sessionID, "plugin is required for inspect", "missing_plugin")
		return "error", ""
	}
	if plugin == "" {
		plugin = "system"
	}
	if target == "" {
		target = "localhost"
	}

	mode := "inspect"
	if sess.sessionType == "diagnose" {
		mode = "alert"
	}

	cb := func(delta, stage string, done bool, metadata map[string]any) {
		c.sendSessionOutput(sess.sessionID, delta, stage, false, "", metadata)
	}

	report, err := diagnoseRunner.RunStreaming(ctx, mode, plugin, target, params, cb)
	if err != nil {
		if ctx.Err() != nil {
			return "cancelled", ""
		}
		c.sendSessionError(sess.sessionID, err.Error(), "diagnose_failed")
		return "error", ""
	}

	return "ok", report
}

// runChat executes a remote chat session with multi-turn conversation.
// Returns the outcome string for logging.
func (c *Conn) runChat(ctx context.Context, sess *remoteSession, payload *sessionStartPayload) string {
	if chatRunner == nil {
		logger.Logger.Warnw("chat_session_rejected",
			"agent_id", c.agentID, "session_id", sess.sessionID,
			"reason", "engine_unavailable")
		c.sendSessionError(sess.sessionID, "chat engine not available", "engine_unavailable")
		return "error"
	}

	if ctx.Err() != nil {
		return "cancelled"
	}

	params := payload.Params
	if params == nil {
		params = make(map[string]any)
	}

	allowShell, _ := params["allow_shell"].(bool)

	cb := func(delta, stage string, done bool, metadata map[string]any) {
		c.sendSessionOutput(sess.sessionID, delta, stage, false, "", metadata)
	}

	handle, err := chatRunner.NewSession(ctx, ChatSessionOpts{AllowShell: allowShell}, cb)
	if err != nil {
		if ctx.Err() != nil {
			return "cancelled"
		}
		logger.Logger.Warnw("chat_session_init_failed",
			"agent_id", c.agentID, "session_id", sess.sessionID,
			"error", err)
		c.sendSessionError(sess.sessionID, err.Error(), "chat_init_failed")
		return "error"
	}

	turnCount := 0
	processTurn := func(msg string) (outcome string) {
		defer func() {
			if r := recover(); r != nil {
				logger.Logger.Errorw("chat_turn_panic",
					"agent_id", c.agentID, "session_id", sess.sessionID,
					"turn", turnCount, "panic", r)
				c.sendSessionOutput(sess.sessionID, "internal error", "error", false, "", map[string]any{"turn_done": true})
			}
		}()

		turnCount++
		reply, err := handle.HandleMessage(ctx, msg)
		if err != nil {
			if ctx.Err() != nil {
				return "cancelled"
			}
			logger.Logger.Warnw("chat_turn_failed",
				"agent_id", c.agentID, "session_id", sess.sessionID,
				"turn", turnCount, "error", err, "input_len", len(msg))
			c.sendSessionOutput(sess.sessionID, err.Error(), "error", false, "", map[string]any{"turn_done": true})
			return ""
		}
		c.sendSessionOutput(sess.sessionID, reply, "answer", false, "", map[string]any{"turn_done": true})
		return ""
	}

	defer func() {
		logger.Logger.Infow("chat_session_summary",
			"agent_id", c.agentID, "session_id", sess.sessionID,
			"turn_count", turnCount)
	}()

	if msg, _ := params["message"].(string); msg != "" {
		if outcome := processTurn(msg); outcome != "" {
			return outcome
		}
	}

	for {
		select {
		case <-ctx.Done():
			return "cancelled"
		case msg := <-sess.inputCh:
			if outcome := processTurn(msg); outcome != "" {
				return outcome
			}
		}
	}
}
