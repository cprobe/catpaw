package server

import (
	"context"
	"sync"
	"testing"

	"github.com/cprobe/digcore/logger"
	"go.uber.org/zap"
)

func init() {
	logger.Logger = zap.NewNop().Sugar()
}

func TestSessionManager_AddAndGet(t *testing.T) {
	m := newSessionManager()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &remoteSession{sessionID: "s1", sessionType: "inspect", cancel: cancel}
	if !m.add(s) {
		t.Fatal("first add should succeed")
	}
	if m.add(s) {
		t.Fatal("duplicate add should fail")
	}
	if got := m.get("s1"); got != s {
		t.Fatal("get should return the added session")
	}
	if got := m.get("nonexistent"); got != nil {
		t.Fatal("get for unknown ID should return nil")
	}
}

func TestSessionManager_Remove(t *testing.T) {
	m := newSessionManager()
	ctx, cancel := context.WithCancel(context.Background())

	s := &remoteSession{sessionID: "s1", sessionType: "chat", cancel: cancel}
	m.add(s)
	m.remove("s1")

	if m.get("s1") != nil {
		t.Fatal("session should be removed")
	}
	if ctx.Err() == nil {
		t.Fatal("remove should call cancel")
	}

	// remove non-existent should not panic
	m.remove("s1")
}

func TestSessionManager_Count(t *testing.T) {
	m := newSessionManager()
	if m.count() != 0 {
		t.Fatal("empty manager should have count 0")
	}

	for i := 0; i < 5; i++ {
		_, cancel := context.WithCancel(context.Background())
		s := &remoteSession{sessionID: string(rune('a' + i)), cancel: cancel}
		m.add(s)
	}
	if m.count() != 5 {
		t.Fatalf("expected 5, got %d", m.count())
	}
}

func TestSessionManager_CancelAll(t *testing.T) {
	m := newSessionManager()
	ctxs := make([]context.Context, 3)
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		ctxs[i] = ctx
		m.add(&remoteSession{
			sessionID:   string(rune('a' + i)),
			sessionType: "inspect",
			cancel:      cancel,
		})
	}

	m.cancelAll()

	if m.count() != 0 {
		t.Fatalf("expected 0 after cancelAll, got %d", m.count())
	}
	for i, ctx := range ctxs {
		if ctx.Err() == nil {
			t.Fatalf("session %d should be cancelled", i)
		}
	}
}

func TestSessionManager_CancelAll_Empty(t *testing.T) {
	m := newSessionManager()
	m.cancelAll() // should not panic
}

func TestSessionManager_ConcurrentAccess(t *testing.T) {
	m := newSessionManager()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, cancel := context.WithCancel(context.Background())
			sid := string(rune(id + 100))
			s := &remoteSession{sessionID: sid, cancel: cancel}
			m.add(s)
			m.get(sid)
			m.count()
			m.remove(sid)
		}(i)
	}
	wg.Wait()

	if m.count() != 0 {
		t.Fatalf("expected 0 after concurrent ops, got %d", m.count())
	}
}
