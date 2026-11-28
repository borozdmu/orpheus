package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/internal/servicecredentials"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestFinalSourceCheckUsesCurrentLeaseForSameAuthority(t *testing.T) {
	now := time.Now()
	raw := json.RawMessage(`{"assignment":"stored"}`)
	access := session.AccessContext{InvocationID: uuid.New(), Assignment: &raw}
	state := func(id string, expiry time.Time) servicecredentials.State {
		return servicecredentials.State{Context: access, Entries: map[string]servicecredentials.Entry{
			"kord": {Grant: servicecredentials.Grant{CredentialID: id, ScopeKey: "scope", ExpiresAt: expiry, RunExpiresAt: now.Add(time.Hour)}}}}
	}
	old := state("same", now.Add(-time.Second))
	if !sameSourceReadGrant(old, state("same", now.Add(time.Minute)), now) {
		t.Fatal("renewed lease rejected")
	}
	if sameSourceReadGrant(old, state("new", now.Add(time.Minute)), now) || sameSourceReadGrant(old, old, now) {
		t.Fatal("unrelated or expired credential accepted")
	}
	current := state("same", now.Add(time.Minute))
	current.Context.InvocationID = uuid.New()
	if sameSourceReadGrant(old, current, now) {
		t.Fatal("unrelated invocation accepted")
	}
}
