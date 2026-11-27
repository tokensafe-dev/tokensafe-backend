package protocol

import (
	"crypto/sha256"
	"encoding/hex"
)

// Wire 1.6.0 (spec §3/§4.4/§14): previewEdit, cancel, index, query, build.

// PreviewEditParams is the §4.4 dry-run request: applyEdit's shape, no bytes written.
type PreviewEditParams struct {
	SessionID       string              `json:"sessionId,omitempty"`
	RequestID       string              `json:"requestId,omitempty"`
	TimeoutMs       int                 `json:"timeoutMs,omitempty"`
	IdempotencyKey  string              `json:"idempotencyKey,omitempty"`
	Path            string              `json:"path,omitempty"`
	Mode            string              `json:"mode,omitempty"`
	Symbol          string              `json:"symbol,omitempty"`
	RenameTo        string              `json:"renameTo,omitempty"`
	Replacement     string              `json:"replacement,omitempty"`
	ExpectedVersion string              `json:"expectedVersion,omitempty"`
	Hunks           []PatchHunk         `json:"hunks,omitempty"`
	Edits           []ApplyEditBatchItem `json:"edits,omitempty"`
}

// PreviewItem is the per-file dry-run verdict.
type PreviewItem struct {
	Path           string       `json:"path"`
	Mode           string       `json:"mode"`
	Unchanged      bool         `json:"unchanged,omitempty"`
	CurrentVersion string       `json:"currentVersion,omitempty"`
	NewVersion     string       `json:"newVersion,omitempty"`
	SyntaxCheck    *SyntaxCheck `json:"syntaxCheck,omitempty"`
	WouldApply     bool         `json:"wouldApply"`
}

// PreviewEditResult answers with what applyEdit WOULD do; applied is always false.
type PreviewEditResult struct {
	Status   string        `json:"status"`
	Applied  bool          `json:"applied"`
	Previews []PreviewItem `json:"previews"`
}

// CancelParams names the request to cancel (§3); Target "last" is the newest.
type CancelParams struct {
	RequestID string `json:"requestId,omitempty"`
	Target    string `json:"target,omitempty"`
}

// CancelResult is best-effort: unknown ids are a result, never an error.
type CancelResult struct {
	Cancelled bool   `json:"cancelled"`
	RequestID string `json:"requestId,omitempty"`
	Finished  bool   `json:"finished,omitempty"`
}

// IndexParams is the §14.1 symbol-search request.
type IndexParams struct {
	Query string   `json:"query"`
	Kinds []string `json:"kinds,omitempty"`
	Scope string   `json:"scope,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

// IndexMatch is one ranked hit.
type IndexMatch struct {
	ResourceID string `json:"resourceId"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	File       string `json:"file"`
	Lines      []int  `json:"lines"`
	Signature  string `json:"signature,omitempty"`
	Refs       int    `json:"refs,omitempty"`
	Match      string `json:"match"`
}

// IndexResult is the ranked-match contract.
type IndexResult struct {
	Matches      []IndexMatch `json:"matches"`
	Truncated    bool         `json:"truncated"`
	TotalMatches int          `json:"totalMatches,omitempty"`
}

// QueryParams is the §14.2 read-only request.
type QueryParams struct {
	SQL    string        `json:"sql"`
	Params []interface{} `json:"params,omitempty"`
	Limit  int           `json:"limit,omitempty"`
}

// QueryResult carries the capped rows.
type QueryResult struct {
	Columns   []string        `json:"columns"`
	Rows      [][]interface{} `json:"rows"`
	RowCount  int             `json:"rowCount"`
	Truncated bool            `json:"truncated"`
}

// BuildParams runs the §14.3 command (params.Command overrides the configured one).
type BuildParams struct {
	Command   string `json:"command,omitempty"`
	Target    string `json:"target,omitempty"`
	TimeoutMs int    `json:"timeoutMs,omitempty"`
}

// BuildDiagnostic mirrors the §2.6 event payload shape.
type BuildDiagnostic struct {
	Severity string `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// BuildResult is the structured verdict; "skipped" MUST carry SkipReason.
type BuildResult struct {
	Status      string            `json:"status"`
	Command     string            `json:"command"`
	Diagnostics []BuildDiagnostic `json:"diagnostics"`
	DurationMs  int               `json:"durationMs,omitempty"`
	Truncated   bool              `json:"truncated,omitempty"`
	SkipReason  string            `json:"skipReason,omitempty"`
}

// VersionOf hashes bytes into the OCC token shape.
func VersionOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
