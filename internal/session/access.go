package session

import (
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

type AccessSubject struct {
	Namespace string `json:"namespace"`
	ID        string `json:"id"`
}
type AccessScopeContext struct {
	AudienceSubjects []string `json:"audience_subjects"`
	ChannelID        string   `json:"channel_id"`
	AgentID          string   `json:"agent_id"`
	PolicySpaceID    string   `json:"policy_space_id"`
}
type AccessContext struct {
	InvocationID      uuid.UUID          `json:"invocation_id"`
	Subject           AccessSubject      `json:"subject"`
	ScopeContext      AccessScopeContext `json:"scope_context"`
	ContextObservedAt time.Time          `json:"context_observed_at"`
}

func (a *AccessContext) Validate() error {
	valid := func(s string) bool {
		return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
	}
	if a == nil || a.InvocationID == uuid.Nil || a.ContextObservedAt.IsZero() {
		return Problem(422, "access_context_required", "A trusted access context is required.")
	}
	c := a.ScopeContext
	if !valid(a.Subject.Namespace) || !valid(a.Subject.ID) || !valid(c.ChannelID) || !valid(c.AgentID) || !valid(c.PolicySpaceID) || len(c.AudienceSubjects) == 0 || len(c.AudienceSubjects) > 4096 {
		return Problem(422, "invalid_access_context", "Invalid access context.")
	}
	seen := map[string]bool{}
	for _, id := range c.AudienceSubjects {
		if !valid(id) || seen[id] {
			return Problem(422, "invalid_access_context", "Invalid audience.")
		}
		seen[id] = true
	}
	if !slices.Contains(c.AudienceSubjects, a.Subject.ID) {
		return Problem(403, "input_subject_denied", "Author is outside the audience.")
	}
	return nil
}
func (a *AccessContext) Allows(subject AccessSubject) bool {
	return a != nil && subject.Namespace == a.Subject.Namespace && slices.Contains(a.ScopeContext.AudienceSubjects, subject.ID)
}
