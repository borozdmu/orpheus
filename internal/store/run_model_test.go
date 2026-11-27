//go:build integration

package store

import (
	"testing"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestRunModelSelectionPreservesSessionAndReplays(t *testing.T) {
	s := fixture(t)
	profile := s.Profiles.Profiles["default"]
	profile.Model = new("other")
	profile.Codex.Effort = "xhigh"
	const variant = "default--other"
	s.Profiles.Profiles[variant] = profile
	a, err := s.Accept(t.Context(), request())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(t.Context(), "UPDATE runs SET status='completed',finished_at=now() WHERE id=$1", a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	q := Admission{Key: uuid.New(), SessionID: a.SessionID, ModelProfile: new(variant), Messages: []session.TextMessage{{Text: "continue"}}}
	for _, bad := range []string{"unrelated", "default--other-owner", "default--masked"} {
		candidate := profile
		if bad == "default--other-owner" {
			candidate.Auth.APIKeyEnv = "OTHER_KEY"
		}
		if bad == "default--masked" {
			candidate.MaskInput = true
		}
		s.Profiles.Profiles[bad] = candidate
		invalid := q
		invalid.ModelProfile = new(bad)
		_, err := s.Accept(t.Context(), invalid)
		requireCode(t, err, "incompatible_model_profile")
	}
	b, err := s.Accept(t.Context(), q)
	if err != nil || b.SessionID != a.SessionID {
		t.Fatal(b, err)
	}
	selected, err := s.RunModel(t.Context(), b.RunID)
	if err != nil || selected == nil || *selected != (session.ModelSelection{Profile: variant, Model: "other", Effort: "xhigh"}) {
		t.Fatal(selected, err)
	}
	original, err := GetSession(t.Context(), s.Pool, a.SessionID, false)
	if err != nil || original.Configuration.Public.Agent.Model != "fixture" {
		t.Fatal("original session changed", err)
	}
	delete(s.Profiles.Profiles, variant)
	if again, err := s.Accept(t.Context(), q); err != nil || again != b {
		t.Fatal("replay", again, err)
	}
	q.ModelProfile = new("default")
	_, err = s.Accept(t.Context(), q)
	requireCode(t, err, "idempotency_conflict")
}
