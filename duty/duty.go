package duty

import (
	"net/http"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/types"
	"go.uber.org/zap"
)

type Duty struct {
	logger *zap.SugaredLogger
	queue  *safe.Queue[*types.Event]
	client *http.Client
}

func NewDuty(logger *zap.SugaredLogger) *Duty {
	client := &http.Client{
		Timeout: time.Duration(config.Config.Flashduty.Timeout),
	}

	return &Duty{
		logger: logger,
		queue:  safe.NewQueue[*types.Event](),
		client: client,
	}
}

func (d *Duty) Push(event *types.Event) {
	d.queue.PushFront(event)
}

func (d *Duty) Start() {
	go d.consume()
}

func (d *Duty) consume() {
	for {
		events := d.queue.PopBackAll()
		if len(events) == 0 {
			time.Sleep(time.Millisecond * 400)
			continue
		}

		for i := range events {
			d.push(events[i])
		}
	}
}

func (d *Duty) push(event *types.Event) {

}
