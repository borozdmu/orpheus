package codex

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/orpheus-agents/orpheus/internal/harness"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestInterruptedReadWaitsForDurableBoundary(t *testing.T) {
	for _, ending := range []struct {
		name, record string
		want         session.Status
	}{
		{"active", "", session.Running},
		{"aborted", `{"type":"turn_aborted","turn_id":"turn"}`, session.Cancelled},
		{"completed", `{"type":"task_complete","turn_id":"turn"}`, session.Completed},
		{"failed", `{"type":"error","turn_id":"turn","message":"unavailable"}`, session.Failed},
	} {
		t.Run(ending.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			body := "{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_started\",\"turn_id\":\"turn\"}}\n"
			if ending.record != "" {
				body += "{\"type\":\"event_msg\",\"payload\":" + ending.record + "}\n"
			}
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			thread := Thread{ID: "thread", Path: &path, Turns: []NativeTurn{{ID: "turn", Status: "interrupted"}}}
			d, _, _ := fixtureDriver(t, &thread)
			snapshot, err := d.Snapshot(t.Context(), "thread", &path, 0)
			if err != nil || len(snapshot.Turns) != 1 || snapshot.Turns[0].Status != ending.want {
				t.Fatalf("unconfirmed cancellation: %#v %v", snapshot, err)
			}
			if ending.name == "active" {
				body += "{\"type\":\"event_msg\",\"payload\":{\"type\":\"turn_aborted\",\"turn_id\":\"turn\"}}\n"
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				snapshot, err = d.Snapshot(t.Context(), "thread", &path, snapshot.Offset)
				if err != nil || snapshot.Turns[0].Status != session.Cancelled {
					t.Fatalf("later cancellation lost: %#v %v", snapshot, err)
				}
			}
		})
	}
}

func TestInterruptedReadNeedsEvidence(t *testing.T) {
	for _, missing := range []string{"path", "turn", "file"} {
		t.Run(missing, func(t *testing.T) {
			var path *string
			if missing != "path" {
				path = new(filepath.Join(t.TempDir(), "history.jsonl"))
				if missing == "turn" {
					if err := os.WriteFile(*path, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			thread := Thread{ID: "thread", Path: path, Turns: []NativeTurn{{ID: "turn", Status: "interrupted"}}}
			d, _, _ := fixtureDriver(t, &thread)
			_, err := d.Snapshot(t.Context(), "thread", path, 0)
			if err == nil || missing != "file" && !errors.Is(err, harness.ErrUncertain) {
				t.Fatalf("missing cancellation evidence accepted: %v", err)
			}
		})
	}
}

func TestCompletionNotificationConfirmsInterruptionWithoutJournal(t *testing.T) {
	thread := Thread{ID: "thread", Turns: []NativeTurn{{ID: "turn", Status: "interrupted"}}}
	d, _, _ := fixtureDriver(t, &thread)
	d.rpc.receive(t.Context(), []byte(`{"method":"turn/completed","params":{"turn":{"id":"turn","status":"interrupted"}}}`))
	snapshot, err := d.Snapshot(t.Context(), "thread", nil, 0)
	if err != nil || snapshot.Turns[0].Status != session.Cancelled {
		t.Fatalf("explicit interruption lost: %#v %v", snapshot, err)
	}
}
