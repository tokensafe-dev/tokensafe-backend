package transport

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// EventStream is the minimal runtime surface the SSE handler needs: a bounded
// event log with monotonic ids, which is what `Last-Event-ID` replays from
// (spec §2.3). Trace and state events share one id space.
type EventStream interface {
	EventsSince(lastID uint64) []protocol.StreamEvent
}

// SSEOptions configures the event stream.
type SSEOptions struct {
	Heartbeat time.Duration // default 15s; tests use ~20ms
	Poll      time.Duration // default 300ms
}

// NewSSEHandler implements the optional SSE binding from spec §2.3:
//
//	GET /__oumv/events  →  text/event-stream
//	event: trace      (oumv/observe L4 entries)
//	event: state      (a resource changed through applyEdit)
//	event: heartbeat  (keeps proxies from closing the stream; no id)
//
// Every trace/state event carries `id: <monotonic sequence>`; reconnecting with
// `Last-Event-ID` replays everything after it. `?since=<RFC3339>` stays as a
// timestamp fallback for clients that predate the id cursor.
func NewSSEHandler(stream EventStream, opts ...SSEOptions) http.Handler {
	opt := SSEOptions{Heartbeat: 15 * time.Second, Poll: 300 * time.Millisecond}
	if len(opts) > 0 {
		if opts[0].Heartbeat > 0 {
			opt.Heartbeat = opts[0].Heartbeat
		}
		if opts[0].Poll > 0 {
			opt.Poll = opts[0].Poll
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "SSE requires GET", http.StatusMethodNotAllowed)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		lastID := uint64(0)
		if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
			if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil {
				lastID = parsed
			}
		} else if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
			if since, err := time.Parse(time.RFC3339Nano, raw); err == nil && stream != nil {
				for _, event := range stream.EventsSince(0) {
					if event.Timestamp.After(since) {
						lastID = event.ID - 1
						break
					}
				}
			}
		}

		writeEvents := func() {
			if stream == nil {
				return
			}
			for _, event := range stream.EventsSince(lastID) {
				w.Write([]byte("id: " + strconv.FormatUint(event.ID, 10) + "\n"))
				w.Write([]byte("event: " + event.Type + "\n"))
				w.Write([]byte("data: " + string(event.Data) + "\n\n"))
				lastID = event.ID
			}
			flusher.Flush()
		}

		w.Write([]byte(": connected\n\n"))
		flusher.Flush()
		writeEvents()

		heartbeat := time.NewTicker(opt.Heartbeat)
		poll := time.NewTicker(opt.Poll)
		defer heartbeat.Stop()
		defer poll.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-heartbeat.C:
				w.Write([]byte("event: heartbeat\ndata: " + strconv.Quote(time.Now().UTC().Format(time.RFC3339Nano)) + "\n\n"))
				flusher.Flush()
			case <-poll.C:
				writeEvents()
			}
		}
	})
}
