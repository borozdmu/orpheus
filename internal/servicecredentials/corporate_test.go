package servicecredentials

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/agentcontract"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestCorporateIssueRenewReconfirmAndRevoke(t *testing.T) {
	key := strings.Repeat("k", 32)
	t.Setenv("TEST_CORPORATE_KEY", key)
	reader := agentcontract.Principal{Namespace: "mm:test", ID: "a", PrincipalID: "1"}
	task := agentcontract.Assignment{Schema: agentcontract.Version, InvocationID: uuid.New(), Agent: "mark", Author: reader, Readers: []agentcontract.Principal{reader}, ProviderContext: map[string]json.RawMessage{"agent_id": json.RawMessage(`"mark"`)}, Text: "private text must not reach provider", Audience: agentcontract.Audience{Kind: "exact", Instance: "mm-test", ChannelID: "c", ChannelType: "P", Complete: true}, Delivery: agentcontract.Route{Connector: "mattermost", Source: "source", Chat: "c", Message: "m"}, Confirmation: agentcontract.Confirmation{ObservedAt: time.Now().UTC(), Revision: "role-1"}}
	if err := task.Seal(key); err != nil {
		t.Fatal(err)
	}
	var denied atomic.Bool
	var confirmations atomic.Int32
	var issues atomic.Int32
	var revokeIDs []string
	witness := agentcontract.Handler(func() string { return key }, func(context.Context, agentcontract.Assignment) error {
		confirmations.Add(1)
		if denied.Load() {
			return errors.New("card removed")
		}
		return nil
	})
	grant := testGrant()
	mux := http.NewServeMux()
	mux.Handle("/audience", witness)
	mux.HandleFunc("/provider", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), task.Text) || strings.Contains(string(raw), "attachments") || strings.Contains(string(raw), "delivery") {
			t.Error("private task data sent to provider")
		}
		var op string
		_ = json.Unmarshal(body["operation"], &op)
		if op == "issue_assignment" {
			issues.Add(1)
		}
		if op == "revoke" {
			var id string
			_ = json.Unmarshal(body["request_id"], &id)
			revokeIDs = append(revokeIDs, id)
			_, _ = w.Write([]byte(`{"revoked":true}`))
			return
		}
		_ = json.NewEncoder(w).Encode(grant)
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	cfg := Config{Protocol: agentcontract.Version, Endpoint: server.URL + "/provider", SecretEnv: "TEST_CORPORATE_KEY", AudienceEndpoint: server.URL + "/audience", AudienceSecretEnv: "TEST_CORPORATE_KEY", TokenFile: "~/.kord/run.token"}
	c := Client{HTTP: server.Client()}
	raw, _ := json.Marshal(task)
	a := session.AccessContext{Assignment: new(json.RawMessage(raw)), InvocationID: task.InvocationID, Subject: session.AccessSubject{Namespace: reader.Namespace, ID: reader.ID}, ScopeContext: session.AccessScopeContext{}, ContextObservedAt: task.Confirmation.ObservedAt}
	got, err := c.Issue(t.Context(), cfg, "kord", a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.RenewConfirmed(t.Context(), cfg, "kord", a, got); err != nil {
		t.Fatal(err)
	}
	denied.Store(true)
	if _, err = c.RenewConfirmed(t.Context(), cfg, "kord", a, got); err == nil {
		t.Fatal("renewed after account card denial")
	}
	if issues.Load() != 1 || confirmations.Load() != 3 {
		t.Fatal("incorrect confirmation lifecycle")
	}
	for range 2 {
		if err = c.Revoke(t.Context(), cfg, "kord", a.InvocationID, got); err != nil {
			t.Fatal(err)
		}
	}
	if len(revokeIDs) != 2 || revokeIDs[0] != revokeIDs[1] {
		t.Fatal("revocation is not idempotent")
	}
	cfg.AudienceEndpoint = "http://untrusted.invalid"
	before := confirmations.Load()
	if c.Confirm(t.Context(), cfg, a) == nil || confirmations.Load() != before {
		t.Fatal("invalid configuration reached connector")
	}
}
