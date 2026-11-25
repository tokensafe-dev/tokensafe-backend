package oumv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

// Language-neutral OUMV Core conformance cases (spec §10).
// Canonical source: agent-oumv/fixtures/conformance-cases.json — keep in sync.
type conformanceCase struct {
	ID                          string                 `json:"id"`
	Method                      string                 `json:"method"`
	Params                      map[string]interface{} `json:"params"`
	ExpectResultFields          []string               `json:"expectResultFields"`
	ExpectError                 *struct {
		Code int `json:"code"`
	} `json:"expectError"`
	ExpectVersionMatchesContent string `json:"expectVersionMatchesContent"`
}

type conformanceSpec struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Cases           []conformanceCase  `json:"cases"`
}

func substituteMarkers(params map[string]interface{}, fixtureFile, missingFile, editTarget string) map[string]interface{} {
	editContent := "package conformance\n\nfunc Value() int {\n\treturn 42\n}\n"
	out := make(map[string]interface{}, len(params))
	for k, v := range params {
		if s, ok := v.(string); ok {
			s = strings.ReplaceAll(s, "${FIXTURE_FILE}", fixtureFile)
			s = strings.ReplaceAll(s, "${MISSING_FILE}", missingFile)
			s = strings.ReplaceAll(s, "${EDIT_TARGET_FILE}", editTarget)
			s = strings.ReplaceAll(s, "${EDIT_CONTENT}", editContent)
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}

func jsonPathExists(data interface{}, path string) bool {
	parts := strings.Split(path, ".")
	var current interface{} = data
	for _, p := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return false
		}
		val, exists := m[p]
		if !exists {
			return false
		}
		current = val
	}
	return true
}

// TestConformanceCases runs the language-neutral OUMV core conformance suite
// through the JSON-RPC dispatcher: handshake shape, error-code model
// (OCC conflict, path traversal, syntax guard, missing resource), and
// real OCC version tokens on applied edits.
func TestConformanceCases(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "conformance-cases.json"))
	if err != nil {
		t.Fatalf("failed to read conformance fixtures: %v", err)
	}
	var spec conformanceSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("failed to parse conformance fixtures: %v", err)
	}
	if len(spec.Cases) == 0 {
		t.Fatal("conformance fixtures contain no cases")
	}

	// Workspace setup: one existing valid file, one edit target, one missing name
	tmpDir := t.TempDir()
	fixtureFile := "conformance_fixture.py"
	editTarget := "conformance_edit_target.py"
	missingFile := "conformance_missing_file.py"
	if err := os.WriteFile(filepath.Join(tmpDir, fixtureFile), []byte("value = 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, editTarget), []byte("value = 41\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rt := newTestRuntime(t, http.NotFoundHandler())
	rt.workspaceDir = tmpDir
	dispatcher := transport.NewDispatcher(rt)

	for _, tc := range spec.Cases {
		tc := tc
		t.Run(tc.ID, func(t *testing.T) {
			params := substituteMarkers(tc.Params, fixtureFile, missingFile, editTarget)
			paramsJSON, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("failed to marshal params: %v", err)
			}
			resp := dispatcher.Dispatch(context.Background(), &protocol.OumvRequest{
				JSONRPC: "2.0",
				ID:      tc.ID,
				Method:  tc.Method,
				Params:  paramsJSON,
			})

			if tc.ExpectError != nil {
				if resp.Error == nil {
					t.Fatalf("expected error code %d, got result: %+v", tc.ExpectError.Code, resp.Result)
				}
				if resp.Error.Code != tc.ExpectError.Code {
					t.Fatalf("expected error code %d, got %d (%s)", tc.ExpectError.Code, resp.Error.Code, resp.Error.Message)
				}
				return
			}

			if resp.Error != nil {
				t.Fatalf("expected success, got error %d: %s", resp.Error.Code, resp.Error.Message)
			}
			// Round-trip the typed result through JSON so field checks use wire keys
			rawResult, err := json.Marshal(resp.Result)
			if err != nil {
				t.Fatalf("failed to marshal result: %v", err)
			}
			var resMap map[string]interface{}
			if err := json.Unmarshal(rawResult, &resMap); err != nil {
				t.Fatalf("failed to unmarshal result into map: %v", err)
			}
			for _, field := range tc.ExpectResultFields {
				if !jsonPathExists(resMap, field) {
					t.Fatalf("result missing expected field %q: %+v", field, resMap)
				}
			}
			if tc.ExpectVersionMatchesContent != "" {
				content := strings.ReplaceAll(tc.ExpectVersionMatchesContent, "${EDIT_CONTENT}", "package conformance\n\nfunc Value() int {\n\treturn 42\n}\n")
				version, _ := resMap["newVersion"].(string)
				if !strings.HasPrefix(version, "sha256:") {
					t.Fatalf("expected sha256 version token, got %q", version)
				}
				sum := sha256.Sum256([]byte(content))
				want := "sha256:" + hex.EncodeToString(sum[:])
				if version != want {
					t.Fatalf("version token must match written content: expected %s, got %s", want, version)
				}
			}
		})
	}
}
