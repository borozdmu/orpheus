package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/httpserver"
	"github.com/orpheus-agents/orpheus/internal/servicecredentials"
	"github.com/orpheus-agents/orpheus/internal/store"
)

// Each entry is operator-owned and mounted read-only. The caller cannot select
// a database, provider, or catalog through a URL, header hint, or session ID.
type tenantSettings struct {
	APIKeys          []string          `json:"api_keys"`
	Database         string            `json:"database_url"`
	Encryption       string            `json:"encryption_key"`
	IssuerKeys       map[string]string `json:"issuer_keys"`
	ProviderCA       string            `json:"provider_ca_file"`
	MaxSessions      int               `json:"max_concurrent_sessions"`
	MaxSessionTokens int64             `json:"default_max_session_tokens"`
	Allowlist        []string          `json:"harness_env_allowlist"`
}

type tenantRoutes map[[sha256.Size]byte]http.Handler

func (routes tenantRoutes) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	values := r.Header.Values("Authorization")
	if len(values) == 1 {
		kind, key, ok := strings.Cut(values[0], " ")
		if ok && strings.EqualFold(kind, "Bearer") {
			if handler := routes[sha256.Sum256([]byte(key))]; handler != nil {
				handler.ServeHTTP(w, r)
				return
			}
		}
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func loadTenant(dir string) (tenantSettings, error) {
	var tenant tenantSettings
	f, err := os.Open(filepath.Join(dir, "settings.json"))
	if err != nil {
		return tenant, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return tenant, errors.New("invalid permissions or size for tenant settings")
	}
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&tenant); err != nil {
		return tenant, errors.New("invalid tenant settings")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return tenant, errors.New("trailing tenant configuration")
	}
	if tenant.Database == "" || len(tenant.APIKeys) == 0 {
		return tenant, errors.New("tenant requires database and API keys")
	}
	for _, key := range tenant.APIKeys {
		if len(key) < 32 || strings.ContainsAny(key, " \n\r\t") {
			return tenant, errors.New("invalid tenant API key")
		}
	}
	return tenant, nil
}

func tenantIssuerClient(tenant tenantSettings, profiles config.Profiles) (servicecredentials.Client, error) {
	client := servicecredentials.Client{LookupSecret: func(key string) string { return tenant.IssuerKeys[key] }}
	for _, svc := range profiles.Services {
		if svc.Provider != nil && len(client.LookupSecret(svc.Provider.SecretEnv)) < 32 {
			return client, errors.New("tenant issuer binding unavailable")
		}
	}
	if tenant.ProviderCA == "" {
		return client, nil
	}
	pem, err := os.ReadFile(tenant.ProviderCA)
	if err != nil {
		return client, errors.New("tenant provider CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return client, errors.New("invalid tenant provider CA")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	client.HTTP = &http.Client{Transport: transport, Timeout: time.Minute}
	return client, nil
}

func serveShared(ctx context.Context, root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	routes := tenantRoutes{}
	used := map[string]bool{}
	streams, stop := context.WithCancel(context.Background())
	defer stop()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		t, err := loadTenant(dir)
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
		settings := config.DefaultSettings()
		settings.DatabaseURL, settings.EnvEncryptionKey, settings.PublicAPIKeys = t.Database, t.Encryption, t.APIKeys
		settings.ConfigFile = filepath.Join(dir, "orpheus.toml")
		settings.HarnessEnvAllowlist = t.Allowlist
		if t.MaxSessions > 0 {
			settings.MaxConcurrentSessions = t.MaxSessions
		}
		if t.MaxSessionTokens > 0 {
			settings.DefaultMaxSessionTokens = t.MaxSessionTokens
		}
		profiles, cipher, err := settings.Runtime(true)
		if err != nil {
			return fmt.Errorf("%s: invalid core settings", entry.Name())
		}
		pg, err := config.DatabasePoolConfig(t.Database)
		if err != nil {
			return errors.New("invalid tenant database")
		}
		databaseID := fmt.Sprintf("%s:%d/%s", pg.ConnConfig.Host, pg.ConnConfig.Port, pg.ConnConfig.Database)
		if used[databaseID] {
			return errors.New("two agents cannot own the same database")
		}
		used[databaseID] = true
		pool, err := pgxpool.NewWithConfig(ctx, pg)
		if err != nil {
			return errors.New("failed to initialize tenant database")
		}
		defer pool.Close()
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.Ping(probe)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: database unavailable", entry.Name())
		}
		issuer, err := tenantIssuerClient(t, profiles)
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
		storage := &store.Store{Pool: pool, Settings: settings, Profiles: profiles, Cipher: cipher, CredentialClient: issuer}
		handler, err := httpserver.Handler(storage, streams)
		if err != nil {
			return fmt.Errorf("%s: API initialization failed", entry.Name())
		}
		for _, key := range t.APIKeys {
			hash := sha256.Sum256([]byte(key))
			if routes[hash] != nil {
				return errors.New("ambiguous API key across tenant routes")
			}
			routes[hash] = handler
		}
	}
	if len(routes) == 0 {
		return errors.New("no tenant routes configured")
	}
	api := &http.Server{Addr: ":8000", Handler: routes, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	api.RegisterOnShutdown(stop)
	var ready atomic.Bool
	system := http.NewServeMux()
	probe := func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}
	system.HandleFunc("GET /ready", probe)
	system.HandleFunc("GET /health", probe)
	return serve(ctx, api, &http.Server{Addr: ":9100", Handler: system, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}, &ready)
}
