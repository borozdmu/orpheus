package main

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/orpheus-agents/orpheus/internal/harness"
	"github.com/orpheus-agents/orpheus/internal/session"
	"github.com/orpheus-agents/orpheus/internal/store"
)

type queue struct {
	s        *store.Store
	workflow workflow
	backend  backend
}

func allowed(w workflow, r *store.SessionRecord, run *store.RunRecord) bool {
	cfg := r.Configuration.Public
	h := cfg.Hooks
	return r.Namespace != nil && *r.Namespace == w.Namespace &&
		r.ExternalKey != nil && digest.MatchString(*r.ExternalKey) &&
		run.InputFingerprint != nil && digest.MatchString(*run.InputFingerprint) &&
		!r.AllowMultipleRuns && run.Number == 1 &&
		cfg.Agent.Profile == w.Profile && cfg.Sandbox.Template == w.Template &&
		len(cfg.Sandbox.Services)+len(cfg.Sandbox.EnvNames)+len(cfg.Sandbox.EnvFrom)+len(run.Services)+len(run.EnvNames)+len(run.EnvFrom) == 0 &&
		h.BeforeRun == nil && h.AfterRun == nil && h.AfterCreate == nil && h.BeforeRemove == nil &&
		cfg.Limits.RunTimeoutSeconds > 0 && cfg.Limits.RunTimeoutSeconds <= w.Timeout &&
		cfg.Limits.MaxSessionTokens > 0 && cfg.Limits.MaxSessionTokens <= w.MaxTokens
}

func finish(ctx context.Context, tx pgx.Tx, r *store.SessionRecord, run *store.RunRecord, status session.Status, code string) error {
	var problem *session.Error
	if code != "" {
		problem = &session.Error{Code: code, Message: code, Details: []session.Detail{}}
	}
	r.SlotReserved = false
	return store.Finish(ctx, tx, r, run, status, problem, nil)
}

func (q queue) plan(ctx context.Context, id uuid.UUID) (assignment, error) {
	var a assignment
	err := q.s.Mutate(ctx, id, true, func(tx pgx.Tx, r *store.SessionRecord) error {
		run, err := store.ActiveRun(ctx, tx, id)
		if err != nil {
			return err
		}
		if run == nil {
			r.SlotReserved = false
			return nil
		}
		if !allowed(q.workflow, r, run) {
			return finish(ctx, tx, r, run, session.Failed, "invalid_external_assignment")
		}
		a = assignment{id: *r.ExternalKey, Fingerprint: *run.InputFingerprint, Run: run.ID, Session: id}
		if run.Status == session.Accepted {
			msgs, err := store.Messages(ctx, tx, run.ID)
			if err != nil {
				return err
			}
			if len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Text != a.id {
				a = assignment{}
				return finish(ctx, tx, r, run, session.Failed, "invalid_external_message")
			}
			now := time.Now().UTC()
			run.Status = session.Starting
			run.Phase = new("agent")
			run.ExecutionStartedAt = &now
			run.DeadlineAt = new(now.Add(time.Duration(r.Configuration.Public.Limits.RunTimeoutSeconds) * time.Second))
			// The immutable (id, session, run, fingerprint) is the dispatch
			// intent. The backend binds it before performing any paid action.
			if err := store.PublishRun(ctx, tx, r, run); err != nil {
				return err
			}
		}
		if run.CancelRequestedAt == nil && run.DeadlineAt != nil && !time.Now().Before(*run.DeadlineAt) {
			run.CancelRequestedAt = new(time.Now().UTC())
			run.StopReason = new("run_timeout")
			run.Status = session.Cancelling
			if err := store.PublishRun(ctx, tx, r, run); err != nil {
				return err
			}
		}
		switch {
		case run.CancelRequestedAt != nil:
			a.action = "cancel"
		case run.Status == session.Starting:
			a.action = "start"
		case run.Status == session.Running:
			a.action = "get"
		default:
			return errors.New("unsupported external run state")
		}
		return nil
	})
	return a, err
}

func (q queue) tick(ctx context.Context, id uuid.UUID) error {
	a, err := q.plan(ctx, id)
	if err != nil || a.action == "" {
		return err
	}
	rec, err := q.backend.exchange(ctx, a)
	if err != nil {
		return err
	}
	return q.s.Mutate(ctx, id, true, func(tx pgx.Tx, r *store.SessionRecord) error {
		run, err := store.ActiveRun(ctx, tx, id)
		if err != nil || run == nil {
			return err
		}
		if run.ID != a.Run {
			return errors.New("external run changed")
		}
		if run.Status == session.Starting && (rec.State == "queued" || rec.State == "running" || rec.State == "done") {
			messages, err := store.Messages(ctx, tx, run.ID)
			if err != nil {
				return err
			}
			first := &messages[0]
			first.DeliveryStatus = new("delivered")
			if err := store.PublishMessage(ctx, tx, r, first); err != nil {
				return err
			}
			run.Status = session.Running
			if err := store.PublishRun(ctx, tx, r, run); err != nil {
				return err
			}
		}
		switch rec.State {
		case "queued", "running":
			return nil
		case "cancelled":
			return finish(ctx, tx, r, run, session.Cancelled, "")
		case "rejected":
			return finish(ctx, tx, r, run, session.Failed, "external_execution_rejected")
		case "unknown":
			if run.CancelRequestedAt != nil {
				return finish(ctx, tx, r, run, session.Failed, "external_execution_outcome_unknown")
			}
			// Only poll after uncertainty. No new model execution key exists.
			if run.Status != session.Running || run.Observation == nil || *run.Observation != "uncertain" {
				run.Status = session.Running
				run.Observation = new("uncertain")
				return store.PublishRun(ctx, tx, r, run)
			}
			return nil
		case "done":
			// A provider receipt wins a race against a later cancellation.
			// Both sides keep the full private result in CX. Only a reference
			// and metered counters enter the Orpheus session history.
			message := store.MessageRecord{
				ID: uuid.New(), SessionID: id, RunID: a.Run, Role: "assistant",
				Kind: new("answer"), Text: a.id}
			if err := store.PublishMessage(ctx, tx, r, &message); err != nil {
				return err
			}
			r.ThreadID = new(id.String())
			run.NativeTurnID = new(a.Run.String())
			if err := store.SaveRun(ctx, tx, run); err != nil {
				return err
			}
			return complete(ctx, tx, r, run, *rec.Usage)
		}
		return errors.New("unexpected backend state")
	})
}

func complete(ctx context.Context, tx pgx.Tx, r *store.SessionRecord, run *store.RunRecord, u counters) error {
	// Native counters have a known zero baseline for a single-run session.
	// ApplyUsage is idempotent and follows terminal publication, so exceeding
	// the budget in the final turn cannot overwrite a confirmed result.
	r.CachedInputNativeTotal = new(int64(0))
	r.ReasoningOutputNativeTotal = new(int64(0))
	if err := finish(ctx, tx, r, run, session.Completed, ""); err != nil {
		return err
	}
	report := harness.UsageReport{ContextID: r.ID.String(), TurnID: run.ID.String(),
		Total:             session.Usage{InputTokens: *u.Input, OutputTokens: *u.Output, TotalTokens: *u.Input + *u.Output},
		CachedInputTokens: u.Cached, ReasoningOutputTokens: u.Reasoning}
	return store.ApplyUsage(ctx, tx, r, []harness.UsageReport{report})
}
