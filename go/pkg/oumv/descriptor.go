package oumv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Descriptor is the zero-footprint discovery file from spec §9.2
// (JSON Schema: schemas/descriptor.schema.json in the oumv repo).
type Descriptor struct {
	Type         string              `json:"type"`
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	URL          string              `json:"url"`
	SessionToken string              `json:"sessionToken,omitempty"`
	Workspace    string              `json:"workspace,omitempty"`
	Capabilities DescriptorCapacitiy `json:"capabilities"`
	StartedAt    string              `json:"startedAt,omitempty"`
}

// DescriptorCapacitiy keeps the descriptor shape stable with the frontend writer.
type DescriptorCapacitiy struct {
	ObserveLevels []string `json:"observeLevels"`
	Transports    []string `json:"transports,omitempty"`
}

// DescriptorPath returns ~/.tokensafe/runtimes/<sha256(realpath(workspace))[:16]>.json
// (TOKENSAFE_HOME overrides the root).
func DescriptorPath(workspace string) string {
	home := os.Getenv("TOKENSAFE_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".tokensafe")
		} else {
			home = ".tokensafe"
		}
	}
	real, err := filepath.Abs(workspace)
	if err != nil {
		real = workspace
	}
	sum := sha256.Sum256([]byte(real))
	return filepath.Join(home, "runtimes", hex.EncodeToString(sum[:])[:16]+".json")
}

// WriteDescriptor writes the discovery file with mode 0600, never into the repo.
func (r *Runtime) WriteDescriptor(publicURL string) (string, error) {
	path := DescriptorPath(r.workspaceDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	levels := []string{"L0", "L1", "L2", "L3", "L4"}
	descriptor := Descriptor{
		Type:         "backend",
		Name:         r.name,
		Version:      r.version,
		URL:          publicURL,
		SessionToken: r.config.Security.AuthToken,
		Workspace:    r.workspaceDir,
		Capabilities: DescriptorCapacitiy{ObserveLevels: levels, Transports: []string{"http", "stdio"}},
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// DeleteDescriptor removes the discovery file (clean shutdown).
func (r *Runtime) DeleteDescriptor() error {
	return os.Remove(DescriptorPath(r.workspaceDir))
}
