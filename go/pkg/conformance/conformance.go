// Package conformance implements the language-neutral OUMV conformance runner.
//
// It executes the canonical fixtures from agent-oumv/fixtures/conformance-cases.json
// through a JSON-RPC dispatcher (HTTP, in-memory, or stdio) and derives the
// conformance level an adapter actually attains (spec §10). Reports are the
// machine-readable certification artifact: a level claim without a report is not
// a certification.
package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// SpecVersion is the protocol version this runner certifies against.
const SpecVersion = "1.4.0"

// Case is one language-neutral conformance case.
type Case struct {
	ID                 string                 `json:"id"`
	Level              int                    `json:"level"`
	Method             string                 `json:"method"`
	Params             map[string]interface{} `json:"params"`
	Preconditions      []string               `json:"preconditions"`
	ExpectResultFields []string               `json:"expectResultFields"`
	ExpectValues       map[string]interface{} `json:"expectValues"`
	ExpectError        *struct {
		Code int `json:"code"`
	} `json:"expectError"`
	ExpectLevelEquals           string `json:"expectLevelEquals"`
	ExpectSha256Field           string `json:"expectSha256Field"`
	ExpectArrayField            string `json:"expectArrayField"`
	ExpectEvidence              bool   `json:"expectEvidence"`
	ExpectVersionMatchesContent string `json:"expectVersionMatchesContent"`
	// RequiresCapability gates a capabilityCase on the handshake claim.
	RequiresCapability string `json:"requiresCapability,omitempty"`
}

// Spec is the fixture file: cases plus the version metadata they were written against.
type Spec struct {
	ProtocolVersion  string            `json:"protocolVersion"`
	FixtureVersion   int               `json:"fixtureVersion"`
	LevelDefinitions map[string]string `json:"levelDefinitions"`
	Cases            []Case            `json:"cases"`
	// CapabilityCases run only when the runtime advertises RequiresCapability.
	CapabilityCases []Case `json:"capabilityCases"`
}

// Markers carries the concrete values substituted for ${MARKER} placeholders.
// Every runner supplies its own language-appropriate content.
type Markers struct {
	FixtureFile   string
	MissingFile   string
	EditTarget    string
	NewFile       string
	EditVersion   string
	EditContent   string
	BrokenContent string
	L2Target      string
	L3Target      string
	VerifyTarget  string

	SymbolFile        string
	SymbolName        string
	SymbolReplacement string
	SymbolVersion     string
	SymbolFileContent string
	DeleteFile        string
	DeleteVersion     string
	RenameSource      string
	RenameTarget      string
	RenameVersion     string

	// Wire 1.3.0: the version of EDIT_CONTENT once written, and the text a
	// mode=patch hunk splices over it (the whole content, replaced).
	EditContentVersion string
	PatchReplacement   string
	// Wire 1.4.0: the target a sandbox capability case dispatches against.
	SandboxTarget string
}

// CaseResult is the outcome of one case.
type CaseResult struct {
	ID     string `json:"id"`
	Level  int    `json:"level"`
	Status string `json:"status"` // "passed" | "failed"
	Error  string `json:"error,omitempty"`
}

// Report is the certification artifact for one adapter.
type Report struct {
	AdapterName    string            `json:"adapterName"`
	Framework      string            `json:"framework,omitempty"`
	SpecVersion    string            `json:"specVersion"`
	FixtureVersion int               `json:"fixtureVersion"`
	Total          int               `json:"total"`
	Passed         int               `json:"passed"`
	Failed         int               `json:"failed"`
	AttainedLevel  int               `json:"attainedLevel"`
	Levels         map[string]string `json:"levels"`
	Cases          []CaseResult      `json:"cases"`
	GeneratedAt    string            `json:"generatedAt"`
}

// Dispatcher is the minimal transport surface the runner needs.
type Dispatcher interface {
	Dispatch(ctx context.Context, req *protocol.OumvRequest) *protocol.OumvResponse
}

