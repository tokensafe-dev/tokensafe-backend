package mutate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/pkg/mutate"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/protocol"
)

func TestApplySymbolPatch(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "service.go")

	initialCode := `package main

func CalculateTax(amount float64) float64 {
	return amount * 0.1
}

func GetName() string {
	return "sample"
}
`
	if err := os.WriteFile(filePath, []byte(initialCode), 0644); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	hash := sha256.Sum256([]byte(initialCode))
	initialVersion := "sha256:" + hex.EncodeToString(hash[:])

	patcher := mutate.NewPatcher(tmpDir)

	// 1. Successful surgical replacement of CalculateTax
	replacement := `func CalculateTax(amount float64) float64 {
	return amount * 0.2
}`
	res, oumvErr := patcher.ApplySymbolPatch("service.go", "CalculateTax", replacement, initialVersion)
	if oumvErr != nil {
		t.Fatalf("expected successful patch, got: %v", oumvErr)
	}
	if res.NewVersion == "" {
		t.Errorf("expected non-empty version")
	}

	updatedContent, _ := os.ReadFile(filePath)
	if !strings.Contains(string(updatedContent), "amount * 0.2") {
		t.Errorf("expected updated tax calculation in file, got:\n%s", string(updatedContent))
	}
	// Verify other functions are completely preserved
	if !strings.Contains(string(updatedContent), "GetName()") {
		t.Errorf("expected GetName() to remain untouched")
	}

	// 2. OCC Conflict check
	_, oumvErr = patcher.ApplySymbolPatch("service.go", "CalculateTax", replacement, "sha256:staleversion")
	if oumvErr == nil || oumvErr.Code != protocol.ErrConflictDetected {
		t.Errorf("expected ErrConflictDetected, got: %v", oumvErr)
	}

	// 3. Syntax error in replacement
	brokenSyntax := `func CalculateTax( { broken`
	_, oumvErr = patcher.ApplySymbolPatch("service.go", "CalculateTax", brokenSyntax, res.NewVersion)
	if oumvErr == nil || oumvErr.Code != protocol.ErrSyntaxError {
		t.Errorf("expected ErrSyntaxError for broken replacement, got: %v", oumvErr)
	}

	// 4. Target symbol not found
	_, oumvErr = patcher.ApplySymbolPatch("service.go", "NonExistentFunc", replacement, res.NewVersion)
	if oumvErr == nil || oumvErr.Code != protocol.ErrTargetNotFound {
		t.Errorf("expected ErrTargetNotFound, got: %v", oumvErr)
	}
}
