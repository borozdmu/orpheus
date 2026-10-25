//go:build integration

package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/session"
)

// The owner's baseline-only regression also passes here: a persisted marker
// must enforce masking even if the profile no longer enables the feature.
func TestProtectedSessionMarkerNeverAcceptsPlaintext(t *testing.T) {
	s := fixture(t)
	a, err := s.Accept(t.Context(), request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(t.Context(), a.SessionID, a.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(t.Context(), "UPDATE sessions SET configuration=jsonb_set(configuration, '{input_masked}', 'true'::jsonb) WHERE id=$1", a.SessionID); err != nil {
		t.Fatal(err)
	}
	accepted, err := s.Accept(t.Context(), Admission{SessionID: a.SessionID, Key: uuid.New(), Messages: []session.TextMessage{{Text: "original private text"}}})
	if err == nil {
		m, readErr := GetMessage(t.Context(), s.Pool, accepted.MessageID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		t.Fatalf("API persisted protected session input without masking: %q", m.Text)
	}
	requireCode(t, err, "input_masker_unavailable")
}

func TestConcurrentReplayWinsOverMaskerFailure(t *testing.T) {
	s := fixture(t)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	masker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"masked_texts": []string{"masked"}})
	}))
	defer masker.Close()
	s.Profiles.InputMasker = config.InputMasker{URL: masker.URL, TimeoutSeconds: 5}
	p := s.Profiles.Profiles["default"]
	p.MaskInput = true
	s.Profiles.Profiles["default"] = p
	req := request()
	type result struct {
		acceptance session.Acceptance
		err        error
	}
	first := make(chan result, 1)
	go func() { a, err := s.Accept(t.Context(), req); first <- result{a, err} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("masker not reached")
	}
	accepted, err := s.Accept(t.Context(), req)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	got := <-first
	if got.err != nil || got.acceptance != accepted {
		t.Fatalf("same idempotency key committed, but concurrent original reports nothing accepted: %v", got.err)
	}
	var sessions, runs, messages int
	if err := s.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM sessions),(SELECT count(*) FROM runs),(SELECT count(*) FROM messages)").Scan(&sessions, &runs, &messages); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || runs != 1 || messages != 1 {
		t.Fatalf("unexpected effects: %d %d %d", sessions, runs, messages)
	}
}

func TestMaskerSecretRotationAndOrdinaryIsolation(t *testing.T) {
	for _, source := range []string{"env", "file"} {
		t.Run(source, func(t *testing.T) {
			s := fixture(t)
			expected := "synthetic-first"
			calls := 0
			masker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer "+expected {
					t.Error("stale or invalid masker credential")
					w.WriteHeader(401)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"masked_texts": []string{"masked"}})
			}))
			defer masker.Close()
			s.Profiles.InputMasker = config.InputMasker{URL: masker.URL, TimeoutSeconds: 1}
			path := filepath.Join(t.TempDir(), "masker-token")
			set := func(value string) { t.Setenv("ROTATING_MASKER_TOKEN", value) }
			if source == "env" {
				s.Profiles.InputMasker.TokenEnv = "ROTATING_MASKER_TOKEN"
			} else {
				s.Profiles.InputMasker.TokenFile = path
				set = func(value string) {
					if err := os.WriteFile(path, []byte(value), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			p := s.Profiles.Profiles["default"]
			p.MaskInput = true
			s.Profiles.Profiles["protected"] = p
			protected := func() Admission { req := request(); req.Create.Configuration.Agent.Profile = "protected"; return req }
			// The file is initially absent; environment is initially empty.
			t.Setenv("ROTATING_MASKER_TOKEN", "")
			for _, value := range []string{"", "bad\nheader", "synthetic-first", "synthetic-rotated", ""} {
				set(value)
				before := calls
				var beforeCount int
				if err := s.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions").Scan(&beforeCount); err != nil {
					t.Fatal(err)
				}
				_, err := s.Accept(t.Context(), protected())
				valid := strings.HasPrefix(value, "synthetic-")
				if valid != (err == nil) {
					t.Fatalf("secret availability: %v", err)
				}
				if !valid {
					if err == nil || err.Error() != "input_masker_unavailable" || calls != before {
						t.Fatal("unsafe credential failure", err)
					}
					var afterCount int
					if err := s.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions").Scan(&afterCount); err != nil {
						t.Fatal(err)
					}
					if afterCount != beforeCount {
						t.Fatal("failed credential admission wrote session")
					}
				}
				report, err := s.AccountLimits(t.Context())
				if err != nil || report.InputMaskerSecretAvailable == nil || *report.InputMaskerSecretAvailable != valid {
					t.Fatal("credential diagnostic", err)
				}
				if _, err := s.Accept(t.Context(), request()); err != nil {
					t.Fatal("ordinary request blocked by secret", err)
				}
				if value == "synthetic-first" {
					expected = "synthetic-rotated"
				}
			}
		})
	}
}

