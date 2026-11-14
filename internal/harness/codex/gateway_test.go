package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orpheus-agents/orpheus/internal/session"
	"github.com/pelletier/go-toml/v2"
)

func TestGatewayNeverLogsInWithProviderKey(t *testing.T) {
	t.Setenv("GATEWAY_SOURCE", "synthetic-gateway-key")
	t.Setenv("OPENAI_API_KEY", "synthetic-provider-key")
	box := gatewayFilesystem(t)
	d := New(box, time.Second, 1024)
	defer func() { _ = d.Close() }()
	source := session.Credentials{Mode: "api_key", APIKeyEnv: "GATEWAY_SOURCE", BaseURL: "https://gateway.example/random/v1"}
	if err := d.PrepareLaunch(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"OTHER": "retained"}
	if _, err := d.Launch(t.Context(), env, "/workspace", source); err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || box.startEnv["COUNSEL_GATEWAY_KEY"] != "synthetic-gateway-key" || box.startEnv["OTHER"] != "retained" {
		t.Fatal("wrong launch environment")
	}
	if strings.Contains(box.startCommand, "synthetic-") || strings.Contains(box.startCommand, source.BaseURL) || strings.Contains(box.startCommand, "forced_login_method") {
		t.Fatal("sensitive or login option in argv")
	}
	var cfg struct {
		ModelProvider string `toml:"model_provider"`
		Providers     map[string]struct {
			BaseURL       string `toml:"base_url"`
			EnvKey        string `toml:"env_key"`
			WireAPI       string `toml:"wire_api"`
			Auth          bool   `toml:"requires_openai_auth"`
			WS            bool   `toml:"supports_websockets"`
			Retries       int    `toml:"request_max_retries"`
			StreamRetries int    `toml:"stream_max_retries"`
		} `toml:"model_providers"`
	}
	raw, err := os.ReadFile(filepath.Join(box.home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers[cfg.ModelProvider]
	if cfg.ModelProvider != "counsel_gateway" || p.BaseURL != source.BaseURL || p.EnvKey != "COUNSEL_GATEWAY_KEY" || p.WireAPI != "responses" || p.Auth || p.WS || p.Retries != 0 || p.StreamRetries != 0 {
		t.Fatal("wrong custom provider")
	}
	for _, value := range box.startEnv {
		if value == "synthetic-provider-key" {
			t.Fatal("provider key leaked to env")
		}
	}
	for _, value := range box.files {
		if strings.Contains(string(value), "synthetic-") {
			t.Fatal("key leaked to files")
		}
	}
	logins, reads := 0, 0
	rpc, _ := newRPC(t, func(req map[string]json.RawMessage) any {
		var method string
		_ = json.Unmarshal(req["method"], &method)
		if method == "initialized" {
			return nil
		}
		if method == "account/login/start" {
			logins++
		}
		if method == "account/read" {
			reads++
		}
		return map[string]any{"id": req["id"], "result": map[string]any{}}
	})
	defer func() { _ = rpc.Close() }()
	d.rpc = rpc
	for _, login := range []bool{true, false} {
		if err := d.Initialize(t.Context(), source, login); err != nil {
			t.Fatal(err)
		}
	}
	if logins != 0 || reads != 0 {
		t.Fatal("custom provider used account RPC")
	}
}

func TestGatewayMissingKeyDoesNotLaunch(t *testing.T) {
	t.Setenv("GATEWAY_SOURCE", "")
	box := &testBox{}
	_, err := New(box, time.Second, 1024).Launch(t.Context(), nil, "/workspace", session.Credentials{Mode: "api_key", APIKeyEnv: "GATEWAY_SOURCE", BaseURL: "https://gateway.example/v1"})
	if err == nil || box.startCommand != "" {
		t.Fatal("missing gateway credential allowed launch")
	}
}
