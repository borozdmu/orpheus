//go:build integration

package store

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/orpheus-agents/orpheus/internal/harness/codex"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestReconcileNULCompletesAndReplays(t *testing.T) {
	s := fixture(t)
	a, err := s.Accept(t.Context(), request())
	if err != nil {
		t.Fatal(err)
	}
	bindUsageRun(t, s, a, "turn")
	thread := codex.Thread{Turns: []codex.NativeTurn{{ID: "turn", Status: "completed", Items: []codex.NativeItem{
		{ID: "tool", Type: "commandExecution", Status: "completed", Command: json.RawMessage(`["printf","a\u0000b"]`), AggregatedOutput: new("a\x00b")},
		{ID: "answer", Type: "agentMessage", Text: "Done\x00."},
	}}}}
	apply := func() error {
		snapshot := codex.Normalize(thread, nil, nil, 1024, nil, nil)
		return s.Mutate(t.Context(), a.SessionID, false, func(tx pgx.Tx, r *SessionRecord) error {
			return Reconcile(t.Context(), tx, r, snapshot)
		})
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	run, err := s.Run(t.Context(), a.SessionID, a.RunID)
	if err != nil || run.Status != session.Completed || run.FinalMessage == nil || run.FinalMessage.Text != "Done�." {
		t.Fatalf("run did not finish: %+v, %v", run, err)
	}
	page, err := s.History(t.Context(), a.SessionID, &a.RunID, 100, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		if item.ToolCall == nil {
			continue
		}
		found = true
		var input []string
		var output codex.Result
		if err := json.Unmarshal(item.ToolCall.Input, &input); err != nil || len(input) != 2 || input[1] != "a�b" {
			t.Fatalf("tool input: %s, %v", item.ToolCall.Input, err)
		}
		if err := json.Unmarshal(item.ToolCall.Result, &output); err != nil || output.Text == nil || *output.Text != "a�b" {
			t.Fatalf("tool output: %s, %v", item.ToolCall.Result, err)
		}
	}
	if !found {
		t.Fatal("tool missing from history")
	}
	before, _, err := s.Read(t.Context(), a.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	after, _, err := s.Read(t.Context(), a.SessionID)
	if err != nil || after.NextEventSequence != before.NextEventSequence {
		t.Fatalf("replay wrote duplicate events: %v", err)
	}
}
