//go:build integration

package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/secret"
	"github.com/orpheus-agents/orpheus/internal/session"
	"github.com/orpheus-agents/orpheus/internal/store"
	"github.com/orpheus-agents/orpheus/internal/testutil"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T) (queue, store.Admission) {
	t.Helper()
	cipher, err := secret.New(base64.URLEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	w := workflow{Namespace: "cxagents-v12", Profile: "v12", Template: "v12-runtime", Backend: "cxagent-unix-v1", Timeout: 300, MaxTokens: 1000000, Capacity: 4}
	s := &store.Store{Pool: testutil.Database(t), Cipher: cipher, Settings: config.DefaultSettings(),
		Profiles: config.Profiles{Templates: map[string]config.Template{w.Template: {}},
			Profiles: map[string]config.Profile{w.Profile: {Harness: "codex", Model: new("managed"), Auth: config.Auth{Mode: "api_key", APIKeyEnv: "EXTERNAL_KEY"}}}}}
	id := strings.Repeat("c", 64)
	req := store.Admission{Key: uuid.New(), Create: &session.CreateSession{Namespace: &w.Namespace, ExternalKey: &id,
		InputFingerprint: new(strings.Repeat("a", 64)), Messages: []session.TextMessage{{Text: id}},
		Configuration: session.ConfigurationInput{Agent: session.AgentInput{Profile: w.Profile},
			Sandbox: session.SandboxInput{Template: w.Template}, Limits: session.Limits{RunTimeoutSeconds: w.Timeout, MaxSessionTokens: w.MaxTokens}}}}
	return queue{s: s, workflow: w}, req
}
func setBackend(q *queue, handler http.HandlerFunc) {
	q.backend = backend{client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, r)
		return res.Result(), nil
	})}}
}
func TestPaidAdmissionReplayAndRecovery(t *testing.T) {
	q, req := fixture(t)
	ctx := t.Context()
	first, err := q.s.Accept(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := q.s.Accept(ctx, req)
	if err != nil || replay != first {
		t.Fatalf("admission replay %v %v", replay, err)
	}
	id := *req.Create.ExternalKey
	post, paid := 0, 0
	var intent binding
	setBackend(&q, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/executions/"+id {
			t.Fatal(r.URL.Path)
		}
		if r.Method == "POST" {
			post++
			var b binding
			if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
				t.Fatal(err)
			}
			if post == 1 {
				intent = b
				paid++
				w.WriteHeader(503)
				return
			}
			if b != intent || intent.Run != first.RunID || intent.Session != first.SessionID {
				t.Fatalf("dispatch rebound: %+v", b)
			}
			_ = json.NewEncoder(w).Encode(receipt{ID: id, State: "running"})
			return
		}
		if r.Header.Get("X-CX-Fingerprint") != intent.Fingerprint || r.Header.Get("X-CX-Run-ID") != first.RunID.String() {
			t.Fatal("lost binding")
		}
		_ = json.NewEncoder(w).Encode(receipt{ID: id, State: "done", Usage: &counters{new(int64(20)), new(int64(7)), new(int64(9)), new(int64(4))}})
	})
	if err := q.tick(ctx, first.SessionID); err == nil {
		t.Fatal("missing lost response")
	}
	if err := q.tick(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := q.tick(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := q.tick(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	}
	view, err := q.s.Session(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != session.Completed || view.FinalMessage == nil || view.FinalMessage.Text != id ||
		view.Usage.InputTokens != 20 || view.Usage.CachedInputTokens != 9 || view.Usage.OutputTokens != 7 || view.Usage.ReasoningOutputTokens != 4 ||
		paid != 1 || post != 2 {
		t.Fatalf("receipt mismatch: %+v, calls %d/%d", view, post, paid)
	}
	left, err := q.s.Reserved(ctx)
	if err != nil || len(left) != 0 {
		t.Fatal(left, err)
	}
}

func TestCancellationBeforeAndAfterDispatch(t *testing.T) {
	for _, before := range []bool{true, false} {
		q, req := fixture(t)
		ctx := t.Context()
		a, err := q.s.Accept(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		setBackend(&q, func(w http.ResponseWriter, r *http.Request) {
			calls++
			state := "running"
			if strings.HasSuffix(r.URL.Path, "/cancel") {
				state = "cancelled"
			}
			_ = json.NewEncoder(w).Encode(receipt{ID: *req.Create.ExternalKey, State: state})
		})
		if !before {
			if err := q.tick(ctx, a.SessionID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := q.s.Cancel(ctx, a.SessionID, a.RunID); err != nil {
			t.Fatal(err)
		}
		if err := q.tick(ctx, a.SessionID); err != nil {
			t.Fatal(err)
		}
		view, err := q.s.Session(ctx, a.SessionID)
		if err != nil || view.Status != session.Cancelled || before && calls != 0 || !before && calls != 2 {
			t.Fatal(view.Status, calls, err)
		}
	}
}

func TestRejectsWrongWorkflowAndHoldsUnknown(t *testing.T) {
	q, req := fixture(t)
	ctx := t.Context()
	req.Create.Namespace = new("someone-else")
	a, err := q.s.Accept(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.tick(ctx, a.SessionID); err != nil {
		t.Fatal(err)
	}
	v, err := q.s.Session(ctx, a.SessionID)
	if err != nil || v.Status != session.Failed {
		t.Fatal(v, err)
	}
	req.Key = uuid.New()
	req.Create.Namespace = &q.workflow.Namespace
	a, err = q.s.Accept(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	setBackend(&q, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		_, _ = io.WriteString(w, `{"state":"unknown","id":"`+*req.Create.ExternalKey+`"}`)
	})
	for range 2 {
		if err := q.tick(ctx, a.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.s.Cancel(ctx, a.SessionID, a.RunID); err != nil {
		t.Fatal(err)
	}
	if err := q.tick(ctx, a.SessionID); err != nil {
		t.Fatal(err)
	}
	v, err = q.s.Session(ctx, a.SessionID)
	if err != nil || v.Status != session.Failed || v.Error.Code != "external_execution_outcome_unknown" || strings.Join(methods, ",") != "POST,GET,POST" {
		t.Fatal(v, methods, err)
	}
}
