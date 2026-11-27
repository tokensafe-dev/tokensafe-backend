package oumv_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

func versionOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newWorkspaceRuntime(t *testing.T) (*oumv.Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	rt := oumv.NewRuntime(oumv.Config{
		Name:         "patch-rt",
		Version:      "test",
		WorkspaceDir: dir,
		Runtime: &oumv.RuntimeConfig{
			Enabled:      true,
			Environment:  oumv.EnvDevelopment,
			WorkspaceDir: dir,
			Capabilities: oumv.CapabilitiesConfig{
				Observe: true, Understand: true, Mutate: true, Verify: true,
			},
		},
	})
	return rt, dir
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestApplyPatchUniqueHunkApplies(t *testing.T) {
	rt, dir := newWorkspaceRuntime(t)
	before := "package main\n\nfunc Value() int {\n\treturn 1\n}\n"
	writeFile(t, dir, "app.go", before)

	res, omErr := rt.ApplyEdit(context.Background(), &protocol.ApplyEditParams{
		Path:            "app.go",
		Mode:            "patch",
		ExpectedVersion: versionOf([]byte(before)),
		Hunks:           []protocol.PatchHunk{{Target: "return 1", Replacement: "return 2"}},
	})
	if omErr != nil {
		t.Fatalf("patch failed: %v", omErr)
	}
	after := readFile(t, dir, "app.go")
	if !strings.Contains(after, "return 2") {
		t.Fatalf("replacement not applied: %q", after)
	}
	if res.HunksApplied != 1 {
		t.Fatalf("hunksApplied = %d, want 1", res.HunksApplied)
	}
	if res.NewVersion != versionOf([]byte(after)) {
		t.Fatalf("newVersion does not match the bytes on disk")
	}
}

func TestApplyPatchIsAtomicAcrossHunks(t *testing.T) {
	rt, dir := newWorkspaceRuntime(t)
	before := "package main\n\nvar A = 1\n"
	writeFile(t, dir, "app.go", before)

	_, omErr := rt.ApplyEdit(context.Background(), &protocol.ApplyEditParams{
		Path:            "app.go",
		Mode:            "patch",
		ExpectedVersion: versionOf([]byte(before)),
		Hunks: []protocol.PatchHunk{
			{Target: "var A = 1", Replacement: "var A = 2"},
			{Target: "does-not-exist", Replacement: "x"},
		},
	})
	if omErr == nil || omErr.Code != protocol.ErrResourceNotFound {
		t.Fatalf("want -32009 for the missing hunk, got %v", omErr)
	}
	if got := readFile(t, dir, "app.go"); got != before {
		t.Fatalf("a failed hunk must leave the file untouched, got %q", got)
	}
}

func TestApplyPatchHunksSeeEarlierHunks(t *testing.T) {
	rt, dir := newWorkspaceRuntime(t)
	before := "package main\n\nvar A = 1\n"
	writeFile(t, dir, "app.go", before)

	res, omErr := rt.ApplyEdit(context.Background(), &protocol.ApplyEditParams{
		Path:            "app.go",
		Mode:            "patch",
		ExpectedVersion: versionOf([]byte(before)),
		Hunks: []protocol.PatchHunk{
			{Target: "var A = 1", Replacement: "var A = 2"},
			{Target: "var A = 2", Replacement: "var A = 3"},
		},
	})
	if omErr != nil {
		t.Fatalf("patch failed: %v", omErr)
	}
	if got := readFile(t, dir, "app.go"); !strings.Contains(got, "var A = 3") {
		t.Fatalf("second hunk must match the text the first one produced, got %q", got)
	}
	if res.HunksApplied != 2 {
		t.Fatalf("hunksApplied = %d, want 2", res.HunksApplied)
	}
}

func TestApplyPatchAmbiguousTargetNamesLines(t *testing.T) {
	rt, dir := newWorkspaceRuntime(t)
	before := "package main\n\nvar A = \"same\"\nvar B = \"same\"\n"
	writeFile(t, dir, "app.go", before)

	_, omErr := rt.ApplyEdit(context.Background(), &protocol.ApplyEditParams{
		Path:            "app.go",
		Mode:            "patch",
		ExpectedVersion: versionOf([]byte(before)),
		Hunks:           []protocol.PatchHunk{{Target: "\"same\"", Replacement: "\"other\""}},
	})
	if omErr == nil || omErr.Code != protocol.ErrValidationFailed {
		t.Fatalf("want -32011 for an ambiguous hunk, got %v", omErr)
	}
	data, ok := omErr.Data.(map[string]interface{})
	if !ok || data["reason"] != "target_not_unique" {
		t.Fatalf("want reason=target_not_unique, got %#v", omErr.Data)
	}
	lines, ok := data["lines"].([]int)
	if !ok || len(lines) != 2 || lines[0] != 3 || lines[1] != 4 {
		t.Fatalf("want occurrence lines [3 4], got %#v", data["lines"])
	}
}

func TestApplyPatchRejectsStaleVersion(t *testing.T) {
	rt, dir := newWorkspaceRuntime(t)
	before := "package main\n\nvar A = 1\n"
	writeFile(t, dir, "app.go", before)

	_, omErr := rt.ApplyEdit(context.Background(), &protocol.ApplyEditParams{
		Path:            "app.go",
		Mode:            "patch",
		ExpectedVersion: "sha256:" + strings.Repeat("0", 64),
		Hunks:           []protocol.PatchHunk{{Target: "var A = 1", Replacement: "var A = 2"}},
	})
	if omErr == nil || omErr.Code != protocol.ErrConflictDetected {
		t.Fatalf("want -32001 for a stale version, got %v", omErr)
	}
}

func TestGetSourceWindowKeepsWholeFileVersion(t *testing.T) {
	rt, dir := newWorkspaceRuntime(t)
	content := "package main\n\nvar A = 1\nvar B = 2\nvar C = 3\n"
	writeFile(t, dir, "app.go", content)

	res, omErr := rt.GetSource(context.Background(), &protocol.GetSourceParams{
		Path: "app.go", StartLine: 3, EndLine: 4,
	})
	if omErr != nil {
		t.Fatalf("getSource failed: %v", omErr)
	}
	if res.Source != "var A = 1\nvar B = 2" {
		t.Fatalf("unexpected window %q", res.Source)
	}
	if res.StartLine != 3 || res.EndLine != 4 {
		t.Fatalf("window = %d-%d, want 3-4", res.StartLine, res.EndLine)
	}
	if res.Version != versionOf([]byte(content)) {
		t.Fatalf("version must hash the whole file, got %s", res.Version)
	}
}

func TestGetSourceIfVersionReturnsNothing(t *testing.T) {
	rt, dir := newWorkspaceRuntime(t)
	content := "package main\n\nvar A = 1\n"
	writeFile(t, dir, "app.go", content)

	res, omErr := rt.GetSource(context.Background(), &protocol.GetSourceParams{
		Path: "app.go", IfVersion: versionOf([]byte(content)),
	})
	if omErr != nil {
		t.Fatalf("getSource failed: %v", omErr)
	}
	if !res.Unchanged || res.Source != "" {
		t.Fatalf("want unchanged with no source, got unchanged=%v source=%q", res.Unchanged, res.Source)
	}
}

func TestGetSourceUnknownSymbolIsNotFound(t *testing.T) {
	rt, dir := newWorkspaceRuntime(t)
	writeFile(t, dir, "app.go", "package main\n\nfunc Real() {}\n")

	_, omErr := rt.GetSource(context.Background(), &protocol.GetSourceParams{
		Path: "app.go", Symbol: "Missing",
	})
	if omErr == nil || omErr.Code != protocol.ErrResourceNotFound {
		t.Fatalf("want -32009 for an unknown symbol, got %v", omErr)
	}
}

func TestHandshakeRejectsUnknownProtocolVersion(t *testing.T) {
	rt, _ := newWorkspaceRuntime(t)

	if _, omErr := rt.Handshake(context.Background(), &protocol.OumvHandshakeParams{ProtocolVersion: "9.9.9"}); omErr == nil || omErr.Code != protocol.ErrUnsupportedCapability {
		t.Fatalf("want -32006 for MAJOR 9, got %v", omErr)
	}
	if _, omErr := rt.Handshake(context.Background(), &protocol.OumvHandshakeParams{ProtocolVersion: "1.1.0"}); omErr != nil {
		t.Fatalf("an earlier MINOR must be accepted, got %v", omErr)
	}
	if _, omErr := rt.Handshake(context.Background(), &protocol.OumvHandshakeParams{ProtocolVersion: "1.2.0"}); omErr != nil {
		t.Fatalf("wire 1.2 clients must stay supported, got %v", omErr)
	}
}

func TestVerifyWithoutHandlerIsUnavailable(t *testing.T) {
	rt, _ := newWorkspaceRuntime(t)

	_, omErr := rt.Verify(context.Background(), &protocol.VerifyParams{Target: "GET /x"})
	if omErr == nil || omErr.Code != protocol.ErrRuntimeUnavailable {
		t.Fatalf("want -32008 without a handler, got %v", omErr)
	}
}
