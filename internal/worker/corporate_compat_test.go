//go:build integration

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/agentcontract"
	public "github.com/orpheus-agents/orpheus/client"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/httpserver"
	"github.com/orpheus-agents/orpheus/internal/servicecredentials"
	"github.com/orpheus-agents/orpheus/internal/session"
)

// TestCorporateCompatibility consumes actual native request bodies exported by
// both connector engines, a real Provider+SQLite and a real Orpheus+PostgreSQL.
// Sandbox and model are the existing deterministic worker simulator.
func TestCorporateCompatibility(t *testing.T) {
	directory, providerURL := os.Getenv("CORPORATE_FIXTURES_DIR"), os.Getenv("CORPORATE_PROVIDER_URL")
	if directory == "" || providerURL == "" {
		t.Skip("manifest compatibility runner supplies fixtures and synthetic Provider")
	}
	for _, transport := range []string{"telegram", "mattermost", "mattermost-public"} {
		t.Run(transport, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(directory, transport+"-request.json"))
			if err != nil {
				t.Fatal(err)
			}
			var req public.CreateSession
			if json.Unmarshal(raw, &req) != nil || req.AccessContext == nil || req.AccessContext.Assignment == nil {
				t.Fatal("connector request missing access context")
			}
			task, err := agentcontract.Decode(*req.AccessContext.Assignment)
			if err != nil {
				t.Fatal(err)
			}
			if task.Delivery.Connector != strings.TrimSuffix(transport, "-public") || task.Author.ID != req.AccessContext.Subject.ID {
				t.Fatal("transport contract differs")
			}
			s, _, _, unused := setup(t)
			unused.Disconnect()
			t.Setenv("CORPORATE_PROVIDER_KEY", "synthetic-core-provider-key-000000000000")
			t.Setenv("CORPORATE_AUDIENCE_KEY", strings.Repeat("a", 32))
			target, err := url.Parse(providerURL)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.Handle("/api/v1/agent-credentials", httputil.NewSingleHostReverseProxy(target))
			mux.Handle("/audience", agentcontract.Handler(func() string { return strings.Repeat("a", 32) }, func(_ context.Context, a agentcontract.Assignment) error {
				if a.InvocationID != task.InvocationID || a.Digest() != task.Digest() {
					return errors.New("unknown frozen assignment")
				}
				return nil
			}))
			authority := httptest.NewTLSServer(mux)
			defer authority.Close()
			cfg := servicecredentials.Config{Protocol: agentcontract.Version, Endpoint: authority.URL + "/api/v1/agent-credentials", SecretEnv: "CORPORATE_PROVIDER_KEY", AudienceEndpoint: authority.URL + "/audience", AudienceSecretEnv: "CORPORATE_AUDIENCE_KEY", TokenFile: "~/.kord/run.token"}
			s.CredentialClient = servicecredentials.Client{HTTP: authority.Client()}
			s.Profiles.Services = map[string]config.Service{"kord": {Name: "Kord", Description: "Synthetic meeting intersection", Provider: &cfg}}
			s.Profiles.Templates[req.Configuration.Sandbox.Template] = config.Template{}
			s.Profiles.Profiles[req.Configuration.Agent.Profile] = config.Profile{Harness: "codex", Model: new("model"), Auth: config.Auth{Mode: "api_key", APIKeyEnv: "KEY"}}
			// Isolate protected service admission from unrelated fixture services/hooks.
			req.Configuration.Sandbox.Services = new([]string{"kord"})
			req.Configuration.Hooks = nil
			s.Settings.PublicAPIKeys = []string{"synthetic-client"}
			handler, err := httpserver.Handler(s, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			apiServer := httptest.NewServer(handler)
			defer apiServer.Close()
			payload, _ := json.Marshal(req)
			call, err := http.NewRequestWithContext(t.Context(), "POST", apiServer.URL+"/api/v1/sessions", bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			call.Header.Set("Authorization", "Bearer synthetic-client")
			call.Header.Set("Content-Type", "application/json")
			call.Header.Set("Idempotency-Key", uuid.NewString())
			response, err := http.DefaultClient.Do(call)
			if err != nil {
				t.Fatal(err)
			}
			answer, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 202 {
				t.Fatalf("admission status=%d body=%s", response.StatusCode, answer)
			}
			var accepted public.Accepted
			if json.Unmarshal(answer, &accepted) != nil {
				t.Fatal("invalid acceptance")
			}
			state, err := s.ServiceCredentials(t.Context(), accepted.SessionID, accepted.RunID)
			if err != nil || len(state.Entries) != 1 {
				t.Fatal("run credential not persisted", err)
			}
			token := state.Entries["kord"].Grant.AccessToken
			authorize := func() int {
				b, _ := json.Marshal(map[string]string{"token": token})
				r, err := http.Post(providerURL+"/authorize", "application/json", bytes.NewReader(b))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = r.Body.Close() }()
				if r.StatusCode == 200 {
					var grant struct {
						Scope struct {
							Meetings []int `json:"meeting_ids"`
						} `json:"scope"`
					}
					if json.NewDecoder(r.Body).Decode(&grant) != nil || len(grant.Scope.Meetings) != 1 || grant.Scope.Meetings[0] != 1 {
						t.Fatal("meeting intersection did not exclude private meeting")
					}
				}
				return r.StatusCode
			}
			if authorize() != 200 {
				t.Fatal("shared meeting not authorized")
			}
			remote := &remote{}
			remote.credentialCommands = func(command string) ([]byte, error) {
				if strings.Contains(command, "mktemp") {
					return []byte("/home/template/.kord/.run.fixture\n"), nil
				}
				return []byte("/home/template/workspace"), nil
			}
			installed := false
			remote.credentialWrite = func(path string, data []byte) error {
				if strings.HasSuffix(path, "run.token") {
					installed = string(data) == token
				}
				return nil
			}
			executor := executor(accepted.SessionID, s, remote)
			defer executor.Disconnect()
			tick(t, executor)
			if !installed || remote.starts != 1 {
				t.Fatal("credential was not installed before model start")
			}
			complete(remote)
			tick(t, executor)
			tick(t, executor)
			view, err := s.Session(t.Context(), accepted.SessionID)
			if err != nil || view.Status != session.Completed || view.FinalMessage == nil {
				t.Fatal("run did not complete", err)
			}
			if err = s.RevokeTerminalCredentials(t.Context()); err != nil {
				t.Fatal(err)
			}
			if authorize() != 403 {
				t.Fatal("completed run retained usable access")
			}
		})
	}
}
