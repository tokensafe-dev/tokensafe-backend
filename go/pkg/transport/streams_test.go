package transport_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

func TestSSEStreamsHeartbeatAndTraces(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{Name: "sse", Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	rt.RecordTrace("GET", "/api/orders", "/api/orders", "handler.List", 200, 1.5, "")

	server := httptest.NewServer(transport.NewSSEHandler(rt, transport.SSEOptions{
		Heartbeat: 20 * time.Millisecond,
		Poll:      10 * time.Millisecond,
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", got)
	}

	scanner := bufio.NewScanner(resp.Body)
	var sawHeartbeat, sawTrace bool
	for scanner.Scan() {
		line := scanner.Text()
		if line == "event: heartbeat" {
			sawHeartbeat = true
		}
		if line == "event: trace" {
			sawTrace = true
		}
		if sawHeartbeat && sawTrace {
			return
		}
	}
	t.Fatalf("expected heartbeat and trace events; heartbeat=%v trace=%v", sawHeartbeat, sawTrace)
}

func TestWebSocketDispatchesJSONRPC(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{Name: "ws", Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	server := httptest.NewServer(transport.NewWSHandler(transport.NewDispatcher(rt)))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":1,"method":"oumv/handshake","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	var resp protocol.OumvResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("invalid response: %s", payload)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}
