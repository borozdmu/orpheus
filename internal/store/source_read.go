package store

import (
	"bytes"
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/orpheus-agents/orpheus/internal/servicecredentials"
	"github.com/orpheus-agents/orpheus/internal/session"
	"github.com/orpheus-agents/orpheus/internal/store/db"
)

func activeForSourceRead(row db.InvocationRunRow) bool {
	return row.CancelRequestedAt == nil && !row.Status.Terminal()
}

// CheckSourceRead checks a live invocation both before and after the remote
// decision. A completed computation's revoked credential cannot release its file.
func (s *Store) CheckSourceRead(ctx context.Context, r servicecredentials.SourceRead) (bool, error) {
	denied := session.Problem(403, "source_read_unconfirmed", "Source access could not be confirmed.")
	queries := db.New(s.Pool)
	row, err := queries.InvocationRun(ctx, r.Invocation)
	if err != nil || !activeForSourceRead(row) {
		return false, denied
	}
	state, err := s.ServiceCredentials(ctx, row.SessionID, row.ID)
	if err != nil || state.Expired(time.Now()) || state.Context.InvocationID != r.Invocation {
		return false, denied
	}
	entry, ok := state.Entries["kord"]
	if !ok {
		return false, denied
	}
	allowed, err := s.CredentialClient.CheckSourceRead(ctx, entry, state.Context, r)
	if err != nil || !allowed {
		return false, err
	}
	err = s.Mutate(ctx, row.SessionID, false, func(tx pgx.Tx, _ *SessionRecord) error {
		current, err := db.New(tx).InvocationRun(ctx, r.Invocation)
		if err != nil || current.ID != row.ID || current.SessionID != row.SessionID || !activeForSourceRead(current) {
			return denied
		}
		fresh, err := s.credentials(ctx, tx, current.SessionID, current.ID)
		if err != nil || !sameSourceReadGrant(state, fresh, time.Now()) {
			return denied
		}
		return nil
	})
	return err == nil, err
}

func sameSourceReadGrant(original, fresh servicecredentials.State, now time.Time) bool {
	initial, had := original.Entries["kord"]
	current, has := fresh.Entries["kord"]
	return had && has && original.Context.Assignment != nil && fresh.Context.Assignment != nil &&
		original.Context.InvocationID == fresh.Context.InvocationID &&
		bytes.Equal(*original.Context.Assignment, *fresh.Context.Assignment) &&
		initial.Grant.CredentialID == current.Grant.CredentialID && initial.Grant.ScopeKey == current.Grant.ScopeKey &&
		!fresh.Expired(now)
}
