package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskerSecretFailureMustNotStopOrdinaryAPI(t *testing.T) {
	t.Setenv("PR7_REVIEW_MASKER_TOKEN", "")
	path := filepath.Join(t.TempDir(), "profiles.toml")
	const content = `[input_masker]
url="http://127.0.0.1:3101"
timeout_seconds=1
token_env="PR7_REVIEW_MASKER_TOKEN"
[templates.codex]
[profiles.ordinary]
harness="codex"
model="fixture"
[profiles.ordinary.auth]
mode="api_key"
api_key_env="KEY"
[profiles.protected]
harness="codex"
model="fixture"
mask_input=true
[profiles.protected.auth]
mode="api_key"
api_key_env="KEY"
`
	for _, source := range []string{`token_env="PR7_REVIEW_MASKER_TOKEN"`, `token_file="` + filepath.Join(t.TempDir(), "missing-token") + `"`} {
		t.Run(source[:9], func(t *testing.T) {
			config := strings.Replace(content, `token_env="PR7_REVIEW_MASKER_TOKEN"`, source, 1)
			if err := os.WriteFile(path, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			s := DefaultSettings()
			s.ConfigFile, s.PublicAPIKeys = path, []string{"synthetic-key"}
			s.EnvEncryptionKey = base64.URLEncoding.EncodeToString(make([]byte, 32))
			if _, _, err := s.Runtime(false); err != nil {
				t.Fatal("worker setup", err)
			}
			if _, _, err := s.Runtime(true); err != nil {
				t.Fatalf("ordinary API startup is blocked by protected profile's missing secret: %v", err)
			}

		})
	}
}
