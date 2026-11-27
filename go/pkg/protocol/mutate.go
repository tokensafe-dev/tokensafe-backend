package protocol

// GetSourceParams defines the request payload for `oumv/getSource`.
type GetSourceParams struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path,omitempty"`
	// StartLine/EndLine return a 1-based inclusive window (spec §4.3, wire 1.3.0).
	// EndLine past EOF is clamped; Version is always the whole file's hash.
	StartLine int `json:"startLine,omitempty"`
	EndLine   int `json:"endLine,omitempty"`
	// IfVersion is the dedup pointer: when the file still has this version the
	// response is {path, version, unchanged:true} and carries no source.
	IfVersion string `json:"ifVersion,omitempty"`
}

// GetSourceResult defines the response payload for `oumv/getSource`.
type GetSourceResult struct {
	Symbol    string `json:"symbol,omitempty"`
	Path      string `json:"path"`
	Source    string `json:"source,omitempty"`
	Unchanged bool   `json:"unchanged,omitempty"`
	Version   string `json:"version"` // "sha256:..." of the whole file
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
}

// PatchHunk is one exact-snippet splice for `applyEdit mode="patch"` (spec §4.3).
type PatchHunk struct {
	Target      string `json:"target"`
	Replacement string `json:"replacement"`
	StartLine   int    `json:"startLine,omitempty"`
	EndLine     int    `json:"endLine,omitempty"`
	ReplaceAll  bool   `json:"replaceAll,omitempty"`
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
	// RenameTo is the destination for mode="rename" (jail-checked, must not exist).
	RenameTo string `json:"renameTo,omitempty"`
	// Hunks carries exact-snippet splices for mode="patch" (wire 1.3.0).
	Hunks []PatchHunk `json:"hunks,omitempty"`
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

	Created      bool   `json:"created,omitempty"`
	Deleted      bool   `json:"deleted,omitempty"`
	NewPath      string `json:"newPath,omitempty"`
	HunksApplied int    `json:"hunksApplied,omitempty"`

	Success      bool   `json:"-"`
	LinesChanged int    `json:"-"`
	Message      string `json:"-"`
}
