package servicecredentials

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/agentcontract"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestSourceReadBindsOwnerMaterialAndExactCurrentDecision(t *testing.T) {
	key := strings.Repeat("s", 40)
	reader := agentcontract.Principal{Namespace: "mm:test", ID: "owner", PrincipalID: "1"}
	a := agentcontract.Assignment{Schema: agentcontract.Version, InvocationID: uuid.New(), Agent: "mark",
		Author: reader, Readers: []agentcontract.Principal{reader}, Text: "original assignment",
		ProviderContext: map[string]json.RawMessage{"agent_id": json.RawMessage(`"mark"`)},
		Audience:        agentcontract.Audience{Kind: "exact", Instance: "mm-test", ChannelID: "c", ChannelType: "P", Complete: true},
		Delivery:        agentcontract.Route{Connector: "mattermost", Source: "source", Chat: "c", Message: "m"},
		Confirmation:    agentcontract.Confirmation{ObservedAt: time.Now().UTC(), Revision: "role-1"}}
	if err := a.Seal(key); err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(a)
	msg := json.RawMessage(stored)
	access := session.AccessContext{Assignment: &msg, InvocationID: a.InvocationID, Subject: session.AccessSubject{Namespace: reader.Namespace, ID: reader.ID}}
	r := SourceRead{Request: uuid.New(), Invocation: a.InvocationID, Assignment: a.Confirmation.Digest, Job: "job", Artifact: strings.Repeat("a", 64), Source: "my", Revision: "r1", Purpose: "publish"}
	sign := func(r SourceRead) SourceRead {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write(r.material())
		r.Signature = hex.EncodeToString(mac.Sum(nil))
		return r
	}
	r = sign(r)
	allowed, mismatch, calls := true, false, 0
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["operation"] != "check_assignment" || body["credential_id"] != "credential" || body["assignment_digest"] != r.Assignment {
			t.Error("wrong stored grant")
		}
		if len(body) != 7 {
			t.Error("unexpected caller data sent to issuer")
		}
		echo := r.Request
		if mismatch {
			echo = uuid.New()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": echo, "invocation_id": r.Invocation, "assignment_digest": r.Assignment,
			"check_sha256": sourceCheck{"mcp_source_read", "my", "r1"}.digest(), "allowed": allowed})
	}))
	defer provider.Close()
	cfg := Config{Protocol: agentcontract.Version, Endpoint: provider.URL, SecretEnv: "ISSUER", AudienceConnectors: map[string]AudienceConnector{"mattermost/source": {Endpoint: provider.URL + "/audience", SecretEnv: "OWNER"}}, TokenFile: "~/.kord/run.token"}
	entry := Entry{Config: cfg, Grant: Grant{CredentialID: "credential"}}
	client := Client{HTTP: provider.Client(), LookupSecret: func(n string) string {
		if n == "OWNER" {
			return key
		}
		return strings.Repeat("p", 40)
	}}
	check := func(input SourceRead) (bool, error) {
		return client.CheckSourceRead(context.Background(), entry, access, input)
	}
	if ok, err := check(r); err != nil || !ok {
		t.Fatalf("valid source decision: %v", err)
	}
	for _, edit := range []func(*SourceRead){
		func(q *SourceRead) { q.Job = "other" }, func(q *SourceRead) { q.Purpose = "restore" },
		func(q *SourceRead) { q.Artifact = strings.Repeat("b", 64) }, func(q *SourceRead) { q.Source = "other" },
		func(q *SourceRead) { q.Revision = "r2" }, func(q *SourceRead) { q.Invocation = uuid.New() },
		func(q *SourceRead) { q.Request = uuid.New() }, func(q *SourceRead) { q.Assignment = strings.Repeat("b", 64) },
		func(q *SourceRead) { q.Signature = strings.Repeat("b", 64) },
	} {
		input := r
		edit(&input)
		if ok, err := check(input); err == nil || ok {
			t.Fatal("unbound check allowed")
		}
	}
	if calls != 1 {
		t.Fatalf("invalid requests reached issuer: %d", calls)
	}
	allowed = false
	if ok, err := check(r); ok || err != nil {
		t.Fatalf("source revoked: %v", err)
	}
	allowed = true
	mismatch = true
	if ok, err := check(r); ok || err == nil {
		t.Fatal("mismatched decision was accepted")
	}
}

func TestProviderCanonicalSourceCheck(t *testing.T) {
	check := sourceCheck{"mcp_source_read", "my", "ревизия🚀<&>"}
	// Reproduce the issuer's json.dumps(sort_keys=True,separators=(",",":")).
	canonical := `{"kind":"mcp_source_read","source_id":"my","source_revision":"\u0440\u0435\u0432\u0438\u0437\u0438\u044f\ud83d\ude80<&>"}`
	sum := sha256.Sum256([]byte(canonical))
	if check.digest() != hex.EncodeToString(sum[:]) {
		t.Fatal("source check digest disagrees with provider")
	}
}
