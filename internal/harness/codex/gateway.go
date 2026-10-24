package codex

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/internal/harness"
	"github.com/orpheus-agents/orpheus/internal/session"
	"github.com/pelletier/go-toml/v2"
)

// PrepareLaunch is repeatable after a worker restart, including a lost rename
// response. It must run before the executor records sending for process Start.
func (d *Driver) PrepareLaunch(ctx context.Context, source session.Credentials) error {
	if source.Mode != "api_key" || source.BaseURL == "" {
		return nil
	}
	d.preparedGateway = ""
	ctx, cancel := context.WithTimeout(ctx, min(d.rpc.timeout, 30*time.Second))
	defer cancel()
	home, err := d.StateDir(ctx)
	if err != nil {
		return err
	}
	path := home + "/config.toml"
	config := map[string]any{}
	file, err := d.rpc.sandbox.Read(ctx, path)
	switch {
	case err == nil:
		const maxConfigBytes = 1024 * 1024
		raw, readErr := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(raw) > maxConfigBytes {
			return harness.Failure("environment_unavailable", "Codex configuration is unreadable or too large.")
		}
		if err := toml.Unmarshal(raw, &config); err != nil {
			return harness.Failure("environment_unavailable", "Codex configuration is invalid.")
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if !gatewayTemplateAllowed(config) {
		return harness.Failure("environment_unavailable", "Codex gateway template has conflicting provider or MCP credential settings.")
	}
	providers, ok := config["model_providers"].(map[string]any)
	if !ok {
		if config["model_providers"] != nil {
			return harness.Failure("environment_unavailable", "Codex providers configuration is invalid.")
		}
		providers = map[string]any{}
	}
	config["model_provider"] = "counsel_gateway"
	config["model_providers"] = providers
	// This reserved provider is owned entirely by Orpheus; retain all other
	// provider, MCP and template settings. Replace stale gateway auth options.
	providers["counsel_gateway"] = map[string]any{
		"name": "Counsel gateway", "base_url": source.BaseURL,
		"env_key": "COUNSEL_GATEWAY_KEY", "wire_api": "responses",
		"requires_openai_auth": false, "supports_websockets": false,
		"request_max_retries": 0, "stream_max_retries": 0,
	}
	raw, err := toml.Marshal(config)
	if err != nil {
		return harness.Failure("environment_unavailable", "Codex configuration cannot be encoded.")
	}
	temporary := home + "/.config.toml.orpheus-" + uuid.NewString()
	// Precreate with restrictive permissions before uploading template content.
	if _, err := d.rpc.sandbox.Run(ctx, "umask 077; touch -- "+harness.Quote(temporary)); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = d.rpc.sandbox.Run(cleanup, "rm -f -- "+harness.Quote(temporary))
	}()
	if err := d.rpc.sandbox.Write(ctx, temporary, raw); err != nil {
		return err
	}
	// Both paths share a directory: readers see either complete old or new TOML.
	if _, err := d.rpc.sandbox.Run(ctx, "set -eu; chmod 600 -- "+harness.Quote(temporary)+"; mv -f -- "+harness.Quote(temporary)+" "+harness.Quote(path)); err != nil {
		return err
	}
	d.preparedGateway = source.BaseURL
	return nil
}

// Gateway routing must not depend on which Codex version a template contains.
// MCP configuration may survive the merge, but must not reference its reserved key.
func gatewayTemplateAllowed(config map[string]any) bool {
	if _, present := config["profile"]; present {
		return false
	}
	profiles, _ := config["profiles"].(map[string]any)
	for _, value := range profiles {
		profile, _ := value.(map[string]any)
		if _, present := profile["model_provider"]; present {
			return false
		}
	}
	return !referencesGatewayKey(config["mcp_servers"])
}

func referencesGatewayKey(value any) bool {
	switch value := value.(type) {
	case string:
		return strings.Contains(value, "COUNSEL_GATEWAY_KEY")
	case map[string]any:
		for key, item := range value {
			if strings.Contains(key, "COUNSEL_GATEWAY_KEY") || referencesGatewayKey(item) {
				return true
			}
		}
	case []any:
		if slices.ContainsFunc(value, referencesGatewayKey) {
			return true
		}
	}
	return false
}
