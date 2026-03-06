package server

// ============================================================================
// SYNC WARNING: These types mirror catpaw-server/internal/pkg/proto/.
// Any structural change (field add/remove/rename, type change, JSON tag change)
// MUST be applied to both repositories simultaneously.
// ============================================================================

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// Message types — Agent -> Server
const (
	typeRegister    = "register"
	typeHeartbeat   = "heartbeat"
	typeAlertEvents = "alert_events"
)

// Message types — Server -> Agent
const (
	typeAck        = "ack"
	typeDisconnect = "disconnect"
)

// Message is the protocol envelope for all Agent <-> Server communication.
type Message struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	RefID   string          `json:"ref_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func newMessage(typ string, payload any) (*Message, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s payload: %w", typ, err)
	}
	return &Message{
		Type:    typ,
		ID:      uuid.NewString(),
		Payload: raw,
	}, nil
}

func (m *Message) decodePayload(target any) error {
	if len(m.Payload) == 0 {
		return fmt.Errorf("message %q has no payload", m.Type)
	}
	return json.Unmarshal(m.Payload, target)
}

// --- Agent -> Server payloads ---

type registerPayload struct {
	Hostname     string            `json:"hostname"`
	IP           string            `json:"ip"`
	OS           string            `json:"os"`
	Arch         string            `json:"arch"`
	Labels       map[string]string `json:"labels"`
	Plugins      []string          `json:"plugins"`
	AgentVersion string            `json:"agent_version"`
	UptimeSec    int64             `json:"uptime_sec"`
}

type heartbeatPayload struct {
	ActiveSessions int     `json:"active_sessions"`
	ActiveAlerts   int     `json:"active_alerts"`
	CPUPct         float64 `json:"cpu_pct,omitempty"`
	MemPct         float64 `json:"mem_pct,omitempty"`
}

type alertEventsPayload struct {
	Events []alertEventItem `json:"events"`
}

type alertEventItem struct {
	EventTime         int64             `json:"event_time"`
	EventStatus       string            `json:"event_status"`
	AlertKey          string            `json:"alert_key"`
	Labels            map[string]string `json:"labels"`
	Attrs             map[string]string `json:"attrs,omitempty"`
	Description       string            `json:"description"`
	DescriptionFormat string            `json:"description_format,omitempty"`
}

// --- Server -> Agent payloads ---

type ackPayload struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
}

type disconnectPayload struct {
	Reason        string `json:"reason"`
	RetryAfterSec int    `json:"retry_after_sec"`
}
