package server

import (
	"sync"

	"github.com/cprobe/catpaw/digcore/types"
)

// RingBuffer is a fixed-capacity circular buffer for alert events.
// When full, the oldest event is overwritten (best-effort semantics).
// It is safe for concurrent use.
type RingBuffer struct {
	mu    sync.Mutex
	buf   []*types.Event
	cap   int
	head  int // next write position
	count int // number of elements currently in the buffer
}

func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1000
	}
	return &RingBuffer{
		buf: make([]*types.Event, capacity),
		cap: capacity,
	}
}

// Push adds an event to the ring buffer.
// If the buffer is full, the oldest event is overwritten.
func (r *RingBuffer) Push(event *types.Event) {
	r.mu.Lock()
	r.buf[r.head] = event
	r.head = (r.head + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
	r.mu.Unlock()
}

// Drain removes and returns up to maxItems events from the buffer in FIFO order.
// Returns nil if the buffer is empty.
func (r *RingBuffer) Drain(maxItems int) []*types.Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.count == 0 {
		return nil
	}

	n := r.count
	if n > maxItems {
		n = maxItems
	}

	result := make([]*types.Event, n)
	tail := (r.head - r.count + r.cap) % r.cap
	for i := 0; i < n; i++ {
		idx := (tail + i) % r.cap
		result[i] = r.buf[idx]
		r.buf[idx] = nil // allow GC
	}
	r.count -= n
	return result
}

// Len returns the current number of events in the buffer.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}
