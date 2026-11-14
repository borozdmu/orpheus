//go:build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestAdmissionValidatesBeforeSessionLookupOrMasker(t *testing.T) {
	s := fixture(t)
	var calls atomic.Int32
	masker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer masker.Close()
	s.Profiles.InputMasker = config.InputMasker{URL: masker.URL, TimeoutSeconds: 1}
	profile := s.Profiles.Profiles["default"]
	profile.MaskInput = true
	s.Profiles.Profiles["default"] = profile
	// A real protected session is created with a successful deterministic stub.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"masked_texts": in.Texts})
	}))
	defer good.Close()
	s.Profiles.InputMasker.URL = good.URL
	accepted, err := s.Accept(t.Context(), request())
	if err != nil {
		t.Fatal(err)
	}
	s.Profiles.InputMasker.URL = masker.URL
	for _, sid := range []uuid.UUID{uuid.New(), accepted.SessionID} {
		for _, rid := range []uuid.UUID{uuid.Nil, uuid.New()} {
			for _, messages := range [][]session.TextMessage{
				nil, make([]session.TextMessage, 257),
				{{Text: " "}}, {{Text: "bad\x00text"}},
				{{Text: "valid", Metadata: json.RawMessage(`[]`)}},
				{{Text: "valid", Metadata: json.RawMessage(`null`)}},
				{{Text: "valid", Metadata: json.RawMessage(`{`)}},
			} {
				_, err := s.Accept(t.Context(), Admission{Key: uuid.New(), SessionID: sid, RunID: rid, Messages: messages})
				problem, ok := errors.AsType[*session.APIError](err)
				if !ok || problem.Status != 422 || problem.Problem.Code != "validation_error" {
					t.Fatalf("invalid input must fail before session/masker: %v", err)
				}
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached masker")
	}
	// Validation also precedes any SQL in a plain Store with no connection pool.
	_, err = (&Store{}).Accept(t.Context(), Admission{SessionID: uuid.New(), Key: uuid.New()})
	if problem, ok := errors.AsType[*session.APIError](err); !ok || problem.Status != 422 {
		t.Fatal("invalid ordinary request performed SQL", err)
	}
}

type sessionPolicyReadTrace struct{ reads atomic.Int32 }

func (tr *sessionPolicyReadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "-- name: GetSession :one\n") {
		tr.reads.Add(1)
	}
	return ctx
}
func (*sessionPolicyReadTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestAdmissionReusesSessionPolicyRead(t *testing.T) {
	s := fixture(t)
	a, err := s.Accept(t.Context(), request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(t.Context(), a.SessionID, a.RunID); err != nil {
		t.Fatal(err)
	}
	trace := &sessionPolicyReadTrace{}
	cfg := s.Pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s.Pool = pool
	_, err = s.Accept(t.Context(), Admission{SessionID: a.SessionID, Key: uuid.New(), Messages: []session.TextMessage{{Text: "next"}}})
	if err != nil {
		t.Fatal(err)
	}
	if trace.reads.Load() != 1 {
		t.Fatal("policy and credentials must share one session read", trace.reads.Load())
	}
}
