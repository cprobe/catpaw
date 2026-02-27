package engine

import (
	"sync"

	"github.com/cprobe/catpaw/types"
)

// 这里不需要定期清理，原因：
// 1. 内存里的事件数量不大
// 2. 有些事件就是很久才恢复，恢复的时候如果发现之前告警的事件已经被清理了，就无法生成恢复事件了
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
