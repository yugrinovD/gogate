package breaker

import (
	"testing"
	"time"
)

func TestBreakerLifecycle(t *testing.T) {
	now := time.Unix(1000, 0)
	m := New(2, 10*time.Second)
	m.SetClock(func() time.Time { return now })

	if !m.Allow("p") {
		t.Fatal("closed breaker should allow")
	}
	if m.State("p") != StateClosed {
		t.Fatalf("state=%s", m.State("p"))
	}
	m.Failure("p")
	if !m.Allow("p") {
		t.Fatal("one failure should not open")
	}
	m.Failure("p")
	if m.State("p") != StateOpen {
		t.Fatalf("expected open, got %s", m.State("p"))
	}
	if m.Allow("p") {
		t.Fatal("open breaker should reject")
	}
	now = now.Add(11 * time.Second)
	if !m.Allow("p") {
		t.Fatal("half-open should allow one request")
	}
	if m.Allow("p") {
		t.Fatal("half-open should allow only one probe")
	}
	m.Success("p")
	if m.State("p") != StateClosed {
		t.Fatalf("expected closed after success, got %s", m.State("p"))
	}
}

func TestHalfOpenFailureReopens(t *testing.T) {
	now := time.Unix(2000, 0)
	m := New(1, time.Second)
	m.SetClock(func() time.Time { return now })
	m.Failure("p")
	if m.State("p") != StateOpen {
		t.Fatal("expected open")
	}
	now = now.Add(2 * time.Second)
	if !m.Allow("p") {
		t.Fatal("expected half-open probe")
	}
	m.Failure("p")
	if m.State("p") != StateOpen {
		t.Fatal("failed probe should reopen")
	}
	if m.Allow("p") {
		t.Fatal("reopened breaker should reject")
	}
}

func TestReleaseHalfOpenProbe(t *testing.T) {
	now := time.Unix(3000, 0)
	m := New(1, time.Second)
	m.SetClock(func() time.Time { return now })
	m.Failure("p")
	now = now.Add(2 * time.Second)
	if !m.Allow("p") {
		t.Fatal("expected probe")
	}
	m.Release("p")
	if !m.Allow("p") {
		t.Fatal("release should free the probe slot")
	}
}
