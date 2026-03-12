package server

import (
	"errors"
	"testing"
	"time"
)

func TestNextReconnectState_ResetsBackoffAfterConnectedSession(t *testing.T) {
	normalMin := time.Second
	normalMax := 300 * time.Second
	authMin := 60 * time.Second
	authMax := 1800 * time.Second

	wait, nextBackoff, logFn := nextReconnectState(
		&reconnectError{err: errConnectionLost, resetBackoff: true},
		128*time.Second,
		normalMin,
		normalMax,
		authMin,
		authMax,
	)

	if wait != normalMin {
		t.Fatalf("wait = %v, want %v", wait, normalMin)
	}
	if nextBackoff != 2*normalMin {
		t.Fatalf("nextBackoff = %v, want %v", nextBackoff, 2*normalMin)
	}
	if logFn == nil {
		t.Fatal("expected warning logger for connected-session disconnect")
	}
}

func TestNextReconnectState_PreservesBackoffForDialFailures(t *testing.T) {
	normalMin := time.Second
	normalMax := 300 * time.Second
	authMin := 60 * time.Second
	authMax := 1800 * time.Second

	wait, nextBackoff, _ := nextReconnectState(
		errors.New("ws dial: connection refused"),
		128*time.Second,
		normalMin,
		normalMax,
		authMin,
		authMax,
	)

	if wait != 128*time.Second {
		t.Fatalf("wait = %v, want %v", wait, 128*time.Second)
	}
	if nextBackoff != 256*time.Second {
		t.Fatalf("nextBackoff = %v, want %v", nextBackoff, 256*time.Second)
	}
}

func TestNextReconnectState_ServerRequestedDisconnectResetsBackoff(t *testing.T) {
	normalMin := time.Second
	normalMax := 300 * time.Second
	authMin := 60 * time.Second
	authMax := 1800 * time.Second

	wait, nextBackoff, logFn := nextReconnectState(
		&disconnectError{retryAfterSec: 0},
		128*time.Second,
		normalMin,
		normalMax,
		authMin,
		authMax,
	)

	if wait != normalMin {
		t.Fatalf("wait = %v, want %v", wait, normalMin)
	}
	if nextBackoff != normalMin {
		t.Fatalf("nextBackoff = %v, want %v", nextBackoff, normalMin)
	}
	if logFn == nil {
		t.Fatal("expected info logger for server-requested disconnect")
	}
}