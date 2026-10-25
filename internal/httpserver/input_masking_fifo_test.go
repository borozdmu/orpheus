//go:build integration && linux

package httpserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/store"
	"github.com/orpheus-agents/orpheus/internal/testutil"
)

func TestFIFOSecretHTTPIsolation(t *testing.T) {
	if os.Getenv("ORPHEUS_FIFO_HTTP_CHILD") != "1" {
		// The parent owns DB cleanup even if a blocked startup/handler must be killed.
		pool := testutil.Database(t)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFIFOSecretHTTPIsolation$")
		cmd.Env = append(os.Environ(), "ORPHEUS_FIFO_HTTP_CHILD=1", "ORPHEUS_FIFO_DB_URL="+pool.Config().ConnString(), "ORPHEUS_FIFO_DB_SCHEMA="+pool.Config().ConnConfig.RuntimeParams["search_path"])
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("startup/admission/metrics failed (deadline=%v): %v\n%s", ctx.Err(), err, output)
		}
		return
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "token-fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "profiles.toml")
	content := fmt.Sprintf(`[input_masker]
url="http://127.0.0.1:1"
timeout_seconds=1
token_file=%q
[templates.codex]
[profiles.default]
harness="codex"
model="fixture"
[profiles.default.auth]
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
	settings := config.DefaultSettings()
	settings.ConfigFile = path
	settings.PublicAPIKeys = []string{"key"}
	settings.EnvEncryptionKey = base64.URLEncoding.EncodeToString(make([]byte, 32))
	profiles, cipher, err := settings.Runtime(true)
	if err != nil {
		t.Fatal("secret failure stopped shared API startup", err)
	}
	poolConfig, err := pgxpool.ParseConfig(os.Getenv("ORPHEUS_FIFO_DB_URL"))
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = os.Getenv("ORPHEUS_FIFO_DB_SCHEMA")
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &store.Store{Pool: pool, Settings: settings, Profiles: profiles, Cipher: cipher}
	handler, err := Handler(s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Real TCP listeners are started only after the exact serve runtime path.
	server := httptest.NewServer(handler)
	defer server.Close()
	metrics := httptest.NewServer(serviceMetricsHandler(s.AccountLimits))
	defer metrics.Close()
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	call := func(method, url, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", uuid.NewString())
		res, err := client.Do(req)
		if err != nil {
			t.Fatal("shared listener deadline exceeded", err)
		}
		defer func() { _ = res.Body.Close() }()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(raw)
	}
	ordinary := func() {
		t.Helper()
		if code, body := call("POST", server.URL+"/api/v1/sessions", validBody); code != 202 {
			t.Fatal("ordinary admission unavailable", code, body)
		}
	}
	protected := strings.Replace(validBody, `"default"`, `"protected"`, 1)
	for _, source := range []string{fifo, dir, "/dev/zero"} {
		s.Profiles.InputMasker.TokenFile = source
		ordinary()
		before := maskingCounts(t, s)
		for range 5 {
			code, body := call("POST", server.URL+"/api/v1/sessions", protected)
			if code != 503 || !strings.Contains(body, "input_masker_unavailable") || strings.Contains(body, source) || strings.Contains(body, "hello") {
				t.Fatal("unsafe or missing protected failure", code)
			}
			report, err := s.AccountLimits(t.Context())
			if err != nil || report.InputMaskerSecretAvailable == nil || *report.InputMaskerSecretAvailable {
				t.Fatal("secret diagnostics must report unavailable", err)
			}
			code, body = call("GET", metrics.URL, "")
			if code != 200 || !strings.Contains(body, "orpheus_input_masker_secret_available 0\n") || strings.Contains(body, source) {
				t.Fatal("unsafe or unavailable metrics", code)
			}
		}
		if maskingCounts(t, s) != before {
			t.Fatal("failed protected admission or diagnostics persisted data")
		}
		var credentials, plaintext int
		if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM run_service_credentials), (SELECT count(*) FROM sessions WHERE configuration->>'input_masked' = 'true')`).Scan(&credentials, &plaintext); err != nil || credentials != 0 || plaintext != 0 {
			t.Fatal("failed protected admission created credentials or a session", err)
		}
		ordinary()
	}
}
