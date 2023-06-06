package engine

import (
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/types"
)

func PushRawEvents(queue *safe.Queue[*types.Event]) {

}

// func (ic *InternalConfig) Process(q *safe.Queue[*types.Event]) *safe.Queue[*types.Event] {
// 	ret := safe.NewQueue[*types.Event]()

// 	if q.Len() == 0 {
// 		return ret
// 	}

// 	now := time.Now().Unix()
// 	old := q.PopBackAll()

// 	for i := range old {
// 		if old[i] == nil {
// 			continue
// 		}

// 		if old[i].EventTime == 0 {
// 			old[i].EventTime = now
// 		}

// 		for k, v := range Config.Global.Labels {
// 			old[i].Labels[k] = v
// 		}

// 		ret.PushFront(old[i])
// 	}

// 	return ret
// }
