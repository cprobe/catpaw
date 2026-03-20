package notify

import (
	"sync"

	"github.com/cprobe/catpaw/digcore/logger"
	"github.com/cprobe/catpaw/digcore/types"
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

	if len(notifiers) == 1 {
		return notifiers[0].Forward(event)
	}

	var wg sync.WaitGroup
	results := make([]bool, len(notifiers))
	for i, n := range notifiers {
		wg.Add(1)
		go func(idx int, notifier Notifier) {
			defer wg.Done()
			results[idx] = notifier.Forward(event)
		}(i, n)
	}
	wg.Wait()

	for _, ok := range results {
		if ok {
			return true
		}
	}
	return false
}
