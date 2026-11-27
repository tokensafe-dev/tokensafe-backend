package protocol

import (
	"encoding/json"
	"time"
)

// StreamEvent is one server-pushed event on a streaming transport (spec §2.3/§2.4).
// IDs are monotonic per process, so `Last-Event-ID: N` replays everything after N
// regardless of how the payload is shaped.
type StreamEvent struct {
	ID        uint64          `json:"id"`
	Type      string          `json:"type"` // trace | state | diagnostic
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}
