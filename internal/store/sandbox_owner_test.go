package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/agentcontract"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/servicecredentials"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestSkillumOwnerRequiresMatchingCorporateGrant(t *testing.T) {
	workspace, dialog := uuid.NewString(), uuid.NewString()
	person := agentcontract.Principal{ID: "1", PrincipalID: "10", Namespace: "skillum:" + workspace}
	task := agentcontract.Assignment{Schema: agentcontract.Version, InvocationID: uuid.New(), Agent: "mark", Author: person, Readers: []agentcontract.Principal{person},
		Delivery: agentcontract.Route{Connector: "skillum", Source: workspace, Chat: dialog, Message: uuid.NewString()},
		Audience: agentcontract.Audience{Kind: "exact", Instance: person.Namespace, ChannelType: "skillum-private", ChannelID: dialog, Complete: true}, Confirmation: agentcontract.Confirmation{ObservedAt: time.Now()}}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(data)
	scope := config.SkillumPrivate{Workspace: workspace, Provider: "provider", Agent: "mark", Profile: "marketing-native"}
	s := &Store{Profiles: config.Profiles{SkillumPrivate: &scope}}
	state := servicecredentials.State{Context: session.AccessContext{Assignment: &raw}, Entries: map[string]servicecredentials.Entry{"provider": {Config: servicecredentials.Config{Protocol: agentcontract.Version}}}}
	owned, err := s.sandboxOwner("marketing-native", state)
	if err != nil || owned == nil || owned.Workspace != workspace || owned.Role != "mark" || owned.Dialog != dialog || len(owned.Reader) != 64 {
		t.Fatal(owned, err)
	}
	if _, err = s.sandboxOwner("counsel", state); err == nil {
		t.Fatal("accepted another role")
	}
	scope.Workspace = uuid.NewString()
	if _, err = s.sandboxOwner("marketing-native", state); err == nil {
		t.Fatal("accepted another workspace")
	}
	scope.Workspace = workspace
	delete(state.Entries, "provider")
	if _, err = s.sandboxOwner("marketing-native", state); err == nil {
		t.Fatal("accepted missing grant")
	}
}
