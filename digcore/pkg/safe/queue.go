package safe

import (
	"container/list"
	"sync"
)

// Queue is a thread-safe linkedlist
type Queue[T any] struct {
	sync.RWMutex
	linkedlist *list.List
}

func NewQueue[T any]() *Queue[T] {
	return &Queue[T]{linkedlist: list.New()}
}

func (q *Queue[T]) PushFront(v T) *list.Element {
	q.Lock()
	e := q.linkedlist.PushFront(v)
	q.Unlock()
	return e
}

func (q *Queue[T]) PushFrontN(vs []T) {
	q.Lock()
	for _, item := range vs {
		q.linkedlist.PushFront(item)
	}
	q.Unlock()
}

func (q *Queue[T]) PopBack() *T {
	q.Lock()
	defer q.Unlock()
	if elem := q.linkedlist.Back(); elem != nil {
		item := q.linkedlist.Remove(elem)
		v, ok := item.(T)
		if !ok {
			return nil
		}
		return &v
	}
	return nil
}

func (q *Queue[T]) PopBackN(n int) []T {
	q.Lock()
	defer q.Unlock()

	count := q.linkedlist.Len()
	if count == 0 {
		return nil
	}

	if count > n {
		count = n
	}

	items := make([]T, 0, count)
	for i := 0; i < count; i++ {
		data := q.linkedlist.Remove(q.linkedlist.Back())
		item, ok := data.(T)
		if ok {
			items = append(items, item)
		}
	}
	return items
}

func (q *Queue[T]) PopBackAll() []T {
	q.Lock()
	defer q.Unlock()
	count := q.linkedlist.Len()
	if count == 0 {
		return nil
	}

	items := make([]T, 0, count)
	for i := 0; i < count; i++ {
		data := q.linkedlist.Remove(q.linkedlist.Back())
		item, ok := data.(T)
		if ok {
			items = append(items, item)
		}
	}
	return items
}

func (q *Queue[T]) RemoveAll() {
	q.Lock()
	q.linkedlist.Init()
	q.Unlock()
}

func (q *Queue[T]) Len() int {
	q.RLock()
	size := q.linkedlist.Len()
	q.RUnlock()
	return size
}

// QueueLimited is Queue with Limited Size
type QueueLimited[T any] struct {
	maxSize int
	queue   *Queue[T]
}

func NewQueueLimited[T any](maxSize int) *QueueLimited[T] {
	return &QueueLimited[T]{queue: NewQueue[T](), maxSize: maxSize}
}

func (ql *QueueLimited[T]) PushFront(v T) bool {
	if ql.queue.Len() >= ql.maxSize {
		return false
	}

	ql.queue.PushFront(v)
	return true
}

func (ql *QueueLimited[T]) PushFrontN(vs []T) bool {
	if ql.queue.Len() >= ql.maxSize {
		return false
	}

	ql.queue.PushFrontN(vs)
	return true
}

func (ql *QueueLimited[T]) PopBack() *T {
	return ql.queue.PopBack()
}

func (ql *QueueLimited[T]) PopBackN(n int) []T {
	return ql.queue.PopBackN(n)
}

func (ql *QueueLimited[T]) PopBackAll() []T {
	return ql.queue.PopBackAll()
}

func (ql *QueueLimited[T]) RemoveAll() {
	ql.queue.RemoveAll()
}

func (ql *QueueLimited[T]) Len() int {
	return ql.queue.Len()
}
