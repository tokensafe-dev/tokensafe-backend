package oumv

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// Wire 1.6.0 runtime surface (spec §3/§4.4/§14): previewEdit, cancel, index,
// query, build. Every method is honest-first: an unavailable feature answers a
// protocol error, never a fabricated result.

// PreviewEdit applies applyEdit's guard order as a dry-run (§4.4): boundary →
// existence → version rules → syntax pre-flight, no bytes written.
func (r *Runtime) PreviewEdit(ctx context.Context, params *protocol.PreviewEditParams) (*protocol.PreviewEditResult, *protocol.OumvError) {
	if !r.config.Capabilities.Mutate {
		return nil, protocol.NewPermissionDeniedError("mutate capability is disabled by policy")
	}
	if params == nil {
		params = &protocol.PreviewEditParams{}
	}
	items := make([]protocol.ApplyEditBatchItem, 0, 1)
	if len(params.Edits) > 0 {
		items = params.Edits
	} else {
		items = append(items, protocol.ApplyEditBatchItem{
			Path:            params.Path,
			Mode:            params.Mode,
			ExpectedVersion: params.ExpectedVersion,
			Replacement:     params.Replacement,
			Hunks:           params.Hunks,
		})
	}
	if len(items) == 0 || len(items) > 16 {
		return nil, protocol.NewValidationFailedError("batch must hold 1..16 items", map[string]interface{}{"reason": "batch_size"})
	}
	previews := make([]protocol.PreviewItem, 0, len(items))
	for _, item := range items {
		preview, oerr := r.previewOne(item)
		if oerr != nil {
			return nil, oerr
		}
		previews = append(previews, *preview)
	}
	return &protocol.PreviewEditResult{Status: "PREVIEW", Applied: false, Previews: previews}, nil
}

func (r *Runtime) previewOne(item protocol.ApplyEditBatchItem) (*protocol.PreviewItem, *protocol.OumvError) {
	rel := strings.TrimSpace(item.Path)
	if rel == "" {
		return nil, protocol.NewInvalidParamsError("path is required")
	}
	mode := strings.ToLower(strings.TrimSpace(item.Mode))
	if mode == "" {
		mode = "replace"
	}
	if !containsString([]string{"replace", "create", "patch", "symbol", "delete", "rename"}, mode) {
		return nil, protocol.NewUnsupportedCapabilityError(fmt.Sprintf("unknown preview mode %q", mode))
	}
	cleanPath, oerr := validateWorkspacePath(r.config.WorkspaceDir, rel)
	if oerr != nil {
		return nil, oerr
	}
	exists := false
	var currentBytes []byte
	if info, err := os.Stat(cleanPath); err == nil && !info.IsDir() {
		exists = true
		currentBytes, _ = os.ReadFile(cleanPath)
	}
	if mode == "create" {
		if item.ExpectedVersion != "" {
			return nil, protocol.NewValidationFailedError("expectedVersion must be omitted for mode=create", map[string]interface{}{"reason": "create_forbids_version", "path": rel})
		}
		if exists {
			return nil, protocol.NewConflictError(fmt.Sprintf("file already exists; use mode=replace with its version: %s", rel), map[string]interface{}{"reason": "file_exists", "path": rel})
		}
	} else {
		if !exists {
			return nil, protocol.NewResourceNotFoundError(fmt.Sprintf("file not found inside project root: %s", rel))
		}
		currentVersion := hashVersion(currentBytes)
		if item.ExpectedVersion == "" {
			return nil, protocol.NewValidationFailedError("expectedVersion is required to preview an edit of an existing file", map[string]interface{}{"reason": "expected_version_required", "path": rel, "current": currentVersion})
		}
		if item.ExpectedVersion != currentVersion {
			return nil, protocol.NewConflictError(fmt.Sprintf("version mismatch: expected %s, current is %s", item.ExpectedVersion, currentVersion), map[string]interface{}{"reason": "version_mismatch", "path": rel})
		}
	}
	replacement := item.Replacement
	syntax := &protocol.SyntaxCheck{Status: "skipped", Tool: "go-parser"}
	if mode != "delete" && mode != "rename" && strings.HasSuffix(cleanPath, ".go") {
		fset := token.NewFileSet()
		if _, perr := parser.ParseFile(fset, cleanPath, replacement, parser.ParseComments); perr != nil {
			syntax = &protocol.SyntaxCheck{Status: "failed", Tool: "go-parser", Error: perr.Error()}
		} else {
			syntax = &protocol.SyntaxCheck{Status: "passed", Tool: "go-parser"}
		}
	}
	newVersion := hashVersion([]byte(replacement))
	currentVersion := ""
	if exists {
		currentVersion = hashVersion(currentBytes)
	}
	return &protocol.PreviewItem{
		Path:           rel,
		Mode:           mode,
		Unchanged:      exists && newVersion == currentVersion,
		CurrentVersion: orOmit(currentVersion),
		NewVersion:     newVersion,
		SyntaxCheck:    syntax,
		WouldApply:     true,
	}, nil
}

