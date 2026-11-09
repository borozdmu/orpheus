//go:build integration

package store

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestTrustedServiceHookSnapshotAndReplay(t *testing.T) {
	s := fixture(t)
	original := "#!/bin/sh\necho original-service\n"
	caller := "#!/bin/sh\necho transport\n"
	s.Profiles.Services = map[string]config.Service{"installer": {Name: "Installer", Description: "Public installer", BeforeRun: &original}}
	req := request()
	req.Create.Configuration.Sandbox.Services = []string{"installer"}
	req.Create.Configuration.Hooks = &session.HooksInput{BeforeRun: &caller}
	accepted, err := s.Accept(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	saved := mustSession(t, s, accepted.SessionID)
	snapshot := *saved.Configuration.Public.Hooks.BeforeRun
	if strings.Count(snapshot, base64.StdEncoding.EncodeToString([]byte(original))) != 1 {
		t.Fatal("service hook was not frozen once")
	}
	s.Profiles.Services["installer"] = config.Service{Name: "Installer", Description: "Updated installer", BeforeRun: new("#!/bin/sh\necho updated-service\n")}
	if replay, err := s.Accept(t.Context(), req); err != nil || replay != accepted {
		t.Fatal("catalog change affected replay", replay, err)
	}
	if got := mustSession(t, s, accepted.SessionID); *got.Configuration.Public.Hooks.BeforeRun != snapshot {
		t.Fatal("catalog change rewrote accepted hook")
	}
	var count int
	if err := s.Pool.QueryRow(t.Context(), "SELECT count(*) FROM hook_executions WHERE run_id=$1 AND name='before_run'", accepted.RunID).Scan(&count); err != nil || count != 1 {
		t.Fatal("replay duplicated planned hook", count, err)
	}
	fresh := request()
	fresh.Create.Configuration.Sandbox.Services = []string{"installer"}
	fresh.Create.Configuration.Hooks = &session.HooksInput{BeforeRun: &caller}
	next, err := s.Accept(t.Context(), fresh)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, s, next.SessionID); *got.Configuration.Public.Hooks.BeforeRun == snapshot {
		t.Fatal("new session retained old catalog script")
	}
	delete(s.Profiles.Services, "installer")
	if replay, err := s.Accept(t.Context(), req); err != nil || replay != accepted {
		t.Fatal("removed catalog broke accepted replay", replay, err)
	}
}
