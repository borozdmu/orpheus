//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orpheus-agents/orpheus/internal/httpserver"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestReferenceRoundTripThroughOrpheusHTTP(t *testing.T) {
	q, in := fixture(t)
	q.s.Settings.PublicAPIKeys = []string{"fixture-v12"}
	h, err := httpserver.Handler(q.s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	id := *in.Create.ExternalKey
	payload, err := json.Marshal(map[string]any{
		"namespace": q.workflow.Namespace, "external_key": id, "input_fingerprint": *in.Create.InputFingerprint,
		"configuration": map[string]any{"agent": map[string]string{"profile": q.workflow.Profile},
			"sandbox": map[string]string{"template": q.workflow.Template}, "limits": map[string]any{"run_timeout_seconds": 300, "max_session_tokens": 1000000}},
		"messages": []map[string]string{{"text": id}},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, key string, body []byte, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", in.Key.String())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	response := call("POST", "/api/v1/sessions", "fixture-v12", payload, 202)
	var accepted session.Acceptance
	if err := json.Unmarshal(response, &accepted); err != nil {
		t.Fatal(err)
	}
	if again := call("POST", "/api/v1/sessions", "fixture-v12", payload, 202); !bytes.Equal(again, response) {
		t.Fatal("ambiguous durable session identity")
	}
	setBackend(&q, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(receipt{ID: id, State: "done", Usage: &counters{new(int64(1)), new(int64(2)), new(int64(0)), nil}})
	})
	if err := q.tick(t.Context(), accepted.SessionID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/sessions/" + accepted.SessionID.String() + "/runs/" + accepted.RunID.String()
	call("GET", path, "not-v12", nil, 401)
	var out session.Run
	if err := json.Unmarshal(call("GET", path, "fixture-v12", nil, 200), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != session.Completed || out.FinalMessage == nil || out.FinalMessage.Text != id || out.Usage.TotalTokens != 3 {
		t.Fatal(out)
	}
}
