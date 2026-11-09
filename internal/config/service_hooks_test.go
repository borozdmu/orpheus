package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/orpheus-agents/orpheus/internal/session"
)

func hookProfiles() (Profiles, session.ConfigurationInput) {
	return Profiles{
		Templates: map[string]Template{"template": {}},
		Profiles:  map[string]Profile{"default": {Harness: "codex", Model: new("model"), Auth: Auth{Mode: "api_key", APIKeyEnv: "KEY"}}},
		Services:  map[string]Service{},
	}, session.ConfigurationInput{Agent: session.AgentInput{Profile: "default"}, Sandbox: session.SandboxInput{Template: "template"}, Limits: session.Limits{RunTimeoutSeconds: 3600}}
}

func TestServiceBeforeRunConfiguration(t *testing.T) {
	const service = "[services.installer]\nname='Installer'\ndescription='Public client installer'\nbefore_run='#!/bin/sh\\necho ready\\n'\n"
	// TOML literal strings deliberately retain escapes; use a multiline literal
	// for real script text, just like an operator's service configuration.
	valid := strings.Replace(service, "'#!/bin/sh\\necho ready\\n'", "'''#!/bin/sh\necho ready\n'''", 1)
	profiles, err := ReadProfiles(strings.NewReader(serviceProfiles + valid))
	if err != nil || profiles.Services["installer"].BeforeRun == nil || *profiles.Services["installer"].BeforeRun != "#!/bin/sh\necho ready\n" {
		t.Fatal(profiles.Services, err)
	}
	for _, script := range []string{"", "echo without shebang", "#!\n", "#!/bin/sh\x00", "#!/bin/sh\r\ntrue\n", "#!/bin/sh\n" + strings.Repeat("a", 65536), "#!/bin/sh\n" + string([]byte{255})} {
		if err := validateServices(map[string]Service{"installer": {Name: "Installer", Description: "Public installer", BeforeRun: new(script)}}); err == nil {
			t.Fatal("accepted invalid trusted service hook")
		}
	}
	if _, err := ReadProfiles(strings.NewReader(serviceProfiles + "[services.installer]\nname='Installer'\ndescription='Public installer'\nbefore_run=true\n")); err == nil {
		t.Fatal("accepted non-string service hook")
	}
}

func TestSelectedServiceBeforeRunOrderAndSnapshot(t *testing.T) {
	profiles, input := hookProfiles()
	transport := "#!/bin/sh\necho transport\n"
	first := "#!/usr/bin/env python3\nprint('first')\n"
	second := "#!/bin/sh\necho second\n"
	blocked := "#!/bin/sh\necho unselected\n"
	profiles.Services = map[string]Service{
		"a":          {BeforeRun: &first, EnvFrom: []string{"FIRST"}},
		"b":          {BeforeRun: &second},
		"unselected": {BeforeRun: &blocked, EnvFrom: []string{"UNSELECTED"}},
	}
	input.Hooks = &session.HooksInput{BeforeRun: &transport}
	input.Sandbox.Services = []string{"b", "a"}
	got, err := Resolve(input, profiles, nil, DefaultMaxSessionTokens)
	if err != nil {
		t.Fatal(err)
	}
	script := *got.Public.Hooks.BeforeRun
	positions := []int{}
	for _, text := range []string{transport, first, second} {
		encoded := base64.StdEncoding.EncodeToString([]byte(text))
		if strings.Count(script, encoded) != 1 {
			t.Fatal("missing or duplicate hook program")
		}
		positions = append(positions, strings.Index(script, encoded))
	}
	if !slices.IsSorted(positions) || strings.Contains(script, base64.StdEncoding.EncodeToString([]byte(blocked))) || !slices.Equal(got.Public.Sandbox.EnvFrom, []string{"FIRST"}) {
		t.Fatal("unselected service broadened hooks or references")
	}
	input.Sandbox.Services = []string{"a", "b"}
	again, err := Resolve(input, profiles, nil, DefaultMaxSessionTokens)
	if err != nil || *again.Public.Hooks.BeforeRun != script || transport != "#!/bin/sh\necho transport\n" {
		t.Fatal("resolution mutates inputs or duplicates service hooks", err)
	}
	frozen, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	transport = "#!/bin/sh\necho changed-caller\n"
	first = "#!/bin/sh\necho changed-service\n"
	after, err := json.Marshal(got)
	if err != nil || string(after) != string(frozen) {
		t.Fatal("resolved hook aliases caller or catalog", err)
	}
	changed, err := Resolve(input, profiles, nil, DefaultMaxSessionTokens)
	if err != nil || *changed.Public.Hooks.BeforeRun == script {
		t.Fatal("new session did not receive changed trusted hook", err)
	}
	public, err := json.Marshal(got.Public.Sandbox.Services)
	if err != nil || strings.Contains(string(public), "before_run") || strings.Contains(string(public), "changed-service") {
		t.Fatal("public service descriptors expose trusted commands", err)
	}
}

