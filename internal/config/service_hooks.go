package config

import (
	"encoding/base64"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/orpheus-agents/orpheus/internal/session"
)

// Service hooks are trusted server configuration. The public service descriptor
// and caller inputs deliberately have no script field.
func validateServiceBeforeRun(script string) error {
	if !utf8.ValidString(script) {
		return invalid("A service hook must be valid UTF-8.", "configuration", "hooks", "before_run")
	}
	_, err := resolveHooks(&session.HooksInput{BeforeRun: new(script)})
	return err
}

// appendServiceBeforeRun runs once during resolution, never during restoration
// or idempotent replay. ResolveServices has already sorted the selected snapshot
// by service code; unselected entries contribute no commands or environment.
func appendServiceBeforeRun(hooks session.HooksConfiguration, services []session.Service, profiles Profiles) (session.HooksConfiguration, error) {
	var scripts []string
	if hooks.BeforeRun != nil {
		scripts = append(scripts, *hooks.BeforeRun)
	}
	before := len(scripts)
	for _, selected := range services {
		script := profiles.Services[selected.Code].BeforeRun
		if script == nil {
			continue
		}
		if err := validateServiceBeforeRun(*script); err != nil {
			return hooks, err
		}
		scripts = append(scripts, *script)
	}
	if len(scripts) == before {
		return hooks, nil
	}
	if len(scripts) == 1 {
		hooks.BeforeRun = new(scripts[0])
		return hooks, nil
	}

	// Preserve each program's shebang and exact bytes. A Python installer cannot
	// be concatenated into a caller's shell program. No command is evaluated by
	// the orchestrator; the existing sandbox hook runner executes this snapshot.
	var script strings.Builder
	script.WriteString("#!/bin/sh\nset -eu\numask 077\ndirectory=$(mktemp -d \"${TMPDIR:-/tmp}/orpheus-service-hooks.XXXXXXXX\")\ntrap 'rm -rf -- \"$directory\"' EXIT\n")
	for index, text := range scripts {
		file := "\"$directory/" + strconv.Itoa(index) + "\""
		script.WriteString("printf '%s' '")
		script.WriteString(base64.StdEncoding.EncodeToString([]byte(text)))
		script.WriteString("' | base64 -d >" + file + "\nchmod 700 " + file + "\n" + file + "\n")
	}
	if script.Len() > 65536 {
		return hooks, invalid("Combined before_run hooks exceed 65536 bytes.", "configuration", "hooks", "before_run")
	}
	hooks.BeforeRun = new(script.String())
	return hooks, nil
}
