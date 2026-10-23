//go:build integration

package worker

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/secret"
	"github.com/orpheus-agents/orpheus/internal/session"
	"github.com/orpheus-agents/orpheus/internal/store"
	"github.com/orpheus-agents/orpheus/internal/testutil"
)

func TestMaskedMetadataDoesNotReachHarness(t *testing.T) {
	masker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		for i := range in.Texts {
			in.Texts[i] = "[PERSON_test]"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"masked_texts": in.Texts})
	}))
	defer masker.Close()
	c, _ := secret.New(base64.URLEncoding.EncodeToString(make([]byte, 32)))
	s := &store.Store{Pool: testutil.Database(t), Cipher: c, Settings: config.DefaultSettings(), Profiles: config.Profiles{InputMasker: config.InputMasker{URL: masker.URL, TimeoutSeconds: 2}, Templates: map[string]config.Template{"codex": {}}, Profiles: map[string]config.Profile{"p": {MaskInput: true, Harness: "codex", Model: new("model"), Auth: config.Auth{Mode: "api_key", APIKeyEnv: "KEY"}}}}}
	a, err := s.Accept(t.Context(), store.Admission{Key: uuid.New(), Create: &session.CreateSession{AllowMultipleRuns: true, Configuration: session.ConfigurationInput{Agent: session.AgentInput{Profile: "p"}, Sandbox: session.SandboxInput{Template: "codex"}, Limits: session.Limits{RunTimeoutSeconds: 3600}}, Messages: []session.TextMessage{{Text: "private name", Metadata: json.RawMessage(`{"private":"private metadata"}`)}}}})
	if err != nil {
		t.Fatal(err)
	}
	r := &remote{}
	e := executor(a.SessionID, s, r)
	t.Cleanup(e.Disconnect)
	tick(t, e)
	if len(r.turns) != 1 || len(r.turns[0].Items) != 1 || r.turns[0].Items[0].Text != "[PERSON_test]" {
		t.Fatal("metadata or original reached initial harness")
	}
	m, err := store.GetMessage(t.Context(), s.Pool, a.MessageID)
	if err != nil || string(m.Metadata) != `{"private": "private metadata"}` {
		t.Fatal("trusted-side metadata changed", err)
	}
	_, err = s.Accept(t.Context(), store.Admission{SessionID: a.SessionID, RunID: a.RunID, Key: uuid.New(), Messages: []session.TextMessage{{Text: "another private name", Metadata: json.RawMessage(`{"private":"steer metadata"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, e)
	if len(r.turns[0].Items) != 2 || r.turns[0].Items[1].Text != "[PERSON_test]" {
		t.Fatal("metadata or original reached steer harness")
	}
	// Only the in-memory fake remote is used; no AgentBox or model network call.
}
