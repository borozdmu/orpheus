package agentcontract

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSignedAssignmentCannotChangeAuthorityOrRoute(t *testing.T) {
	key := strings.Repeat("k", 32)
	reader := Principal{Namespace: "telegram:test", ID: "1", PrincipalID: "42"}
	a := Assignment{Schema: Version, InvocationID: uuid.New(), Agent: "mark", Author: reader, Readers: []Principal{reader}, Text: "read", Delivery: Route{Connector: "telegram", Source: "test", Chat: "-1001", Message: "1"}, Audience: Audience{Kind: "exact", Instance: "https://test.invalid", ChannelID: "-1001", ChannelType: "telegram-managed", Complete: true}, Confirmation: Confirmation{ObservedAt: time.Now().UTC(), Epoch: 1, Revision: "role-v1"}}
	if err := a.Seal(key); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(key, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Assignment){func(a *Assignment) { a.Delivery.Chat = "-2002" }, func(a *Assignment) { a.Author.ID = "2" }, func(a *Assignment) { a.Readers = []Principal{{Namespace: "telegram:test", ID: "1", PrincipalID: "99"}} }, func(a *Assignment) { a.Confirmation.Epoch++ }, func(a *Assignment) { a.Text = "write" }, func(a *Assignment) { a.Audience.ChannelType = "O" }} {
		changed := a
		mutate(&changed)
		if changed.Verify(key, time.Now()) == nil {
			t.Fatal("modified assignment accepted")
		}
	}
	if a.Verify(key, time.Now().Add(21*time.Minute)) == nil {
		t.Fatal("expired receipt accepted")
	}
}
func TestPublicAudienceRequiresAllCurrentTeams(t *testing.T) {
	g := AudienceGroup{Instance: "instance-1", TeamID: "team-1", Revision: "roles-members-1"}
	a := Audience{Kind: "team-current", Instance: "instance-1", ChannelID: "public-1", ChannelType: "O", Complete: true, Groups: []AudienceGroup{g}}
	if a.Validate() != nil {
		t.Fatal("complete current team descriptor rejected")
	}
	for _, kind := range []string{"incomplete", "unknown", "no-team"} {
		b := a
		switch kind {
		case "incomplete":
			b.Complete = false
		case "unknown":
			b.ChannelType = "X"
		case "no-team":
			b.Groups = nil
		}
		if b.Validate() == nil {
			t.Fatalf("coverage failure accepted: %s", kind)
		}
	}
	a.Groups = append(a.Groups, AudienceGroup{Instance: "instance-2", TeamID: "team-2", Revision: "roles-members-2"})
	if a.Validate() != nil {
		t.Fatal("complete multi-team descriptor rejected")
	}
}