// Cancel answers the §3 best-effort contract: nothing in this runtime is
// observably in-flight, so every call is a result, never an error.
func (r *Runtime) Cancel(_ context.Context, params *protocol.CancelParams) (*protocol.CancelResult, *protocol.OumvError) {
	if params != nil && params.Target == "last" {
		return &protocol.CancelResult{Cancelled: false, Finished: true}, nil
	}
	out := &protocol.CancelResult{Cancelled: false}
	if params != nil {
		out.RequestID = params.RequestID
	}
	return out, nil
}

// Index serves the §14.1 repo-wide symbol search from the Go AST.
func (r *Runtime) Index(_ context.Context, params *protocol.IndexParams) (*protocol.IndexResult, *protocol.OumvError) {
	query := strings.TrimSpace(params.Query)
	if query == "" {
		return nil, protocol.NewInvalidParamsError("query is required")
	}
	q := strings.ToLower(query)
	kindFilter := map[string]bool{}
	for _, k := range params.Kinds {
		kindFilter[strings.ToLower(k)] = true
	}
	limit := params.Limit
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	type hit struct {
		rank  int
		match protocol.IndexMatch
	}
	hits := []hit{}
	root := r.config.WorkspaceDir
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", "node_modules", "vendor", "dist", "build", ".tokensafe", ".oumv", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.Size() > 512*1024 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if params.Scope != "" && !strings.HasPrefix(rel, strings.TrimPrefix(params.Scope, "/")) {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil
		}
		add := func(name, kind string, line int) {
			lower := strings.ToLower(name)
			rank := -1
			switch {
			case q == lower:
				rank = 0
			case strings.HasPrefix(lower, q):
				rank = 1
			case len(q) >= 2 && strings.Contains(lower, q):
				rank = 2
			}
			if rank < 0 {
				return
			}
			if len(kindFilter) > 0 && !kindFilter[kind] {
				return
			}
			hits = append(hits, hit{rank: rank, match: protocol.IndexMatch{
				ResourceID: fmt.Sprintf("%s:%s@%s", kind, name, rel),
				Name:       name,
				Kind:       kind,
				File:       rel,
				Lines:      []int{line, line},
				Match:      []string{"exact", "prefix", "substring"}[rank],
			}})
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				kind := "function"
				if decl.Recv != nil {
					kind = "method"
				}
				add(decl.Name.Name, kind, fset.Position(decl.Pos()).Line)
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						add(ts.Name.Name, "class", fset.Position(ts.Pos()).Line)
					}
				}
			}
		}
		return nil
	})
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		if hits[i].match.File != hits[j].match.File {
			return hits[i].match.File < hits[j].match.File
		}
		return hits[i].match.Name < hits[j].match.Name
	})
	total := len(hits)
	truncated := total > limit
	if truncated {
		hits = hits[:limit]
	}
	matches := make([]protocol.IndexMatch, 0, len(hits))
	for _, h := range hits {
		matches = append(matches, h.match)
	}
	return &protocol.IndexResult{Matches: matches, Truncated: truncated, TotalMatches: total}, nil
}

