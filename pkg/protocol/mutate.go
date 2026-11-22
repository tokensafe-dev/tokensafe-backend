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
	ExpectedVersion string `json:"expectedVersion"` // "sha256:..."
}

// ApplyEditResult defines the response payload for `oumv/applyEdit`.
type ApplyEditResult struct {
	Success      bool   `json:"success"`
	NewVersion   string `json:"newVersion"`
	LinesChanged int    `json:"linesChanged"`
	Message      string `json:"message,omitempty"`
}
