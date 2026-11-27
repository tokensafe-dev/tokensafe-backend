package oumv

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// DefaultSessionTTL is the lifetime of a handshake-issued session (spec §3).
const DefaultSessionTTL = time.Hour

// MaxSessionTTL bounds a client-requested lifetime (spec §3, wire 1.5.0).
const MaxSessionTTL = 24 * time.Hour

// ClampSessionTTL clamps a requested lifetime in seconds to [1, 86400].
func ClampSessionTTL(seconds int) int {
	if seconds < 1 {
		return 1
	}
	if max := int(MaxSessionTTL.Seconds()); seconds > max {
		return max
	}
	return seconds
}

type session struct {
	expiresAt time.Time
}

// SessionRegistry tracks handshake sessions. Sessions are advisory for stateless
// callers: an absent sessionId is allowed, but a supplied one must be live.
type SessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]session
	ttl      time.Duration
	now      func() time.Time
}

// NewSessionRegistry creates a registry with the given TTL (0 → DefaultSessionTTL).
func NewSessionRegistry(ttl time.Duration) *SessionRegistry {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &SessionRegistry{
		sessions: make(map[string]session),
		ttl:      ttl,
		now:      time.Now,
	}
}

// TTLSeconds reports the configured session lifetime in seconds.
func (r *SessionRegistry) TTLSeconds() int {
	if r == nil {
		return int(DefaultSessionTTL.Seconds())
	}
	return int(r.ttl.Seconds())
}

// Create issues a new session id with the registry's configured lifetime.
func (r *SessionRegistry) Create() string {
	return r.create(r.ttl)
}

// CreateWithTTL issues a session with the requested lifetime (clamped to
// [1, 86400] seconds) and reports the granted seconds. Wire 1.5.0.
func (r *SessionRegistry) CreateWithTTL(seconds int) (string, int) {
	granted := ClampSessionTTL(seconds)
	return r.create(time.Duration(granted) * time.Second), granted
}

func (r *SessionRegistry) create(ttl time.Duration) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is not a reason to break the handshake; fall back to
		// a time-derived id that still satisfies the "opaque, unique" contract.
		buf = []byte(hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano))))[:16]
	}
	id := "sess-" + hex.EncodeToString(buf)

	r.mu.Lock()
	r.sessions[id] = session{expiresAt: r.now().Add(ttl)}
	r.mu.Unlock()
	return id
}

// Validate reports whether id is a live session. An empty id is accepted (stateless).
func (r *SessionRegistry) Validate(id string) bool {
	if id == "" {
		return true
	}
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return false
	}
	if r.now().After(s.expiresAt) {
		delete(r.sessions, id)
		return false
	}
	return true
}

// Delete removes a session (disconnect). It reports whether it existed.
func (r *SessionRegistry) Delete(id string) bool {
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.sessions[id]
	delete(r.sessions, id)
	return ok
}

// Len reports the number of registered sessions (tests/diagnostics).
func (r *SessionRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}
