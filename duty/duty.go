package duty

import (
	"net/http"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/types"
	"go.uber.org/zap"
)

type DutyClient struct {
	logger *zap.SugaredLogger
	queue  *safe.Queue[*types.Event]
	client *http.Client
}

func NewDutyClient(logger *zap.SugaredLogger) *DutyClient {
	client := &http.Client{
		Timeout: time.Duration(config.Config.Flashduty.Timeout),
	}

	return &DutyClient{
		logger: logger,
		queue:  safe.NewQueue[*types.Event](),
		client: client,
	}
}

func (d *DutyClient) Push(event *types.Event) {
	d.queue.PushFront(event)
}

func (d *DutyClient) Start() {
	go d.consume()
}

func (d *DutyClient) consume() {
	for {
		events := d.queue.PopBackN(1)
		if len(events) == 0 {
			time.Sleep(time.Millisecond * 400)
			continue
		}

		for i := range events {
			d.push(events[i])
		}
	}
}

func (d *DutyClient) push(event *types.Event) {

}
