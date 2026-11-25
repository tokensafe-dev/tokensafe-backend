package mutate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// Patcher performs symbol-level AST mutations with OCC and compiler validation.
type Patcher struct {
	workspaceDir string
}

// NewPatcher creates a new AST mutator rooted in workspaceDir.
func NewPatcher(workspaceDir string) *Patcher {
	if workspaceDir == "" {
		workspaceDir = "."
	}
	return &Patcher{workspaceDir: workspaceDir}
}

// PatchResult contains results of a surgical AST patch operation.
type PatchResult struct {
	NewVersion   string `json:"newVersion"`
	LinesChanged int    `json:"linesChanged"`
	Formatted    string `json:"formatted"`
}

// ApplySymbolPatch surgically replaces an exact function or type symbol in a Go file.
func (p *Patcher) ApplySymbolPatch(relPath, symbol, replacement, expectedVersion string) (*PatchResult, *protocol.OumvError) {
	cleanPath := filepath.Clean(filepath.Join(p.workspaceDir, relPath))
	currentBytes, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, protocol.NewResourceNotFoundError(relPath)
	}

	// 1. Optimistic Concurrency Control (OCC) Check
	currentHash := sha256.Sum256(currentBytes)
	currentVersion := "sha256:" + hex.EncodeToString(currentHash[:])
	if expectedVersion != "" && expectedVersion != currentVersion {
		return nil, protocol.NewConflictError(
			fmt.Sprintf("OCC version mismatch: expected %s, current is %s", expectedVersion, currentVersion),
			map[string]string{"expected": expectedVersion, "current": currentVersion},
		)
	}

	// 2. Parse source file into AST
	fset := token.NewFileSet()
	fileNode, err := parser.ParseFile(fset, cleanPath, currentBytes, parser.ParseComments)
	if err != nil {
		return nil, protocol.NewSyntaxError(fmt.Sprintf("Failed to parse file: %v", err), err.Error())
	}

	// 3. Parse replacement snippet into AST declaration
	replFset := token.NewFileSet()
	dummyFile := fmt.Sprintf("package dummy\n\n%s\n", replacement)
	replNode, err := parser.ParseFile(replFset, "replacement.go", dummyFile, parser.ParseComments)
	if err != nil {
		return nil, protocol.NewSyntaxError(fmt.Sprintf("Replacement syntax error: %v", err), err.Error())
	}
	if len(replNode.Decls) == 0 {
		return nil, protocol.NewSyntaxError("Replacement does not contain any valid Go declarations", nil)
	}
	newDecl := replNode.Decls[0]

	// 4. Locate symbol in target file and replace node
	replaced := false
	for i, decl := range fileNode.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == symbol {
			fileNode.Decls[i] = newDecl
			replaced = true
			break
		}
		if gen, ok := decl.(*ast.GenDecl); ok {
			for _, spec := range gen.Specs {
				if typeSpec, ok := spec.(*ast.TypeSpec); ok && typeSpec.Name.Name == symbol {
					fileNode.Decls[i] = newDecl
					replaced = true
					break
				}
			}
		}
	}

	if !replaced {
		return nil, protocol.NewTargetNotFoundError(fmt.Sprintf("Symbol %q not found in %s", symbol, relPath))
	}

	// 5. Format modified AST back into source code
	var outBuf bytes.Buffer
	if err := format.Node(&outBuf, fset, fileNode); err != nil {
		return nil, protocol.NewSyntaxError(fmt.Sprintf("Failed to format patched AST: %v", err), err.Error())
	}
	formattedBytes := outBuf.Bytes()

	// 6. Pre-flight compiler type-check
	if typeErr := ValidateASTTypes(fset, fileNode); typeErr != nil {
		return nil, protocol.NewSyntaxError(fmt.Sprintf("Type check failed: %v", typeErr), typeErr.Error())
	}

	// 7. Persist to disk
	if err := os.WriteFile(cleanPath, formattedBytes, 0644); err != nil {
		return nil, &protocol.OumvError{
			Code:    protocol.ErrInternalError,
			Message: "Failed to persist patch: " + err.Error(),
		}
	}

	newHash := sha256.Sum256(formattedBytes)
	newVersion := "sha256:" + hex.EncodeToString(newHash[:])

	oldLineCount := len(strings.Split(string(currentBytes), "\n"))
	newLineCount := len(strings.Split(string(formattedBytes), "\n"))
	linesChanged := newLineCount - oldLineCount
	if linesChanged < 0 {
		linesChanged = -linesChanged
	}

	return &PatchResult{
		NewVersion:   newVersion,
		LinesChanged: linesChanged,
		Formatted:    string(formattedBytes),
	}, nil
}

// ValidateASTTypes performs in-memory Go type checking on an AST file.
func ValidateASTTypes(fset *token.FileSet, file *ast.File) error {
	conf := types.Config{
		Importer: importer.Default(),
		Error: func(err error) {
			// Collect errors
		},
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}

	_, err := conf.Check(file.Name.Name, fset, []*ast.File{file}, info)
	return err
}
