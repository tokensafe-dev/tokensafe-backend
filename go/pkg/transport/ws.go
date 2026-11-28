package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// EventSubscriber is the runtime surface the WS handler needs to push
// server-initiated notifications (spec §2.4).
type EventSubscriber interface {
	SubscribeEvents() (<-chan protocol.StreamEvent, func())
}

// notificationMethod maps an event type to its JSON-RPC notification method.
var notificationMethod = map[string]string{
	"trace": "notifications/oumv.trace",
	"state": "notifications/oumv.state",
}

// WSOptions configures the optional WebSocket binding.
type WSOptions struct {
	// Auth, when enabled, refuses the upgrade with 401 before a socket exists
	// (§12: an authenticated binding protects WS too).
	Auth *Authorizer
}

// NewWSHandler implements the optional WebSocket binding from spec §2.4:
// frames are JSON-RPC 2.0 objects, one message per frame; notifications (no id)
// get no response frame. After `oumv/handshake` the connection is bound to that
// sessionId and receives `notifications/oumv.trace` / `notifications/oumv.state`;
// closing the socket ends the session.
func NewWSHandler(dispatcher *Dispatcher, stream EventSubscriber, opts ...WSOptions) http.Handler {
	var opt WSOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// The transport sits behind the same auth as HTTP; browsers on the dev
		// loopback are the intended client, so origin checks are delegated to the
		// HTTP layer's auth token.
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opt.Auth != nil && !opt.Auth.Authorized(r) {
			opt.Auth.Refuse(w)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		var writeMu sync.Mutex
		write := func(payload []byte) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return conn.WriteMessage(websocket.TextMessage, payload)
		}

		// sessionId is written by the read loop after handshake and read by the
		// notifier goroutine, so it needs its own lock.
		var sessionMu sync.RWMutex
		sessionID := ""

		if stream != nil {
			events, unsubscribe := stream.SubscribeEvents()
			defer unsubscribe()
			go func() {
				for event := range events {
					method, ok := notificationMethod[event.Type]
					if !ok {
						continue
					}
					sessionMu.RLock()
					bound := sessionID
					sessionMu.RUnlock()
					if bound == "" {
						// Not a session yet: notifications start after handshake.
						continue
					}
					params := map[string]interface{}{}
					_ = json.Unmarshal(event.Data, &params)
					params["sessionId"] = bound
					frame, err := json.Marshal(map[string]interface{}{
						"jsonrpc": "2.0",
						"method":  method,
						"params":  params,
					})
					if err != nil {
						continue
					}
					if write(frame) != nil {
						return
					}
				}
			}()
		}

		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				break
			}

			var probe struct {
				ID interface{} `json:"id"`
			}
			_ = json.Unmarshal(payload, &probe)

			resp := dispatcher.Handle(context.Background(), payload)
			if probe.ID == nil && resp.Error == nil {
				continue // notification
			}
			raw, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			if write(raw) != nil {
				break
			}
			if resp.Result != nil {
				var handshake struct {
					SessionID string `json:"sessionId"`
				}
				if probeRaw, err := json.Marshal(resp.Result); err == nil && json.Unmarshal(probeRaw, &handshake) == nil && handshake.SessionID != "" {
					sessionMu.Lock()
					sessionID = handshake.SessionID
					sessionMu.Unlock()
				}
			}
		}

		sessionMu.RLock()
		bound := sessionID
		sessionMu.RUnlock()
		dispatcher.EndSession(bound)
	})
}
