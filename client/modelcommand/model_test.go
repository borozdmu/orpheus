package modelcommand

import (
	"strings"
	"testing"

	api "github.com/orpheus-agents/orpheus/client"
)

func TestModelCatalogIsScopedToWorkflow(t *testing.T) {
	items := []api.Profile{
		{Name: "mark", Model: new("alpha"), Codex: api.CodexProfile{Effort: new(api.CodexProfileEffortHigh)}},
		{Name: "mark--fast", Model: new("beta"), Codex: api.CodexProfile{Effort: new(api.CodexProfileEffortLow)}},
		{Name: "finance", Model: new("other")},
	}
	name, msg := Select(items, "mark", "mark--fast", nil)
	if name != "" || !strings.Contains(msg, "Текущая настройка: beta low") || strings.Contains(msg, "other") {
		t.Fatal(name, msg)
	}
	name, _ = Select(items, "mark", "mark", []string{"beta", "low"})
	if name != "mark--fast" {
		t.Fatal(name)
	}
	for _, args := range [][]string{{"beta", "high"}, {"other", "default"}, {"beta"}} {
		if name, _ := Select(items, "mark", "mark", args); name != "" {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestDuplicatePairUsesBaseAndAppearsOnce(t *testing.T) {
	base := api.Profile{Name: "bot", Model: new("alpha")}
	variant := base
	variant.Name = "bot--alternative"
	for _, items := range [][]api.Profile{{base, variant}, {variant, base}} {
		name, _ := Select(items, "bot", "bot", []string{"alpha", "default"})
		if name != base.Name {
			t.Fatalf("selected %q", name)
		}
		_, hint := Select(items, "bot", "bot", nil)
		if strings.Count(hint, "\nalpha default\n") != 1 || strings.Count(hint, "alpha default") != 2 {
			t.Fatal(hint)
		}
	}
}
