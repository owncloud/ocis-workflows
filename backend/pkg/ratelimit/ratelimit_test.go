package ratelimit

import (
	"testing"
	"time"
)

func TestAllowPermitsUpToMaxThenBlocks(t *testing.T) {
	l := New(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !l.Allow("token-a") {
			t.Fatalf("request %d: expected Allow to permit within the limit", i+1)
		}
	}
	if l.Allow("token-a") {
		t.Fatal("expected the 4th request within the window to be blocked")
	}
}

func TestAllowTracksKeysIndependently(t *testing.T) {
	l := New(1, time.Minute)

	if !l.Allow("token-a") {
		t.Fatal("expected first request for token-a to be allowed")
	}
	if l.Allow("token-a") {
		t.Fatal("expected second request for token-a to be blocked")
	}
	if !l.Allow("token-b") {
		t.Fatal("a different key must have its own independent budget")
	}
}

func TestAllowResetsAfterWindowElapses(t *testing.T) {
	l := New(1, 20*time.Millisecond)

	if !l.Allow("token-a") {
		t.Fatal("expected first request to be allowed")
	}
	if l.Allow("token-a") {
		t.Fatal("expected second immediate request to be blocked")
	}

	time.Sleep(30 * time.Millisecond)

	if !l.Allow("token-a") {
		t.Fatal("expected request to be allowed again once the window elapsed")
	}
}
