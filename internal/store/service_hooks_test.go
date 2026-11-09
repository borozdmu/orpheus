package store

import (
	"errors"
	"testing"

	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestRunOnlyServiceBeforeRunIsRejectedBeforeAnyIssuing(t *testing.T) {
	store := &Store{Profiles: config.Profiles{Services: map[string]config.Service{
		"installer": {BeforeRun: new("#!/bin/sh\necho install\n")},
		"ordinary":  {EnvFrom: []string{"PUBLIC_ENV"}},
	}}}
	request := Admission{Create: &session.CreateSession{}, Services: []string{"installer"}}
	_, err := store.issueCredentials(t.Context(), request)
	problem, ok := errors.AsType[*session.APIError](err)
	if !ok || problem.Status != 422 || problem.Problem.Code != "invalid_service_scope" {
		t.Fatal("run-only trusted hook accepted", err)
	}
	request.Services = []string{"ordinary"}
	if state, err := store.issueCredentials(t.Context(), request); err != nil || len(state.Entries) != 0 {
		t.Fatal("ordinary run-only service changed", state, err)
	}
}