// LoadCases reads a fixture file.
func LoadCases(path string) (Spec, error) {
	var spec Spec
	raw, err := os.ReadFile(path)
	if err != nil {
		return spec, fmt.Errorf("read conformance fixtures: %w", err)
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, fmt.Errorf("parse conformance fixtures: %w", err)
	}
	if len(spec.Cases) == 0 {
		return spec, fmt.Errorf("fixture %s contains no cases", path)
	}
	return spec, nil
}

// Run executes every case against the dispatcher and derives the attained level.
func Run(dispatcher Dispatcher, spec Spec, markers Markers) Report {
	report := Report{
		SpecVersion:    spec.ProtocolVersion,
		FixtureVersion: spec.FixtureVersion,
		Total:          len(spec.Cases),
		Cases:          make([]CaseResult, 0, len(spec.Cases)),
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	for _, tc := range spec.Cases {
		result := CaseResult{ID: tc.ID, Level: tc.Level, Status: "passed"}

		// Preconditions establish the runtime evidence a case asserts on.
		for _, pre := range tc.Preconditions {
			if pre != "verify" {
				result.Status, result.Error = "failed", fmt.Sprintf("unknown precondition %q", pre)
				break
			}
			resp := dispatch(dispatcher, tc.ID+"-precondition", "oumv/verify", map[string]interface{}{
				"target": markers.VerifyTarget,
				"action": "conformance-probe",
			})
			if resp.Error != nil {
				result.Status = "failed"
				result.Error = fmt.Sprintf("verify precondition failed: %d %s", resp.Error.Code, resp.Error.Message)
				break
			}
		}
		if result.Status == "failed" {
			report.Cases = append(report.Cases, result)
			continue
		}

		resp := dispatch(dispatcher, tc.ID, tc.Method, markers.substitute(tc.Params))
		if errMsg := check(tc, resp, markers); errMsg != "" {
			result.Status, result.Error = "failed", errMsg
		}
		report.Cases = append(report.Cases, result)
	}

	// Capability cases prove an advertised claim; a runtime that does not make the
	// claim is not penalised, the case simply is not part of its report.
	if len(spec.CapabilityCases) > 0 {
		capabilities := advertisedCapabilities(dispatcher)
		for _, tc := range spec.CapabilityCases {
			if !capabilityEnabled(capabilities, tc.RequiresCapability) {
				continue
			}
			result := CaseResult{ID: tc.ID, Level: tc.Level, Status: "passed"}
			resp := dispatch(dispatcher, tc.ID, tc.Method, markers.substitute(tc.Params))
			if errMsg := check(tc, resp, markers); errMsg != "" {
				result.Status, result.Error = "failed", errMsg
			}
			report.Cases = append(report.Cases, result)
		}
	}

	report.Total = len(report.Cases)
	for _, c := range report.Cases {
		if c.Status == "passed" {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	report.AttainedLevel = attainedLevel(report.Cases)
	report.Levels = summarizeLevels(report.Cases)
	return report
}

// advertisedCapabilities reads the handshake capability object as a map so case
// gating does not depend on the concrete result type of any runtime.
func advertisedCapabilities(d Dispatcher) map[string]interface{} {
	resp := dispatch(d, "capability-handshake", "oumv/handshake", map[string]interface{}{})
	if resp == nil || resp.Error != nil || resp.Result == nil {
		return nil
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return nil
	}
	var parsed struct {
		Capabilities map[string]interface{} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return parsed.Capabilities
}

// capabilityEnabled walks a dotted capability path and requires literal true.
func capabilityEnabled(capabilities map[string]interface{}, path string) bool {
	if capabilities == nil || path == "" {
		return false
	}
	var current interface{} = capabilities
	for _, segment := range strings.Split(path, ".") {
		record, ok := current.(map[string]interface{})
		if !ok {
			return false
		}
		current = record[segment]
	}
	enabled, ok := current.(bool)
	return ok && enabled
}

// Save writes the report as JSON (creating parent directories as needed).
func (r Report) Save(path string) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0644)
}

// FailedCaseIDs lists failing cases at or below the given level.
func (r Report) FailedCaseIDs(upTo int) []string {
	var ids []string
	for _, c := range r.Cases {
		if c.Status != "passed" && c.Level <= upTo {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func dispatch(d Dispatcher, id, method string, params map[string]interface{}) *protocol.OumvResponse {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return &protocol.OumvResponse{Error: &protocol.OumvError{Code: protocol.ErrInternalError, Message: err.Error()}}
	}
	return d.Dispatch(context.Background(), &protocol.OumvRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  paramsJSON,
	})
}

// check evaluates every expectation a case declares. It returns "" on success.
func check(tc Case, resp *protocol.OumvResponse, markers Markers) string {
	if tc.ExpectError != nil {
		if resp.Error == nil {
			return fmt.Sprintf("expected error code %d, got result", tc.ExpectError.Code)
		}
		if resp.Error.Code != tc.ExpectError.Code {
			return fmt.Sprintf("expected error code %d, got %d (%s)", tc.ExpectError.Code, resp.Error.Code, resp.Error.Message)
		}
		return ""
	}
	if resp.Error != nil {
		return fmt.Sprintf("expected success, got error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return "failed to marshal result: " + err.Error()
	}
	var res map[string]interface{}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "failed to unmarshal result: " + err.Error()
	}

	for _, field := range tc.ExpectResultFields {
		if !jsonPathExists(res, field) {
			return fmt.Sprintf("result missing expected field %q", field)
		}
	}
	if tc.ExpectLevelEquals != "" {
		got, _ := jsonPathValue(res, "level")
		if got != tc.ExpectLevelEquals {
			return fmt.Sprintf("expected level %q, got %v", tc.ExpectLevelEquals, got)
		}
	}
	if tc.ExpectSha256Field != "" {
		got, _ := jsonPathValue(res, tc.ExpectSha256Field)
		s, _ := got.(string)
		if !strings.HasPrefix(s, "sha256:") || len(s) != len("sha256:")+64 {
			return fmt.Sprintf("field %q must be a sha256 OCC token, got %v", tc.ExpectSha256Field, got)
		}
	}
	if tc.ExpectArrayField != "" {
		got, _ := jsonPathValue(res, tc.ExpectArrayField)
		arr, ok := got.([]interface{})
		if !ok || len(arr) == 0 {
			return fmt.Sprintf("field %q must be a non-empty array", tc.ExpectArrayField)
		}
	}
	if tc.ExpectEvidence {
		ev, _ := jsonPathValue(res, "data.evidence")
		evMap, ok := ev.(map[string]interface{})
		if !ok {
			return "result must carry evidence metadata at data.evidence"
		}
		switch evMap["provenance"] {
		case "runtime", "static", "contract", "inferred", "external":
		default:
			return fmt.Sprintf("evidence provenance %q is not a spec §6.2 value", evMap["provenance"])
		}
	}
	for path, want := range tc.ExpectValues {
		if expected, ok := want.(string); ok {
			want = markers.substituteText(expected)
		}
		got, exists := jsonPathValue(res, path)
		if !exists {
			return fmt.Sprintf("result missing expected path %q", path)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			return fmt.Sprintf("expected %s=%s, got %s", path, wantJSON, gotJSON)
		}
	}
	if tc.ExpectVersionMatchesContent != "" {
		version, _ := res["newVersion"].(string)
		content := markers.substituteText(tc.ExpectVersionMatchesContent)
		want := Version([]byte(content))
		if version != want {
			return fmt.Sprintf("version token must match written content: expected %s, got %s", want, version)
		}
	}
	return ""
}

// Version returns the OCC token for content.
func Version(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (m Markers) substitute(params map[string]interface{}) map[string]interface{} {
	replacements := map[string]string{
		"${FIXTURE_FILE}":         m.FixtureFile,
		"${MISSING_FILE}":         m.MissingFile,
		"${EDIT_TARGET_FILE}":     m.EditTarget,
		"${EDIT_TARGET_VERSION}":  m.EditVersion,
		"${NEW_FILE}":             m.NewFile,
		"${EDIT_CONTENT}":         m.EditContent,
		"${BROKEN_CONTENT}":       m.BrokenContent,
		"${L2_TARGET}":            m.L2Target,
		"${L3_TARGET}":            m.L3Target,
		"${VERIFY_TARGET}":        m.VerifyTarget,
		"${SYMBOL_FILE}":          m.SymbolFile,
		"${SYMBOL_NAME}":          m.SymbolName,
		"${SYMBOL_REPLACEMENT}":   m.SymbolReplacement,
		"${SYMBOL_VERSION}":       m.SymbolVersion,
		"${SYMBOL_FILE_CONTENT}":  m.SymbolFileContent,
		"${DELETE_FILE}":          m.DeleteFile,
		"${DELETE_VERSION}":       m.DeleteVersion,
		"${RENAME_SOURCE}":        m.RenameSource,
		"${RENAME_TARGET}":        m.RenameTarget,
		"${RENAME_VERSION}":       m.RenameVersion,
		"${EDIT_CONTENT_VERSION}": m.EditContentVersion,
		"${PATCH_REPLACEMENT}":    m.PatchReplacement,
		"${SANDBOX_TARGET}":       m.SandboxTarget,
	}
	var walk func(value interface{}) interface{}
	walk = func(value interface{}) interface{} {
		switch typed := value.(type) {
		case string:
			replaced := typed
			for marker, replacement := range replacements {
				replaced = strings.ReplaceAll(replaced, marker, replacement)
			}
			return replaced
		case []interface{}:
			out := make([]interface{}, len(typed))
			for i, item := range typed {
				out[i] = walk(item)
			}
			return out
		case map[string]interface{}:
			out := make(map[string]interface{}, len(typed))
			for key, item := range typed {
				out[key] = walk(item)
			}
			return out
		default:
			return value
		}
	}
	out := make(map[string]interface{}, len(params))
	for key, value := range params {
		out[key] = walk(value)
	}
	return out
}

// substituteText applies the marker table to one string (expectations too).
func (m Markers) substituteText(text string) string {
	return m.substitute(map[string]interface{}{"value": text})["value"].(string)
}

func jsonPathExists(data interface{}, path string) bool {
	_, ok := jsonPathValue(data, path)
	return ok
}

func jsonPathValue(data interface{}, path string) (interface{}, bool) {
	var current interface{} = data
	for _, p := range strings.Split(path, ".") {
		if arr, ok := current.([]interface{}); ok {
			index := 0
			if _, err := fmt.Sscanf(p, "%d", &index); err != nil || index < 0 || index >= len(arr) {
				return nil, false
			}
			current = arr[index]
			continue
		}
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		val, exists := m[p]
		if !exists {
			return nil, false
		}
		current = val
	}
	return current, true
}

// attainedLevel mirrors the TS determineConformanceLevel: levels are cumulative
// and a level must actually be exercised to be claimed.
func attainedLevel(cases []CaseResult) int {
	attained := 0
	for level := 1; level <= 3; level++ {
		atThisLevel, allPassed := 0, true
		for _, c := range cases {
			if c.Level > level {
				continue
			}
			if c.Level == level {
				atThisLevel++
			}
			if c.Status != "passed" {
				allPassed = false
			}
		}
		if atThisLevel == 0 || !allPassed {
			break
		}
		attained = level
	}
	return attained
}

func summarizeLevels(cases []CaseResult) map[string]string {
	levels := map[string]string{}
	for level := 1; level <= 3; level++ {
		status, seen := "skipped", false
		for _, c := range cases {
			if c.Level != level {
				continue
			}
			seen = true
			if c.Status != "passed" {
				status = "failed"
				break
			}
			status = "passed"
		}
		if !seen {
			status = "skipped"
		}
		levels[fmt.Sprintf("%d", level)] = status
	}
	return levels
}
