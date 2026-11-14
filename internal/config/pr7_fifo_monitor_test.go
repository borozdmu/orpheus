//go:build linux

package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func pr7FIFO(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func pr7ExpectPromptReturn(t *testing.T, path string, wantErr bool, call func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		if (err != nil) != wantErr {
			t.Fatalf("returned err=%v; wantErr=%v", err, wantErr)
		}
	case <-time.After(300 * time.Millisecond):
		// Release only our FIFO reader so the probe never leaves a blocked goroutine.
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0600)
		if err != nil {
			t.Fatalf("call blocked at least 300ms; releasing FIFO writer failed: %v", err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			t.Fatalf("call blocked at least 300ms on FIFO with no writer; returned only after writer connected (err=%v)", err)
		case <-time.After(time.Second):
			t.Fatal("call stayed blocked even after releasing FIFO")
		}
	}
}

func TestPR7FIFOSecretTokenMustReturn(t *testing.T) {
	path := pr7FIFO(t)
	m := InputMasker{TokenFile: path}
	pr7ExpectPromptReturn(t, path, true, func() error {
		_, err := m.Token()
		return err
	})
}

func TestPR7FIFOSecretSettingsRuntimeMustReturn(t *testing.T) {
	fifo := pr7FIFO(t)
	path := filepath.Join(t.TempDir(), "profiles.toml")
	content := fmt.Sprintf(`[input_masker]
url="http://127.0.0.1:3101"
timeout_seconds=1
token_file=%q
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
`, fifo)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	s.ConfigFile = path
	s.PublicAPIKeys = []string{"synthetic-key"}
	s.EnvEncryptionKey = base64.URLEncoding.EncodeToString(make([]byte, 32))
	if _, _, err := s.Runtime(false); err != nil {
		t.Fatal("worker runtime setup failed", err)
	}
	pr7ExpectPromptReturn(t, fifo, false, func() error {
		_, _, err := s.Runtime(true)
		return err
	})
}
