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
// (TOKENSAFE_HOME overrides the root). The file is a registry keyed by profile, so a
// React frontend and a Go backend in one workspace never overwrite each other.
func DescriptorPath(workspace string) string {
	home := os.Getenv("TOKENSAFE_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".tokensafe")
		} else {
			home = ".tokensafe"
		}
	}
	sum := sha256.Sum256([]byte(realWorkspace(workspace)))
	return filepath.Join(home, "runtimes", hex.EncodeToString(sum[:])[:16]+".json")
}

func realWorkspace(workspace string) string {
	real, err := filepath.Abs(workspace)
	if err != nil {
		real = workspace
	}
	if resolved, resolveErr := filepath.EvalSymlinks(real); resolveErr == nil {
		real = resolved
	}
	return real
}

// transportsOrDefault reports the transports this runtime actually mounted: the
// registry must not advertise SSE/WS for a runtime that only serves HTTP.
func (r *Runtime) transportsOrDefault() []string {
	if len(r.transports) > 0 {
		return r.transports
	}
	return []string{"http"}
}

// SetTransports records the transports an adapter mounted (http, sse, ws).
func (r *Runtime) SetTransports(transports ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transports = transports
}

// WriteDescriptor registers this runtime in the workspace discovery registry
// (mode 0600, never inside the repository), preserving other profile entries.
func (r *Runtime) WriteDescriptor(publicURL string) (string, error) {
	path := DescriptorPath(r.workspaceDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	registry, err := readRegistry(path)
	if err != nil {
		return "", err
	}
	levels := []string{"L0", "L1", "L2", "L3", "L4"}
	descriptor := Descriptor{
		Type:         "backend",
		Name:         r.name,
		Version:      r.version,
		URL:          publicURL,
		SessionToken: r.config.Security.AuthToken,
		Workspace:    realWorkspace(r.workspaceDir),
		Capabilities: DescriptorCapacitiy{ObserveLevels: levels, Transports: r.transportsOrDefault()},
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return "", err
	}
	registry["backend"] = raw
	registry["workspaceRoot"] = mustJSON(realWorkspace(r.workspaceDir))
	registry["pid"] = mustJSON(os.Getpid())
	registry["updatedAt"] = mustJSON(time.Now().UnixMilli())
	return path, writeRegistry(path, registry)
}

// DeleteDescriptor drops this runtime's entry (clean shutdown) and removes the
// file once no profile is left in it.
func (r *Runtime) DeleteDescriptor() error {
	path := DescriptorPath(r.workspaceDir)
	registry, err := readRegistry(path)
	if err != nil {
		return err
	}
	delete(registry, "backend")
	if _, ok := registry["frontend"]; !ok {
		return os.Remove(path)
	}
	return writeRegistry(path, registry)
}

func mustJSON(value interface{}) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}

// readRegistry keeps unknown profile sections (written by the frontend or a newer
// adapter) byte-for-byte, so writers of different profiles merge instead of clobber.
func readRegistry(path string) (map[string]json.RawMessage, error) {
	registry := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return registry, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return registry, nil
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		// A corrupt registry must not block starting a runtime.
		return map[string]json.RawMessage{}, nil
	}
	return registry, nil
}

func writeRegistry(path string, registry map[string]json.RawMessage) error {
	raw, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temp := path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
