package oumv

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/conformance"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

// TestConformanceCases runs the language-neutral OUMV conformance suite through
// the JSON-RPC dispatcher and asserts the level the runtime actually attains.
// The Go runtime implements all three levels, so the claim is Level 3 — if a case
// regresses, this test fails with the blocking case ids instead of silently
// keeping a higher claim.
func TestConformanceCases(t *testing.T) {
	spec, err := conformance.LoadCases(filepath.Join("testdata", "conformance-cases.json"))
	if err != nil {
		t.Fatal(err)
	}

	const (
		fixtureFile   = "conformance_fixture.go"
		editTarget    = "conformance_edit_target.go"
		missingFile   = "conformance_missing_file.go"
		editContent   = "package conformance\n\nfunc Value() int {\n\treturn 42\n}\n"
		brokenContent = "package conformance\n\nfunc Broken( {\n"
	)

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, fixtureFile), []byte("package conformance\n"), 0644); err != nil {
		t.Fatal(err)
	}
	originalEditTarget := []byte("package conformance\n\nfunc Value() int {\n\treturn 41\n}\n")
	if err := os.WriteFile(filepath.Join(tmpDir, editTarget), originalEditTarget, 0644); err != nil {
		t.Fatal(err)
	}

	symbolFile := "conformance_symbol_target.go"
	symbolOriginal := []byte("package conformance\n\nfunc Symbol() int {\n\treturn 41\n}\n")
	symbolAfter := []byte("package conformance\n\nfunc Symbol() int {\n\treturn 42\n}\n")
	if err := os.WriteFile(filepath.Join(tmpDir, symbolFile), symbolOriginal, 0644); err != nil {
		t.Fatal(err)
	}
	deleteFile := "conformance_delete_target.go"
	deleteOriginal := []byte("package conformance\n\nvar DeleteMe = true\n")
	if err := os.WriteFile(filepath.Join(tmpDir, deleteFile), deleteOriginal, 0644); err != nil {
		t.Fatal(err)
	}
	renameSource := "conformance_rename_source.go"
	renameTarget := "conformance_rename_target.go"
	renameOriginal := []byte("package conformance\n\nvar RenameMe = true\n")
	if err := os.WriteFile(filepath.Join(tmpDir, renameSource), renameOriginal, 0644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/conformance", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	rt := newTestRuntime(t, mux)
	rt.workspaceDir = tmpDir
	rt.RegisterRoute("GET", "/conformance", "conformance.Handler")

	report := conformance.Run(transport.NewDispatcher(rt), spec, conformance.Markers{
		FixtureFile:        fixtureFile,
		MissingFile:        missingFile,
		EditTarget:         editTarget,
		NewFile:            "created_new_file.go",
		EditVersion:        conformance.Version(originalEditTarget),
		EditContent:        editContent,
		EditContentVersion: conformance.Version([]byte(editContent)),
		PatchReplacement:   editContent + "\n// patched by conformance\n",
		BrokenContent:      brokenContent,
		SymbolFile:         symbolFile,
		SymbolName:         "Symbol",
		SymbolReplacement:  "func Symbol() int {\n\treturn 42\n}\n",
		SymbolVersion:      conformance.Version(symbolOriginal),
		SymbolFileContent:  string(symbolAfter),
		DeleteFile:         deleteFile,
		DeleteVersion:      conformance.Version(deleteOriginal),
		RenameSource:       renameSource,
		RenameTarget:       renameTarget,
		RenameVersion:      conformance.Version(renameOriginal),
		L2Target:           "GET /conformance",
		L3Target:           editTarget,
		VerifyTarget:       "GET /conformance",
	})
	report.AdapterName = rt.Name()

	t.Logf("OUMV conformance: level %d (fixture v%d) — %d/%d cases passed",
		report.AttainedLevel, report.FixtureVersion, report.Passed, report.Total)

	if report.AttainedLevel < 3 {
		t.Errorf("Go runtime attains conformance level %d, expected 3; blocking cases: %v",
			report.AttainedLevel, report.FailedCaseIDs(3))
		for _, c := range report.Cases {
			if c.Status != "passed" {
				t.Logf("  %s (L%d): %s", c.ID, c.Level, c.Error)
			}
		}
	}
}
