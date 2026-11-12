// Package modelcommand implements the shared /model command for chat connectors.
package modelcommand

import (
	"slices"
	"strings"

	api "github.com/orpheus-agents/orpheus/client"
)

// InFamily reports whether name is the base profile or one of its provisioned variants.
// Unrelated profiles can have different credentials and are never offered.
func InFamily(base, name string) bool {
	return base != "" && (name == base || strings.HasPrefix(name, base+"--"))
}

// Select returns the selected profile and reply, or an empty profile and a catalog hint.
func Select(items []api.Profile, base, current string, args []string) (profile, message string) {
	var options []string
	active := "не настроена"
	for _, p := range items {
		if !InFamily(base, p.Name) || p.Model == nil || *p.Model == "" {
			continue
		}
		effort := "default"
		if p.Codex.Effort != nil {
			effort = string(*p.Codex.Effort)
		}
		choice := *p.Model + " " + effort
		options = append(options, choice)
		if p.Name == current {
			active = choice
		}
		if len(args) == 2 && args[0] == *p.Model && args[1] == effort && (profile == "" || p.Name == base) {
			profile = p.Name
		}
	}
	if profile != "" {
		return profile, "Для новых разговоров в этом чате установлено: " + strings.Join(args, " ")
	}
	if len(options) == 0 {
		return "", "Ядро не вернуло доступных моделей для этого бота."
	}
	slices.Sort(options)
	hint := "Текущая настройка: " + active + ". Доступные сочетания модели и усилия:\n" + strings.Join(slices.Compact(options), "\n") + "\nФормат: /model <модель> <усилие>."
	if len(args) != 0 {
		hint = "Такого сочетания нет. " + hint
	}
	return "", hint
}
