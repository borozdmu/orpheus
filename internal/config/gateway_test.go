package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestGatewayProfile(t *testing.T) {
	const prefix = "[templates.codex]\n[profiles.p]\nharness='codex'\nmodel='m'\n[profiles.p.auth]\nmode='api_key'\napi_key_env='GATEWAY_KEY'\n"
	for _, address := range []string{"https://gateway.example/random/v1", "https://gateway.example:443/v1"} {
		p, err := ReadProfiles(strings.NewReader(prefix + "base_url='" + address + "'\n"))
		if err != nil {
			t.Fatal(err)
		}
		in := session.ConfigurationInput{Agent: session.AgentInput{Profile: "p"}, Sandbox: session.SandboxInput{Template: "codex"}, Limits: session.Limits{RunTimeoutSeconds: 60}}
		out, err := Resolve(in, p, nil, DefaultMaxSessionTokens)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip session.ResolvedConfiguration
		if err := json.Unmarshal(raw, &roundtrip); err != nil {
			t.Fatal(err)
		}
		if roundtrip.Credentials.BaseURL != address {
			t.Fatal("gateway address lost")
		}
	}
	for _, address := range []string{"", "http://gateway.example/v1", "https:///v1", "//gateway.example/v1", "https://user:pass@gateway.example/v1", "https://gateway.example/v1?q=a", "https://gateway.example/v1?", "https://gateway.example/v1#fragment", "https://gateway.example/v1#"} {
		if _, err := ReadProfiles(strings.NewReader(prefix + "base_url='" + address + "'\n")); err == nil {
			t.Fatalf("accepted invalid URL %q", address)
		}
	}
	const account = "[templates.codex]\n[credential_stores.s]\nbucket='b'\n[profiles.p]\nharness='codex'\n[profiles.p.auth]\nmode='account'\naccount_id='a'\nstore='s'\nkey='k'\n"
	for _, address := range []string{"", "https://gateway.example/v1"} {
		if _, err := ReadProfiles(strings.NewReader(account + "base_url='" + address + "'\n")); err == nil {
			t.Fatal("account accepted base_url")
		}
	}
	var old session.Credentials
	if err := json.Unmarshal([]byte(`{"mode":"api_key","api_key_env":"KEY","store":null,"key":""}`), &old); err != nil || old.BaseURL != "" {
		t.Fatal("old snapshot broken", err)
	}
	for _, in := range []session.SandboxInput{
		{Template: "codex", Env: map[string]string{"COUNSEL_GATEWAY_KEY": "untrusted"}},
		{Template: "codex", EnvFrom: []string{"COUNSEL_GATEWAY_KEY"}},
	} {
		if err := ValidateSandbox(in); err == nil {
			t.Fatal("session can override internal gateway credential")
		}
		if err := ValidateRunEnvironment(in.Env, in.EnvFrom, []string{"COUNSEL_GATEWAY_KEY"}); err == nil {
			t.Fatal("run can override internal gateway credential")
		}
	}
}