func TestServiceBeforeRunDefaultsAndCombinedLimit(t *testing.T) {
	profiles, input := hookProfiles()
	plain, err := Resolve(input, profiles, nil, DefaultMaxSessionTokens)
	if err != nil || plain.Public.Hooks.BeforeRun != nil {
		t.Fatal(plain, err)
	}
	transport := "#!/bin/sh\necho caller-only\n"
	input.Hooks = &session.HooksInput{BeforeRun: &transport}
	profiles.Services["unselected"] = Service{BeforeRun: new("#!/bin/sh\nexit 99\n")}
	callerOnly, err := Resolve(input, profiles, nil, DefaultMaxSessionTokens)
	if err != nil || *callerOnly.Public.Hooks.BeforeRun != transport {
		t.Fatal("caller hook caused unselected service execution", err)
	}
	transport = "changed after resolve"
	if *callerOnly.Public.Hooks.BeforeRun == transport {
		t.Fatal("legacy caller hook is not frozen")
	}
	input.Hooks = nil
	profiles.Services["a"] = Service{BeforeRun: new("#!/bin/sh\ntrue\n")}
	input.Sandbox.Services = []string{"a"}
	only, err := Resolve(input, profiles, nil, DefaultMaxSessionTokens)
	if err != nil || *only.Public.Hooks.BeforeRun != *profiles.Services["a"].BeforeRun {
		t.Fatal("single trusted program changed", err)
	}
	large := "#!/bin/sh\n#" + strings.Repeat("a", 40000) + "\n"
	profiles.Services["a"] = Service{BeforeRun: &large}
	profiles.Services["b"] = Service{BeforeRun: &large}
	input.Sandbox.Services = []string{"a", "b"}
	if _, err := Resolve(input, profiles, nil, DefaultMaxSessionTokens); err == nil {
		t.Fatal("oversized composed hook accepted")
	}
}

func TestServiceBeforeRunExecutesMixedInterpretersAndStopsOnFailure(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("mixed interpreter test needs Python in the development tools image")
	}
	for name, fail := range map[string]bool{"ordered": false, "fail-fast": true} {
		t.Run(name, func(t *testing.T) {
			profiles, input := hookProfiles()
			transport := "#!/bin/sh\nprintf 'transport\\n' >> \"$EVENTS\"\n"
			first := "#!/usr/bin/env python3\nimport os\nwith open(os.environ['EVENTS'], 'a') as stream: stream.write('first\\n')\n"
			if fail {
				first += "raise SystemExit(23)\n"
			}
			profiles.Services["a"] = Service{BeforeRun: &first}
			profiles.Services["b"] = Service{BeforeRun: new("#!/bin/sh\nprintf 'second\\n' >> \"$EVENTS\"\n")}
			profiles.Services["unselected"] = Service{BeforeRun: new("#!/bin/sh\nexit 99\n")}
			input.Hooks = &session.HooksInput{BeforeRun: &transport}
			input.Sandbox.Services = []string{"b", "a"}
			resolved, err := Resolve(input, profiles, nil, DefaultMaxSessionTokens)
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			file, events := filepath.Join(directory, "hook"), filepath.Join(directory, "events")
			if err := os.WriteFile(file, []byte(*resolved.Public.Hooks.BeforeRun), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "/bin/sh", file)
			command.Env = append(os.Environ(), "EVENTS="+events)
			output, runErr := command.CombinedOutput()
			if (runErr != nil) != fail {
				t.Fatalf("unexpected hook result: %v %s", runErr, output)
			}
			got, err := os.ReadFile(events)
			want := "transport\nfirst\nsecond\n"
			if fail {
				want = "transport\nfirst\n"
			}
			if err != nil || string(got) != want {
				t.Fatal(string(got), err)
			}
		})
	}
}
