package session

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAccessContextValidation(t *testing.T) {
	var missing *AccessContext
	if missing.Validate() == nil {
		t.Fatal("missing accepted")
	}
	access := AccessContext{InvocationID: uuid.New(), Subject: AccessSubject{Namespace: "mm", ID: "a"}, ScopeContext: AccessScopeContext{AudienceSubjects: []string{"a", "b"}, ChannelID: "c", AgentID: "bot", PolicySpaceID: "p"}, ContextObservedAt: time.Now()}
	if err := access.Validate(); err != nil {
		t.Fatal(err)
	}
	if !access.Allows(AccessSubject{Namespace: "mm", ID: "b"}) || access.Allows(AccessSubject{Namespace: "other", ID: "b"}) || access.Allows(AccessSubject{Namespace: "mm", ID: "outsider"}) {
		t.Fatal("audience mismatch")
	}
	access.ScopeContext.AudienceSubjects = []string{"b"}
	if access.Validate() == nil {
		t.Fatal("author outside audience")
	}
	access.ScopeContext.AudienceSubjects = []string{"a", "a"}
	if access.Validate() == nil {
		t.Fatal("duplicates accepted")
	}
}
