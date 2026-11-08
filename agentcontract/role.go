package agentcontract

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Role is the single, transport-independent description shipped by Box.
type Role struct {
	Schema            string          `json:"schema"`
	ID                string          `json:"id"`
	Revision          string          `json:"revision"`
	Profile           string          `json:"profile"`
	SandboxTemplate   string          `json:"sandbox_template"`
	Instructions      string          `json:"instructions"`
	Services          []string        `json:"services"`
	RunTimeoutSeconds int             `json:"run_timeout_seconds"`
	MaxSessionTokens  int64           `json:"max_session_tokens"`
	MaxConcurrentRuns int             `json:"max_concurrent_runs"`
	Memory            json.RawMessage `json:"memory"`
}

// ResolveRole resolves one trusted reference at load time. Bindings cannot
// override the role, model, sandbox, limits or add services above its ceiling.
func ResolveRole(raw []byte, directory string) ([]byte, error) {
	if !strings.HasPrefix(string(raw), "---\n") {
		return raw, nil
	}
	header, body, ok := strings.Cut(string(raw[4:]), "\n---\n")
	if !ok {
		return raw, nil
	}
	var front map[string]any
	d := yaml.NewDecoder(strings.NewReader(header))
	if d.Decode(&front) != nil {
		return nil, errors.New("invalid binding header")
	}
	reference, exists := front["role_ref"]
	if !exists {
		return raw, nil
	}
	ref, ok := reference.(string)
	if !ok || ref == "" || strings.TrimSpace(body) != "" {
		return nil, errors.New("a role binding requires one reference and no copied instructions")
	}
	data, err := os.ReadFile(filepath.Join(directory, ref))
	if err != nil || len(data) > 2<<20 {
		return nil, errors.New("shared role unavailable")
	}
	var role Role
	if json.Unmarshal(data, &role) != nil || role.Schema != "corporate-agent.v1" || !plain(role.ID) || role.Instructions == "" || role.Profile == "" || role.SandboxTemplate == "" || len(role.Services) == 0 || role.RunTimeoutSeconds < 1 || role.MaxSessionTokens < 1 || role.MaxConcurrentRuns < 1 {
		return nil, errors.New("invalid shared role")
	}
	for _, key := range []string{"agent_id", "profile", "sandbox_template", "run_timeout_seconds", "max_session_tokens", "max_concurrent_runs"} {
		if _, present := front[key]; present {
			return nil, errors.New("binding cannot override shared role")
		}
	}
	if selected, present := front["services"]; present {
		services, ok := selected.([]any)
		if !ok {
			return nil, errors.New("invalid selected services")
		}
		for _, service := range services {
			s, ok := service.(string)
			if !ok || !slices.Contains(role.Services, s) {
				return nil, errors.New("service exceeds agent ceiling")
			}
		}
	} else {
		front["services"] = role.Services
	}
	delete(front, "role_ref")
	front["agent_id"] = role.ID
	front["profile"] = role.Profile
	front["sandbox_template"] = role.SandboxTemplate
	front["run_timeout_seconds"] = role.RunTimeoutSeconds
	front["max_session_tokens"] = role.MaxSessionTokens
	front["max_concurrent_runs"] = role.MaxConcurrentRuns
	front["revision"] = role.Revision
	resolved, err := yaml.Marshal(front)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(resolved) + "---\n" + role.Instructions + "\n"), nil
}
