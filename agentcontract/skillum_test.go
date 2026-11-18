package agentcontract

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSkillumPrivateDialog(t *testing.T) {
	workspace, dialog := uuid.NewString(), uuid.NewString()
	person := Principal{Namespace: "skillum:" + workspace, ID: "12", PrincipalID: "34"}
	task := Assignment{
		Schema: Version, InvocationID: uuid.New(), Agent: "mark", Author: person,
		Readers: []Principal{person}, Text: "hello",
		Delivery:     Route{Connector: "skillum", Source: workspace, Chat: dialog, Message: uuid.NewString()},
		Audience:     Audience{Kind: "exact", ChannelType: "skillum-private", Complete: true, Instance: person.Namespace, ChannelID: dialog},
		Confirmation: Confirmation{ObservedAt: time.Now()},
	}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Assignment){
		"wrong workspace": func(a *Assignment) { a.Delivery.Source = uuid.NewString() },
		"wrong transport": func(a *Assignment) { a.Delivery.Connector = "telegram" },
		"wrong audience":  func(a *Assignment) { a.Audience.ChannelType = "D" },
		"extra reader": func(a *Assignment) {
			a.Readers = append(a.Readers, Principal{Namespace: person.Namespace, ID: "13", PrincipalID: "35"})
		},
		"attachment":   func(a *Assignment) { a.Attachments = []Attachment{{ID: "file"}} },
		"group thread": func(a *Assignment) { a.Delivery.Thread = "group" },
		"incomplete":   func(a *Assignment) { a.Audience.Complete = false },
	} {
		t.Run(name, func(t *testing.T) {
			changed := task
			change(&changed)
			if changed.Validate() == nil {
				t.Fatal("accepted invalid private dialog")
			}
		})
	}
}
