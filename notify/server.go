package notify

import (
	"github.com/cprobe/catpaw/server"
	"github.com/cprobe/catpaw/types"
)

// ServerNotifier forwards alert events to catpaw-server via the WebSocket
// connection's ring buffer. Writing to the ring buffer is O(1) and never
// blocks, so it cannot affect other notifiers or the plugin engine.
type ServerNotifier struct{}

func NewServerNotifier() *ServerNotifier {
	return &ServerNotifier{}
}

func (n *ServerNotifier) Name() string {
	return "server"
}

func (n *ServerNotifier) Forward(event *types.Event) bool {
	server.SendAlertEvent(event)
	return true
}
