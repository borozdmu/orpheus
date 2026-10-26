//go:build integration

package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/orpheus-agents/orpheus/internal/config"
)

func TestOrdinaryAdmissionDuringBlockedMasker(t *testing.T) {
	s := fixture(t)
	cfg := s.Pool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s.Pool = pool
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	masker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(503)
	}))
	defer masker.Close()
	defer unblock()
	s.Profiles.InputMasker = config.InputMasker{URL: masker.URL, TimeoutSeconds: 5}
	p := s.Profiles.Profiles["default"]
	p.MaskInput = true
	s.Profiles.Profiles["protected"] = p
	req := request()
	req.Create.Configuration.Agent.Profile = "protected"
	done := make(chan error, 1)
	go func() { _, err := s.Accept(t.Context(), req); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("masker not reached")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := s.Accept(ctx, request()); err != nil {
		t.Fatal("ordinary request affected by blocked masker", err)
	}
	unblock()
	if err := <-done; err == nil {
		t.Fatal("protected request accepted during masker failure")
	}
	t.Log("ordinary request accepted with one SQL connection while protected request was waiting on failed masker")
}
