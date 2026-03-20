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

type CommentNotifier interface {
	Comment(alertKey, comment string) bool
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

func ForwardComment(alertKey, comment string) bool {
	if len(notifiers) == 0 {
		logger.Logger.Warnw("forward comment: no notifiers configured, comment dropped",
			"alert_key", alertKey)
		return false
	}

	commentNotifiers := make([]CommentNotifier, 0, len(notifiers))
	for _, notifier := range notifiers {
		cn, ok := notifier.(CommentNotifier)
		if !ok {
			continue
		}
		commentNotifiers = append(commentNotifiers, cn)
	}

	if len(commentNotifiers) == 0 {
		logger.Logger.Warnw("forward comment: no notifier supports comment delivery",
			"alert_key", alertKey)
		return false
	}

	if len(commentNotifiers) == 1 {
		return commentNotifiers[0].Comment(alertKey, comment)
	}

	var wg sync.WaitGroup
	results := make([]bool, len(commentNotifiers))
	for i, notifier := range commentNotifiers {
		wg.Add(1)
		go func(idx int, commenter CommentNotifier) {
			defer wg.Done()
			results[idx] = commenter.Comment(alertKey, comment)
		}(i, notifier)
	}
	wg.Wait()

	for _, ok := range results {
		if ok {
			return true
		}
	}
	return false
}
