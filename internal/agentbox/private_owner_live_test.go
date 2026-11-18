//go:build live

package agentbox

import (
	"context"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLivePrivateOwnerLifecycle(t *testing.T) {
	template := os.Getenv("ORPHEUS_OWNER_TEST_TEMPLATE")
	if template == "" {
		t.Skip("requires an operator-selected template in the test project")
	}
	base, err := New()
	if err != nil {
		t.Fatal(err)
	}
	workspace := os.Getenv("ORPHEUS_OWNER_TEST_WORKSPACE")
	operation := os.Getenv("ORPHEUS_OWNER_TEST_OPERATION")
	dialog := os.Getenv("ORPHEUS_OWNER_TEST_DIALOG")
	for _, id := range []string{workspace, operation, dialog} {
		if _, err := uuid.Parse(id); err != nil {
			t.Fatal("missing test owner reservation")
		}
	}
	sessionID := uuid.NewString()
	scope := map[string]string{"orpheus_workspace": workspace, "orpheus_role": "owner-test", "orpheus_profile": "test", "orpheus_dialog": dialog, "orpheus_reader": uuid.NewString(), "orpheus_session_id": sessionID}
	owner := base.WithOwner(scope)
	creation := map[string]string{"orpheus_session_id": sessionID, "orpheus_operation_id": operation}
	box, err := owner.Create(t.Context(), template, 3*time.Minute, creation)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("owned-sandbox=%s", box.ID())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := owner.Delete(ctx, box.ID()); err != nil {
			t.Error(err)
		}
	})
	matches, err := owner.Find(t.Context(), creation)
	if err != nil || len(matches) != 1 || matches[0] != box.ID() {
		t.Fatal("cannot recover owned sandbox", err)
	}
	different := maps.Clone(scope)
	different["orpheus_dialog"] = uuid.NewString()
	other := base.WithOwner(different)
	if _, err := other.Info(t.Context(), box.ID()); err == nil {
		t.Fatal("foreign metadata accepted")
	}
	matches, err = other.Find(t.Context(), creation)
	if err != nil || len(matches) != 0 {
		t.Fatal("foreign sandbox in scoped list", err)
	}
	if err := box.Pause(t.Context()); err != nil {
		t.Fatal(err)
	}
	resumed, err := owner.Connect(t.Context(), box.ID(), 3*time.Minute)
	if err != nil || resumed.ID() != box.ID() {
		t.Fatal("owned sandbox did not resume", err)
	}
}
