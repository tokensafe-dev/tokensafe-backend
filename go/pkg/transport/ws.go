package transport

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// NewWSHandler implements the optional WebSocket binding from spec §2.4:
// frames are JSON-RPC 2.0 objects, one message per frame; notifications (no id)
// get no response frame.
func NewWSHandler(dispatcher *Dispatcher) http.Handler {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// The transport sits behind the same auth as HTTP; browsers on the dev
		// loopback are the intended client, so origin checks are delegated to the
		// HTTP layer's auth token.
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
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
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				return
			}
		}
	})
}

var _ = protocol.MethodHandshake
