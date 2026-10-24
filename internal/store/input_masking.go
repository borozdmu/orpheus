package store

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/internal/inputmasker"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func (a *Admission) normalize() {
	if a.Create != nil {
		a.Messages = a.Create.Messages
		a.InputFingerprint = a.Create.InputFingerprint
		a.Env = a.Create.Env
		a.EnvFrom = a.Create.EnvFrom
		a.Services = a.Create.Services
		a.AccessContext = a.Create.AccessContext
	}
}

func (a Admission) operation() (string, string) {
	if a.RunID != uuid.Nil {
		return "steer", a.RunID.String()
	}
	if a.SessionID != uuid.Nil {
		return "run", a.SessionID.String()
	}
	return "create", "sessions"
}

// Admit applies the trusted session privacy policy before the admission
// transaction, credentials issuance, or any durable write. Opaque metadata and
// authority/routing identifiers never enter the masker or model input.
func (s *Store) Admit(ctx context.Context, a Admission) (session.Acceptance, error) {
	a.normalize()
	if err := a.validate(); err != nil {
		return session.Acceptance{}, err
	}
	enabled := false
	if a.Create != nil {
		enabled = s.Profiles.Profiles[a.Create.Configuration.Agent.Profile].MaskInput
	} else {
		record, err := GetSession(ctx, s.Pool, a.SessionID, false)
		if err != nil {
			return session.Acceptance{}, err
		}
		enabled = record.Configuration.InputMasked
		a.policySession = &record
	}
	if !enabled {
		return s.accept(ctx, a)
	}
	// Hash the original canonical request, never store the original text. Replay
	// happens before masking, so a retry needs neither the masker nor its key.
	digest, err := fingerprint(a)
	if err != nil {
		return session.Acceptance{}, err
	}
	op, resource := a.operation()
	if prior, found, err := s.replay(ctx, s.Pool, a, op, resource, digest); err != nil || found {
		return prior, err
	}
	texts := make([]string, len(a.Messages))
	for i, message := range a.Messages {
		texts[i] = message.Text
	}
	clientInstructions := a.Create != nil && a.Create.Configuration.Agent.Instructions != nil
	if clientInstructions {
		texts = append(texts, *a.Create.Configuration.Agent.Instructions)
	}
	token, err := s.Profiles.InputMasker.Token()
	if err != nil {
		slog.WarnContext(ctx, "Input masking unavailable", "reason", "access_token")
		return s.maskingUnavailable(ctx, a, op, resource, digest)
	}
	masked, err := inputmasker.Mask(ctx, s.Profiles.InputMasker.URL, time.Duration(s.Profiles.InputMasker.TimeoutSeconds)*time.Second, texts, token)
	if err != nil {
		slog.WarnContext(ctx, "Input masking unavailable", "reason", "masker_request")
		return s.maskingUnavailable(ctx, a, op, resource, digest)
	}
	a.Messages = slices.Clone(a.Messages)
	for i := range a.Messages {
		a.Messages[i].Text = masked[i]
	}
	if a.Create != nil {
		create := *a.Create
		create.Messages = a.Messages
		if clientInstructions {
			create.Configuration.Agent.Instructions = new(masked[len(a.Messages)])
		}
		a.Create = &create
	}
	a.inputMasked, a.requestDigest = true, digest
	return s.accept(ctx, a)
}

func (s *Store) maskingUnavailable(ctx context.Context, a Admission, op, resource, digest string) (session.Acceptance, error) {
	// A concurrent request may have committed while we waited for the secret or
	// service. Keep the original digest: only durable acceptance can win here.
	if prior, found, err := s.replay(ctx, s.Pool, a, op, resource, digest); err != nil || found {
		return prior, err
	}
	return session.Acceptance{}, session.Problem(503, "input_masker_unavailable", "Input masking is unavailable. Nothing was accepted; retry later.")
}

// Accept also enforces intake masking for in-process callers. The HTTP entry
// points call Admit explicitly; neither route can bypass the session policy.
func (s *Store) Accept(ctx context.Context, a Admission) (session.Acceptance, error) {
	return s.Admit(ctx, a)
}
