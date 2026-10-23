package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInputMaskerConfiguration(t *testing.T) {
	const profile = "[templates.codex]\n[profiles.p]\nharness='codex'\nmodel='m'\nmask_input=true\n[profiles.p.auth]\nmode='api_key'\napi_key_env='KEY'\n"
	for _, address := range []string{"http://127.0.0.1:3101", "https://masker.internal/prefix"} {
		p, err := ReadProfiles(strings.NewReader("[input_masker]\nurl='" + address + "'\ntimeout_seconds=30\n" + profile))
		if err != nil || !p.Profiles["p"].MaskInput || p.InputMasker.URL != address || p.InputMasker.TimeoutSeconds != 30 {
			t.Fatal("masker configuration lost", err)
		}
	}
	for _, settings := range []string{
		"", "[input_masker]\n", "[input_masker]\nurl='http://localhost'\n",
		"[input_masker]\nurl='http://localhost'\ntimeout_seconds=0\n",
		"[input_masker]\nurl='http://localhost'\ntimeout_seconds=301\n",
		"[input_masker]\nurl='http://localhost'\ntimeout_seconds=-1\n",
	} {
		if _, err := ReadProfiles(strings.NewReader(settings + profile)); err == nil {
			t.Fatal("accepted missing or unbounded masker", settings)
		}
	}
	for _, address := range []string{"file:///tmp/masker", "http:///path", "http://user:password@localhost", "http://localhost?secret=value", "http://localhost?", "http://localhost#"} {
		if _, err := ReadProfiles(strings.NewReader("[input_masker]\nurl='" + address + "'\ntimeout_seconds=2\n" + profile)); err == nil {
			t.Fatal("invalid masker URL accepted", address)
		}
	}
	p, err := ReadProfiles(strings.NewReader(strings.Replace(profile, "mask_input=true\n", "", 1)))
	if err != nil || p.Profiles["p"].MaskInput || p.InputMasker.URL != "" {
		t.Fatal("masking enabled by default", err)
	}
}

func TestInputMaskerTokenSources(t *testing.T) {
	t.Setenv("FIXTURE_MASKER_TOKEN", "synthetic-only-token")
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("synthetic-only-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, settings := range []InputMasker{
		{TokenEnv: "FIXTURE_MASKER_TOKEN"}, {TokenFile: path},
	} {
		token, err := settings.Token()
		if err != nil || token != "synthetic-only-token" {
			t.Fatal("token source failed", err)
		}
		raw, err := json.Marshal(settings)
		if err != nil || bytes.Contains(raw, []byte(token)) {
			t.Fatal("token exposed in config serialization")
		}
		profiles := Profiles{InputMasker: settings}
		if settings.TokenEnv != "" && slices.Contains(profiles.EnvironmentAllowlist([]string{settings.TokenEnv, "SAFE_ENV"}), settings.TokenEnv) {
			t.Fatal("masker credential may reach sandbox")
		}
	}
	for _, settings := range []InputMasker{{TokenEnv: "MISSING_MASKER_FIXTURE"}, {TokenFile: path + "-missing"}, {TokenEnv: "FIXTURE_MASKER_TOKEN", TokenFile: path}} {
		if token, err := settings.Token(); err == nil || token != "" || strings.Contains(err.Error(), "synthetic-only-token") {
			t.Fatal("bad token source accepted or exposed")
		}
	}
	t.Setenv("FIXTURE_MASKER_TOKEN", "bad\nheader")
	if token, err := (InputMasker{TokenEnv: "FIXTURE_MASKER_TOKEN"}).Token(); err == nil || token != "" {
		t.Fatal("header injection accepted")
	}
	if err := os.WriteFile(path, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if token, err := (InputMasker{TokenFile: path}).Token(); err == nil || token != "" {
		t.Fatal("empty token file accepted")
	}
}

func TestInputMaskerHTTPTokenWarning(t *testing.T) {
	t.Setenv("FIXTURE_MASKER_TOKEN", "synthetic-only-token")
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct {
		url         string
		token, warn bool
	}{
		{"http://masker.internal", true, true}, {"http://192.168.1.2:3001", true, true},
		{"http://127.0.0.1:3001", true, false}, {"http://[::1]:3001", true, false},
		{"http://localhost:3001", true, false}, {"https://masker.internal", true, false},
		{"http://masker.internal", false, false},
	} {
		var output bytes.Buffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
		settings := InputMasker{URL: tc.url, TimeoutSeconds: 1}
		if tc.token {
			settings.TokenEnv = "FIXTURE_MASKER_TOKEN"
		}
		if err := settings.runtime(); err != nil {
			t.Fatal(err)
		}
		if (output.Len() > 0) != tc.warn || strings.Contains(output.String(), "synthetic-only-token") || strings.Contains(output.String(), "masker.internal") {
			t.Fatal("missing or unsafe startup warning", tc.url)
		}
	}
}