// Pause after the initial durable replay read, before loading the invalid token.
type tokenReplayBarrier struct {
	entered, release chan struct{}
	used             atomic.Bool
}

type tokenReplayBarrierKey struct{}

func (b *tokenReplayBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "-- name: GetIdempotency :one\n") && b.used.CompareAndSwap(false, true) {
		return context.WithValue(ctx, tokenReplayBarrierKey{}, true)
	}
	return ctx
}

func (b *tokenReplayBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(tokenReplayBarrierKey{}) == true {
		// The initial replay SELECT has finished with no row. Pause before Token,
		// so another replica can commit without depending on special-file I/O.
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
}

func TestConcurrentReplayOnTokenFailure(t *testing.T) {
	for _, race := range []string{"same", "different", "absent"} {
		t.Run(race, func(t *testing.T) {
			s := fixture(t)
			masker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"masked_texts": []string{"masked"}})
			}))
			defer masker.Close()
			s.Profiles.InputMasker = config.InputMasker{URL: masker.URL, TimeoutSeconds: 1, TokenFile: filepath.Join(t.TempDir(), "invalid-token")}
			if err := os.WriteFile(s.Profiles.InputMasker.TokenFile, []byte("\n"), 0600); err != nil {
				t.Fatal(err)
			}
			other := *s
			other.Profiles.InputMasker.TokenFile = ""
			barrier := &tokenReplayBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			cfg := s.Pool.Config()
			cfg.ConnConfig.Tracer = barrier
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			s.Pool = pool
			var once sync.Once
			unblock := func() { once.Do(func() { close(barrier.release) }) }
			defer unblock()
			p := s.Profiles.Profiles["default"]
			p.MaskInput = true
			s.Profiles.Profiles["default"] = p
			req := request()
			type result struct {
				a   session.Acceptance
				err error
			}
			done := make(chan result, 1)
			go func() { a, err := s.Accept(t.Context(), req); done <- result{a, err} }()
			select {
			case <-barrier.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("initial replay barrier not reached")
			}
			var accepted session.Acceptance
			if race != "absent" {
				// Same policy and DB, independent secret source, as on another API replica.
				second := req
				if race == "different" {
					create := *req.Create
					create.Messages = []session.TextMessage{{Text: "different original"}}
					second.Create = &create
				}
				var err error
				accepted, err = other.Accept(t.Context(), second)
				if err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("token failure replay did not finish")
			}
			switch race {
			case "same":
				if got.err != nil || got.a != accepted {
					t.Fatal("committed replay lost on token error", got.err)
				}
			case "different":
				requireCode(t, got.err, "idempotency_conflict")
			case "absent":
				requireCode(t, got.err, "input_masker_unavailable")
			}
			var sessions, runs, messages int
			if err := s.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM sessions),(SELECT count(*) FROM runs),(SELECT count(*) FROM messages)").Scan(&sessions, &runs, &messages); err != nil {
				t.Fatal(err)
			}
			want := 1
			if race == "absent" {
				want = 0
			}
			if sessions != want || runs != want || messages != want {
				t.Fatalf("duplicate or unexpected durable effects: %d %d %d", sessions, runs, messages)
			}
		})
	}
}

func TestConcurrentReplayOnMaskerFailureConflictAndAbsence(t *testing.T) {
	for _, race := range []string{"different", "absent"} {
		t.Run(race, func(t *testing.T) {
			s := fixture(t)
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			masker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
					w.WriteHeader(503)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"masked_texts": []string{"masked"}})
			}))
			defer masker.Close()
			defer unblock()
			s.Profiles.InputMasker = config.InputMasker{URL: masker.URL, TimeoutSeconds: 5}
			p := s.Profiles.Profiles["default"]
			p.MaskInput = true
			s.Profiles.Profiles["default"] = p
			req := request()
			done := make(chan error, 1)
			go func() { _, err := s.Accept(t.Context(), req); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("masker not reached")
			}
			if race == "different" {
				create := *req.Create
				create.Messages = []session.TextMessage{{Text: "different original"}}
				second := req
				second.Create = &create
				if _, err := s.Accept(t.Context(), second); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			err := <-done
			if race == "different" {
				requireCode(t, err, "idempotency_conflict")
			} else {
				requireCode(t, err, "input_masker_unavailable")
			}
		})
	}
}