// Query executes a read-only statement over the wired sandbox engine (§14.2).
func (r *Runtime) Query(ctx context.Context, params *protocol.QueryParams) (*protocol.QueryResult, *protocol.OumvError) {
	if r.dbSandbox == nil || !r.dbSandbox.SupportsReads() {
		return nil, protocol.NewUnsupportedCapabilityError("no query-capable database engine is wired into this runtime")
	}
	limit := params.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	result, err := r.dbSandbox.ReadQuery(ctx, params.SQL, params.Params, limit)
	if err != nil {
		if strings.Contains(err.Error(), "only SELECT/EXPLAIN") {
			return nil, protocol.NewSecurityViolationError(err.Error(), map[string]interface{}{"reason": "query_forbidden"})
		}
		return nil, &protocol.OumvError{Code: protocol.ErrInternalError, Message: err.Error()}
	}
	return &protocol.QueryResult{
		Columns:   result.Columns,
		Rows:      result.Rows,
		RowCount:  result.RowCount,
		Truncated: result.Truncated,
	}, nil
}

// Build runs the §14.3 command (params.Command overrides the configured default)
// and publishes observed errors as diagnostic events (§2.6).
func (r *Runtime) Build(ctx context.Context, params *protocol.BuildParams) (*protocol.BuildResult, *protocol.OumvError) {
	command := strings.TrimSpace(params.Command)
	if command == "" {
		command = strings.TrimSpace(r.config.BuildCommand)
	}
	if command == "" {
		return &protocol.BuildResult{
			Status:     "skipped",
			Command:    "none",
			Diagnostics: []protocol.BuildDiagnostic{},
			SkipReason: "no build command is configured for this runtime (set RuntimeConfig.BuildCommand or pass command)",
		}, nil
	}
	fields := strings.Fields(command)
	timeout := time.Duration(params.TimeoutMs) * time.Millisecond
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(runCtx, fields[0], fields[1:]...)
	cmd.Dir = r.config.WorkspaceDir
	outBytes, runErr := cmd.CombinedOutput()
	durationMs := int(time.Since(started).Milliseconds())
	diagnostics := parseBuildDiagnostics(string(outBytes), fields[0])
	for i, diag := range diagnostics {
		if diag.Severity == "error" && i < 20 {
			r.appendEvent("diagnostic", diag)
		}
	}
	status := "passed"
	if runErr != nil {
		status = "failed"
		if ctxErr := runCtx.Err(); ctxErr != nil && ctxErr == context.DeadlineExceeded {
			diagnostics = append(diagnostics, protocol.BuildDiagnostic{
				Severity: "error", Source: "build", Tool: fields[0],
				Message: fmt.Sprintf("build timed out after %d ms", params.TimeoutMs), Reason: "timeout",
			})
		}
	}
	return &protocol.BuildResult{
		Status:      status,
		Command:     command,
		Diagnostics: diagnostics,
		DurationMs:  durationMs,
		Truncated:   len(diagnostics) >= 50,
	}, nil
}

// parseBuildDiagnostics turns tool output into §2.6-shaped diagnostics.
func parseBuildDiagnostics(output string, tool string) []protocol.BuildDiagnostic {
	diagnostics := make([]protocol.BuildDiagnostic, 0, 16)
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		trimmed := line
		if len(trimmed) > 500 {
			trimmed = trimmed[:500]
		}
		lower := strings.ToLower(trimmed)
		severity := "info"
		switch {
		case strings.Contains(lower, "error") || strings.Contains(lower, "failed"):
			severity = "error"
		case strings.Contains(lower, "warn"):
			severity = "warning"
		}
		diagnostics = append(diagnostics, protocol.BuildDiagnostic{
			Severity: severity,
			Source:   "build",
			Message:  trimmed,
			Tool:     tool,
		})
		if len(diagnostics) >= 50 {
			break
		}
	}
	return diagnostics
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func orOmit(value string) string {
	return value
}
