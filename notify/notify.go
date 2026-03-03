package notify

import (
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/types"
)

type Notifier interface {
	Name() string
	Forward(event *types.Event) bool
}

var notifiers []Notifier

func Register(n Notifier) {
	notifiers = append(notifiers, n)
	logger.Logger.Infow("notifier registered", "name", n.Name())
}

func Forward(event *types.Event) bool {
	if len(notifiers) == 0 {
		logger.Logger.Warnw("forward: no notifiers configured, event dropped",
			"event_key", event.AlertKey)
		return false
	}

	anyOk := false
	for _, n := range notifiers {
		if n.Forward(event) {
			anyOk = true
		}
	}
	return anyOk
}
