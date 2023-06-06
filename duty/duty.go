package duty

import (
	"net/http"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/types"
)

type Duty struct {
	queue  *safe.Queue[*types.Event]
	client *http.Client
}

var Flashduty *Duty

func Init() {
	client := &http.Client{
		Timeout: time.Duration(config.Config.Flashduty.Timeout),
	}

	Flashduty = &Duty{
		queue:  safe.NewQueue[*types.Event](),
		client: client,
	}

	go Flashduty.consume()
}

func (d *Duty) Push(event *types.Event) {
	d.queue.PushFront(event)
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
