package config

import (
	"time"

	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/types"
)

type InternalConfig struct {
	// append labels to every event
	Labels map[string]string `toml:"labels"`

	// gather interval
	Interval Duration `toml:"interval"`

	// whether instance initial success
	inited bool `toml:"-"`
}

func (ic *InternalConfig) GetLabels() map[string]string {
	if ic.Labels != nil {
		return ic.Labels
	}

	return map[string]string{}
}

func (ic *InternalConfig) Initialized() bool {
	return ic.inited
}

func (ic *InternalConfig) SetInitialized() {
	ic.inited = true
}

func (ic *InternalConfig) GetInterval() Duration {
	return ic.Interval
}

func (ic *InternalConfig) InitInternalConfig() error {
	// maybe compile some glob/regex pattern here
	return nil
}

func (ic *InternalConfig) Process(q *safe.Queue[*types.Event]) *safe.Queue[*types.Event] {
	ret := safe.NewQueue[*types.Event]()

	if q.Len() == 0 {
		return ret
	}

	now := time.Now().Unix()
	old := q.PopBackAll()

	for i := range old {
		if old[i] == nil {
			continue
		}

		if old[i].EventTime == 0 {
			old[i].EventTime = now
		}

		for k, v := range Config.Global.Labels {
			old[i].Labels[k] = v
		}

		ret.PushFront(old[i])
	}

	return ret
}
