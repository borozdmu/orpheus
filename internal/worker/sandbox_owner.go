package worker

import (
	"errors"

	"github.com/orpheus-agents/orpheus/internal/harness"
	"github.com/orpheus-agents/orpheus/internal/store"
)

// Each executor belongs to one session. Both foreground execution and the
// independent retention pass must bind their platform before calling AgentBox.
func (e *Executor) bindPlatform(record store.SessionRecord) error {
	scope := e.Store.Profiles.SkillumPrivate
	owner := record.Configuration.SandboxOwner
	if scope == nil && owner == nil {
		return nil
	}
	if scope == nil || owner == nil || owner.Workspace != scope.Workspace || owner.Role != record.Configuration.Public.Agent.Profile || owner.Dialog == "" || owner.Reader == "" || record.ID != e.ID {
		return errors.New("sandbox owner differs from tenant configuration")
	}
	platform, ok := e.Platform.(interface {
		WithOwner(map[string]string) harness.Platform
	})
	if !ok {
		return errors.New("sandbox ownership verification unavailable")
	}
	e.Platform = platform.WithOwner(map[string]string{"orpheus_workspace": owner.Workspace, "orpheus_role": owner.Role, "orpheus_dialog": owner.Dialog, "orpheus_reader": owner.Reader, "orpheus_session_id": e.ID.String()})
	return nil
}
