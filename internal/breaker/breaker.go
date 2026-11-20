// Package breaker implements a small per-provider circuit breaker.
package breaker

import (
	"sync"
	"time"
)

// State is the breaker state.
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

type entry struct {
	failures     int
	state        State
	openedAt     time.Time
	halfInFlight bool
}

// Manager tracks breakers by provider name.
type Manager struct {
	mu        sync.Mutex
	entries   map[string]*entry
	threshold int
	cooldown  time.Duration
	now       func() time.Time
}

// New creates a breaker manager.
func New(threshold int, cooldown time.Duration) *Manager {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &Manager{entries: make(map[string]*entry), threshold: threshold, cooldown: cooldown, now: time.Now}
}

// Allow reports whether a request may be sent to a provider.
func (m *Manager) Allow(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.get(name)
	switch e.state {
	case StateOpen:
		if m.now().Sub(e.openedAt) >= m.cooldown {
			e.state = StateHalfOpen
			e.halfInFlight = true
			return true
		}
		return false
	case StateHalfOpen:
		if e.halfInFlight {
			return false
		}
		e.halfInFlight = true
		return true
	default:
		return true
	}
}

// Success closes the breaker.
func (m *Manager) Success(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.get(name)
	e.failures = 0
	e.state = StateClosed
	e.halfInFlight = false
}

// Failure records a pre-first-byte failure.
func (m *Manager) Failure(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.get(name)
	if e.state == StateHalfOpen {
		e.state = StateOpen
		e.openedAt = m.now()
		e.halfInFlight = false
		return
	}
	e.failures++
	if e.failures >= m.threshold {
		e.state = StateOpen
		e.openedAt = m.now()
	}
}

// State returns the current state.
func (m *Manager) State(name string) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.get(name)
	if e.state == "" {
		return StateClosed
	}
	return e.state
}

// Snapshot returns states for all known providers.
func (m *Manager) Snapshot() map[string]State {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]State, len(m.entries))
	for name, e := range m.entries {
		if e.state == "" {
			out[name] = StateClosed
		} else {
			out[name] = e.state
		}
	}
	return out
}

// SetClock is used by tests.
func (m *Manager) SetClock(fn func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn != nil {
		m.now = fn
	}
}

func (m *Manager) get(name string) *entry {
	e, ok := m.entries[name]
	if !ok {
		e = &entry{state: StateClosed}
		m.entries[name] = e
	}
	return e
}
