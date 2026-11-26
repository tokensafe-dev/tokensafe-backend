package protocol

// GetSourceParams defines the request payload for `oumv/getSource`.
type GetSourceParams struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path,omitempty"`
}

// GetSourceResult defines the response payload for `oumv/getSource`.
type GetSourceResult struct {
	Symbol    string `json:"symbol"`
	Path      string `json:"path"`
	Source    string `json:"source"`
	Version   string `json:"version"` // "sha256:..."
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

// ApplyEditParams defines the request payload for `oumv/applyEdit`.
type ApplyEditParams struct {
	Symbol          string `json:"symbol"`
	Path            string `json:"path"`
	Replacement     string `json:"replacement"`
	ExpectedVersion string `json:"expectedVersion"` // "sha256:..." (mandatory for replace)
	// Mode is "replace" (default) or "create"; create forbids expectedVersion and
	// refuses to overwrite an existing file (spec §4.3).
	Mode string `json:"mode,omitempty"`
}

// SyntaxCheck reports the pre-flight guard outcome (spec §4.3).
type SyntaxCheck struct {
	Status string `json:"status"` // "passed" | "skipped" | "failed"
	Tool   string `json:"tool,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ApplyEditResult defines the response payload for `oumv/applyEdit`.
// Diagnostics (success/linesChanged/message) stay in-process: the wire carries the
// canonical fields only, and deprecated aliases are accepted but never emitted.
type ApplyEditResult struct {
	Status       string       `json:"status"` // "APPLIED" | "CURRENT"
	Path         string       `json:"path"`
	Symbol       string       `json:"symbol,omitempty"`
	NewVersion   string       `json:"newVersion"`
	SyntaxCheck  *SyntaxCheck `json:"syntaxCheck,omitempty"`
	BackupPath   string       `json:"backupPath,omitempty"`
	RollbackHint string       `json:"rollbackHint,omitempty"`

	Created      bool `json:"created,omitempty"`

	Success      bool `json:"-"`
	LinesChanged int  `json:"-"`
	Message      string `json:"-"`
}
