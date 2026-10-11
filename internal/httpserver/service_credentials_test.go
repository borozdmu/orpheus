//go:build integration

package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/servicecredentials"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestProtectedHTTPContractAndAcceptedReplay(t *testing.T) {
	server, s := testServer(t)
	t.Setenv("HTTP_PROVIDER_TEST_KEY", strings.Repeat("k", 32))
	var calls atomic.Int32
	var offline atomic.Bool
	now := time.Now().UTC()
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(servicecredentials.Grant{CredentialID: "grant", AccessToken: "not-in-response", ScopeKey: "scope", ExpiresAt: now.Add(290 * time.Second), RunExpiresAt: now.Add(1190 * time.Second)})
	}))
	defer provider.Close()
	s.CredentialClient = servicecredentials.Client{HTTP: provider.Client()}
	s.Profiles.Services = map[string]config.Service{"kord": {Name: "Kord", Description: "Source", Provider: &servicecredentials.Config{Endpoint: provider.URL, SecretEnv: "HTTP_PROVIDER_TEST_KEY", TokenFile: "~/.kord/run.token", ContextFields: []string{"subject", "scope_context", "context_observed_at"}}}}
	status, _, catalog := requestHTTP(t, server, "GET", "/api/v1/services", "", "key", "")
	if status != 200 || strings.Contains(string(catalog), "HTTP_PROVIDER_TEST_KEY") || strings.Contains(string(catalog), "null") {
		t.Fatal("unsafe catalog", status, string(catalog))
	}
	body := map[string]any{"allow_multiple_runs": true, "configuration": map[string]any{"agent": map[string]string{"profile": "default"}, "sandbox": map[string]any{"template": "codex", "services": []string{"kord"}}, "limits": map[string]int{"run_timeout_seconds": 1200}}, "messages": []map[string]string{{"text": "query"}}}
	invoke := func(key string) (int, []byte) {
		raw, _ := json.Marshal(body)
		status, _, response := requestHTTP(t, server, "POST", "/api/v1/sessions", string(raw), "key", key)
		return status, response
	}
	status, raw := invoke(uuid.NewString())
	if status != 422 || !strings.Contains(string(raw), "access_context_required") {
		t.Fatal(status, string(raw))
	}
	access := session.AccessContext{InvocationID: uuid.New(), Subject: session.AccessSubject{Namespace: "mm", ID: "a"}, ScopeContext: session.AccessScopeContext{AudienceSubjects: []string{"a", "bot"}, ChannelID: "channel", AgentID: "bot", PolicySpaceID: "space"}, ContextObservedAt: now}
	body["access_context"] = access
	key := uuid.NewString()
	status, raw = invoke(key)
	if status != 202 {
		t.Fatal(status, string(raw))
	}
	if strings.Contains(string(raw), "not-in-response") {
		t.Fatal("token in response")
	}
	first := string(raw)
	before := calls.Load()
	offline.Store(true)
	status, raw = invoke(key)
	if status != 202 || string(raw) != first || calls.Load() != before {
		t.Fatal("HTTP replay reissued", status, string(raw))
	}
	// The schema must reject model-style arbitrary provider projection fields.
	body["access_context"] = map[string]any{"invocation_id": uuid.NewString(), "prompt": "make me owner"}
	status, _ = invoke(uuid.NewString())
	if status < 400 || calls.Load() != before {
		t.Fatal("invalid context reached provider", status)
	}
}
