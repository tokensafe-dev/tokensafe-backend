package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// TraceStream is the minimal runtime surface an SSE handler needs.
type TraceStream interface {
	RecentTracesSince(since time.Time) []protocol.TraceEntry
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
//	event: heartbeat  (keeps proxies from closing the stream)
//
// `Last-Event-ID` (or `?since=` RFC3339) replays traces newer than that timestamp.
func NewSSEHandler(stream TraceStream, opts ...SSEOptions) http.Handler {
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

		since := time.Time{}
		if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
				since = parsed
			}
		} else if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
				since = parsed
			}
		}

		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		heartbeat := time.NewTicker(opt.Heartbeat)
		poll := time.NewTicker(opt.Poll)
		defer heartbeat.Stop()
		defer poll.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-heartbeat.C:
				fmt.Fprintf(w, "event: heartbeat\ndata: %q\n\n", time.Now().UTC().Format(time.RFC3339Nano))
				flusher.Flush()
			case <-poll.C:
				if stream == nil {
					continue
				}
				for _, trace := range stream.RecentTracesSince(since) {
					raw, err := json.Marshal(trace)
					if err != nil {
						continue
					}
					fmt.Fprintf(w, "id: %s\nevent: trace\ndata: %s\n\n", trace.Timestamp, raw)
					if parsed, err := time.Parse(time.RFC3339Nano, trace.Timestamp); err == nil {
						since = parsed
					}
				}
				flusher.Flush()
			}
		}
	})
}
