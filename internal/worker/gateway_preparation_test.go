//go:build integration

package worker

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/internal/harness"
	"github.com/orpheus-agents/orpheus/internal/harness/codex"
	"github.com/orpheus-agents/orpheus/internal/session"
	"github.com/orpheus-agents/orpheus/internal/store"
)

type gatewayLaunchBox struct {
	*remote
	startCalls            int
	readConfig            []byte
	fault                 string
	cancel                context.CancelFunc
	lostStart, foundStart bool
}

func (b *gatewayLaunchBox) Start(_ context.Context, _ string, env map[string]string, _ string) (harness.Stream, int, error) {
	b.startCalls++
	if b.lostStart {
		if b.foundStart {
			b.processes = []harness.Process{{PID: 123, Env: env}}
		}
		return nil, 0, errors.New("lost process start response")
	}
	return nil, 0, harness.Failure("synthetic_stop", "process start was reached")
}
func (b *gatewayLaunchBox) Attach(context.Context, int) (harness.Stream, error) {
	return nil, errors.New("synthetic attach interruption")
}
func (b *gatewayLaunchBox) Read(context.Context, string) (io.ReadCloser, error) {
	if b.readConfig == nil {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(string(b.readConfig))), nil
}
func (b *gatewayLaunchBox) Run(ctx context.Context, command string) ([]byte, error) {
	if strings.Contains(command, "CODEX_HOME") {
		switch b.fault {
		case "state_dir", "state_dir_lost_response":
			return nil, errors.New("synthetic state directory response loss")
		case "timeout":
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte("/home/template/.codex"), nil
	}
	return b.remote.Run(ctx, command)
}
func (b *gatewayLaunchBox) Write(ctx context.Context, path string, raw []byte) error {
	switch b.fault {
	case "write":
		return errors.New("synthetic write failure")
	case "write_lost_response":
		b.readConfig = raw
		return errors.New("synthetic write response loss")
	case "shutdown":
		b.cancel()
		return ctx.Err()
	}
	b.readConfig = raw
	return b.remote.Write(ctx, path, raw)
}
func gatewayExecutor(t *testing.T) (*store.Store, *gatewayLaunchBox, session.Acceptance, *Executor) {
	t.Helper()
	s, r, a, e := setup(t)
	s.Settings.MaxConcurrentSessions = 1
	t.Setenv("KEY", "synthetic-gateway-key")
	if _, err := s.Pool.Exec(t.Context(), "UPDATE sessions SET configuration=jsonb_set(configuration, '{credentials,base_url}', to_jsonb($2::text)) WHERE id=$1", a.SessionID, "https://gateway.example/v1"); err != nil {
		t.Fatal(err)
	}
	box := &gatewayLaunchBox{remote: r}
	e.NewDriver = func(harness.Sandbox) harness.Driver { return codex.New(box, 50*time.Millisecond, 1024) }
	return s, box, a, e
}
func restartGatewayExecutor(s *store.Store, b *gatewayLaunchBox, a session.Acceptance, e *Executor) *Executor {
	e.Disconnect()
	next := NewExecutor(a.SessionID, s, b.remote)
	next.NewDriver = e.NewDriver
	return next
}
func settleGateway(t *testing.T, e *Executor) {
	t.Helper()
	for range 4 {
		if err := e.Tick(t.Context()); err != nil {
			if failure, ok := errors.AsType[*harness.ExecutionError](err); ok {
				if err := e.failure(t.Context(), failure); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Fatal(err)
			}
		}
	}
}
func ordinaryGatewayAdmission(t *testing.T, s *store.Store) {
	t.Helper()
	if _, err := s.Accept(t.Context(), store.Admission{Key: uuid.New(), Create: &session.CreateSession{Configuration: session.ConfigurationInput{Agent: session.AgentInput{Profile: "p"}, Sandbox: session.SandboxInput{Template: "codex"}, Limits: session.Limits{RunTimeoutSeconds: 60}}, Messages: []session.TextMessage{{Text: "ordinary task"}}}}); err != nil {
		t.Fatal("ordinary admission blocked", err)
	}
}

// Regression from the owner's review: a Write error must not strand an
// uncertain launch forever while consuming the only shared capacity slot.
func TestGatewayPreparationFailureMustReleaseOrRecoverCapacity(t *testing.T) {
	for _, fault := range []string{"state_dir", "state_dir_lost_response", "write", "write_lost_response", "timeout"} {
		t.Run(fault, func(t *testing.T) {
			s, box, a, e := gatewayExecutor(t)
			box.fault = fault
			started := time.Now()
			err := e.Tick(t.Context())
			failure, ok := errors.AsType[*harness.ExecutionError](err)
			if !ok || failure.Code != "environment_unavailable" {
				t.Fatalf("pre-Start failure became uncertain: %v", err)
			}
			if time.Since(started) > time.Second || box.startCalls != 0 {
				t.Fatal("unbounded preparation or premature Start")
			}
			// Restart before the error can be committed: the launch must still be
			// pending, so preparation may be retried safely with no duplicate process.
			e = restartGatewayExecutor(s, box, a, e)
			box.fault = ""
			settleGateway(t, e)
			record, run, err := s.Read(t.Context(), a.SessionID)
			if err != nil || run != nil || record.SlotReserved {
				t.Fatalf("preparation retained capacity: active=%v reserved=%v err=%v", run != nil, record.SlotReserved, err)
			}
			if box.startCalls != 1 {
				t.Fatalf("recovery Start calls=%d", box.startCalls)
			}
			ordinaryGatewayAdmission(t, s)
		})
	}
}
func TestGatewayPreparationTerminalFailureReleasesCapacity(t *testing.T) {
	s, box, a, e := gatewayExecutor(t)
	box.fault = "write"
	settleGateway(t, e)
	record, run, err := s.Read(t.Context(), a.SessionID)
	if err != nil || run != nil || record.SlotReserved || box.startCalls != 0 {
		t.Fatal("failed setup retained shared capacity", err)
	}
	ordinaryGatewayAdmission(t, s)
}
func TestGatewayPreparationInterruptedByWorkerShutdown(t *testing.T) {
	s, box, a, e := gatewayExecutor(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	box.fault, box.cancel = "shutdown", cancel
	if err := e.Tick(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown: %v", err)
	}
	e = restartGatewayExecutor(s, box, a, e)
	box.fault = ""
	settleGateway(t, e)
	if box.startCalls != 1 {
		t.Fatalf("shutdown recovery starts=%d", box.startCalls)
	}
	ordinaryGatewayAdmission(t, s)
}
func TestGatewayLostStartResponseNeverStartsTwice(t *testing.T) {
	for _, found := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_found", true: "found"}[found], func(t *testing.T) {
			s, box, a, e := gatewayExecutor(t)
			box.lostStart, box.foundStart = true, found
			if err := e.Tick(t.Context()); !errors.Is(err, harness.ErrUncertain) {
				t.Fatalf("lost Start: %v", err)
			}
			for range 3 {
				e = restartGatewayExecutor(s, box, a, e)
				_ = e.Tick(t.Context())
			}
			if box.startCalls != 1 {
				t.Fatalf("blind Start retry: %d", box.startCalls)
			}
		})
	}
}
