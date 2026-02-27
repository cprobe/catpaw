package engine

import (
	"sync"

	"github.com/cprobe/catpaw/types"
)

type EventCache struct {
	sync.RWMutex
	records map[string]*types.Event
}

var Events = &EventCache{records: make(map[string]*types.Event)}

func (c *EventCache) Get(key string) *types.Event {
	c.RLock()
	defer c.RUnlock()
	return c.records[key]
}

func (c *EventCache) Set(val *types.Event) {
	c.Lock()
	defer c.Unlock()
	c.records[val.AlertKey] = val
}

func (c *EventCache) Del(key string) {
	c.Lock()
	defer c.Unlock()
	delete(c.records, key)
}
