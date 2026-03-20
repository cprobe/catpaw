package server

import (
	"github.com/cprobe/catpaw/digcore/notify"
	"github.com/cprobe/catpaw/digcore/types"
)

// ServerNotifier forwards alert events to the WebSocket server's ring buffer.
// Writing to the ring buffer is O(1) and never blocks, so it cannot affect
// other notifiers or the plugin engine.
type ServerNotifier struct{}

// NewServerNotifier creates a new ServerNotifier instance.
func NewServerNotifier() notify.Notifier {
	return &ServerNotifier{}
}

func (n *ServerNotifier) Name() string {
	return "server"
}

func (n *ServerNotifier) Forward(event *types.Event) bool {
	SendAlertEvent(event)
	return true
}
