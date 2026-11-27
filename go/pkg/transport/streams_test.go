package transport_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	server := httptest.NewServer(transport.NewWSHandler(transport.NewDispatcher(rt), rt))
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

func TestSSEStateEventReplaysFromLastEventID(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{Name: "sse-state", Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	rt.RecordState("svc.py", "sha256:"+strings.Repeat("a", 64), "patch")

	server := httptest.NewServer(transport.NewSSEHandler(rt, transport.SSEOptions{
		Heartbeat: time.Hour, // no heartbeat noise: only real events in the assertions
		Poll:      5 * time.Millisecond,
	}))
	defer server.Close()

	readState := func(lastEventID string, timeout time.Duration) (id, data string, ok bool) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatalf("build sse request: %v", err)
		}
		if lastEventID != "" {
			req.Header.Set("Last-Event-ID", lastEventID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("sse request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		event := ""
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
				if event == "state" {
					return id, data, true
				}
			}
		}
		return id, data, false
	}

	id, data, ok := readState("", 2*time.Second)
	if !ok || id != "1" || !strings.Contains(data, `"path":"svc.py"`) || !strings.Contains(data, `"operation":"patch"`) {
		t.Fatalf("first stream: id=%q data=%q ok=%v", id, data, ok)
	}

	if replayID, replayData, replayed := readState("0", 2*time.Second); !replayed || replayID != id || replayData != data {
		t.Fatalf("Last-Event-ID: 0 must replay the missed event, got id=%q data=%q ok=%v", replayID, replayData, replayed)
	}

	if _, _, extra := readState("1", 250*time.Millisecond); extra {
		t.Fatalf("Last-Event-ID: 1 must not replay an already delivered event")
	}
}

func TestWebSocketPushesStateNotification(t *testing.T) {
	workspace := t.TempDir()
	content := "package sample\n\nfunc Value() int {\n\treturn 1\n}\n"
	if err := os.WriteFile(filepath.Join(workspace, "target.go"), []byte(content), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	digest := sha256.Sum256([]byte(content))
	version := "sha256:" + hex.EncodeToString(digest[:])

	runtimeCfg := oumv.DefaultDevelopmentRuntimeConfig()
	runtimeCfg.WorkspaceDir = workspace
	rt := oumv.NewRuntime(oumv.Config{Name: "ws-state", WorkspaceDir: workspace, Runtime: runtimeCfg})
	server := httptest.NewServer(transport.NewWSHandler(transport.NewDispatcher(rt), rt))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	write := func(payload map[string]interface{}) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal frame: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}

	write(map[string]interface{}{"jsonrpc": "2.0", "id": "hs", "method": "oumv/handshake", "params": map[string]interface{}{}})

	sessionID := ""
	for sessionID == "" {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("handshake read: %v", err)
		}
		var response struct {
			ID     interface{} `json:"id"`
			Result struct {
				SessionID string `json:"sessionId"`
			} `json:"result"`
		}
		_ = json.Unmarshal(payload, &response)
		if response.ID != nil {
			sessionID = response.Result.SessionID
		}
	}
	if sessionID == "" {
		t.Fatal("handshake returned no sessionId")
	}

	write(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "edit",
		"method":  "oumv/applyEdit",
		"params": map[string]interface{}{
			"sessionId":       sessionID,
			"path":            "target.go",
			"mode":            "patch",
			"expectedVersion": version,
			"hunks":           []map[string]string{{"target": "return 1", "replacement": "return 2"}},
		},
	})

	edited, notified := false, false
	for !edited || !notified {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read (response=%v notification=%v): %v", edited, notified, err)
		}
		var frame struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Path      string `json:"path"`
				Operation string `json:"operation"`
				Version   string `json:"version"`
			} `json:"params"`
			Result struct {
				Status string `json:"status"`
			} `json:"result"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatalf("invalid frame %s: %v", payload, err)
		}
		if frame.ID != nil && frame.Result.Status == "APPLIED" {
			edited = true
		}
		if frame.Method == "notifications/oumv.state" {
			if frame.Params.SessionID != sessionID {
				t.Fatalf("notification must be bound to session %q, got %q", sessionID, frame.Params.SessionID)
			}
			if frame.Params.Path != "target.go" || frame.Params.Operation != "patch" || !strings.HasPrefix(frame.Params.Version, "sha256:") {
				t.Fatalf("unexpected notification params: %s", payload)
			}
			notified = true
		}
	}
}
