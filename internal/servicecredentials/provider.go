// Package servicecredentials implements the server-only issue/renew protocol.
// It never receives prompts, messages, model output, or source data.
package servicecredentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/agentcontract"
	"github.com/orpheus-agents/orpheus/internal/session"
)

// Config is trusted server configuration, frozen in encrypted run state.
// The response cannot select a file path, environment variable or command.
type AudienceConnector struct {
	Endpoint  string `toml:"endpoint" json:"endpoint"`
	SecretEnv string `toml:"secret_env" json:"secret_env"`
}
type Config struct {
	AudienceConnectors map[string]AudienceConnector `toml:"audience_connectors" json:"audience_connectors,omitzero"`
	Protocol           string                       `toml:"protocol" json:"protocol,omitzero"`
	AudienceEndpoint   string                       `toml:"audience_endpoint" json:"audience_endpoint,omitzero"`
	AudienceSecretEnv  string                       `toml:"audience_secret_env" json:"audience_secret_env,omitzero"`
	Endpoint           string                       `toml:"endpoint" json:"endpoint"`
	SecretEnv          string                       `toml:"secret_env" json:"secret_env"`
	ContextFields      []string                     `toml:"context_fields" json:"context_fields"`
	TokenFile          string                       `toml:"token_file" json:"token_file"`
}

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
var tokenPath = regexp.MustCompile(`^~/\.[a-z][a-z0-9_-]*/run\.token$`)
var projection = []string{"subject", "scope_context", "context_observed_at"}

func (c Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !envName.MatchString(c.SecretEnv) || !tokenPath.MatchString(c.TokenFile) {
		return errors.New("invalid credentials provider configuration")
	}
	if c.Protocol != "" && c.Protocol != agentcontract.Version {
		return errors.New("unsupported credentials protocol")
	}
	if c.Protocol == agentcontract.Version {
		configs := []AudienceConnector{}
		if len(c.AudienceConnectors) > 0 {
			if c.AudienceEndpoint != "" || c.AudienceSecretEnv != "" || len(c.AudienceConnectors) > 64 {
				return errors.New("invalid connector routing")
			}
			for source, connector := range c.AudienceConnectors {
				if source == "" || len(source) > 256 {
					return errors.New("invalid connector identity")
				}
				configs = append(configs, connector)
			}
		} else {
			configs = append(configs, AudienceConnector{Endpoint: c.AudienceEndpoint, SecretEnv: c.AudienceSecretEnv})
		}
		for _, connector := range configs {
			audience, err := url.Parse(connector.Endpoint)
			if err != nil || audience.Scheme != "https" || audience.Hostname() == "" || audience.User != nil || audience.RawQuery != "" || audience.Fragment != "" || !envName.MatchString(connector.SecretEnv) || len(c.ContextFields) != 0 {
				return errors.New("invalid connector confirmation configuration")
			}
		}
	} else if c.AudienceEndpoint != "" || c.AudienceSecretEnv != "" || len(c.AudienceConnectors) > 0 {
		return errors.New("confirmation requires corporate assignment protocol")
	}
	seen := map[string]bool{}
	for _, field := range c.ContextFields {
		if !slices.Contains(projection, field) || seen[field] {
			return errors.New("provider context projection must contain unique fields from subject, scope_context and context_observed_at")
		}
		seen[field] = true
	}
	return nil
}

type Grant struct {
	CredentialID string    `json:"credential_id"`
	AccessToken  string    `json:"access_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	RunExpiresAt time.Time `json:"run_expires_at"`
	ScopeKey     string    `json:"scope_key"`
}

// Providers and workers run on different hosts. This tolerance only bounds a
// future timestamp; it never extends an expired grant or changes its deadline.
const maxProviderClockSkew = 5 * time.Second

// Admission includes the provider's bounded, fresh audience projection.
// Leave room for its internal RPC deadline; caller cancellation still wins.
const providerRequestTimeout = 60 * time.Second

func (g Grant) validLifetime(now time.Time) bool {
	return now.Before(g.ExpiresAt) && !g.ExpiresAt.After(g.RunExpiresAt) &&
		!g.ExpiresAt.After(now.Add(300*time.Second+maxProviderClockSkew)) &&
		!g.RunExpiresAt.After(now.Add(1200*time.Second+maxProviderClockSkew))
}

type Entry struct {
	Config Config `json:"config"`
	Grant  Grant  `json:"grant"`
}
type State struct {
	Context session.AccessContext `json:"context"`
	Entries map[string]Entry      `json:"entries"`
}
type Client struct {
	HTTP *http.Client
	// Shared servers resolve issuer keys in the authenticated tenant's configuration.
	LookupSecret func(string) string
}

func (c Client) request(ctx context.Context, cfg Config, body any) ([]byte, error) {
	fail := func() ([]byte, error) {
		return nil, session.Problem(503, "service_credentials_unavailable", "Service credentials are unavailable.")
	}
	if cfg.Validate() != nil {
		return fail()
	}
	lookup := c.LookupSecret
	if lookup == nil {
		lookup = os.Getenv
	}
	secret := lookup(cfg.SecretEnv)
	if len(secret) < 32 || strings.ContainsAny(secret, "\r\n") {
		return fail()
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, providerRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return fail()
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	// Never forward the server key through redirects or environment proxies.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: providerRequestTimeout}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return fail()
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusUnauthorized:
		return nil, session.Problem(403, "service_access_denied", "Provider denied access.")
	case http.StatusConflict:
		return nil, session.Problem(409, "service_credentials_conflict", "Invocation context conflicts with the existing grant.")
	default:
		return fail()
	}
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return fail()
	}
	return raw, nil
}
func (c Client) call(ctx context.Context, cfg Config, body any) (Grant, error) {
	var out Grant
	raw, err := c.request(ctx, cfg, body)
	if err != nil {
		return out, err
	}
	fail := func() (Grant, error) {
		return Grant{}, session.Problem(503, "service_credentials_unavailable", "Service credentials are unavailable.")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&out); err != nil {
		return fail()
	}
	if err = d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fail()
	}
	now := time.Now()
	if len(out.CredentialID) == 0 || len(out.CredentialID) > 256 || len(out.ScopeKey) == 0 || len(out.ScopeKey) > 1024 || len(out.AccessToken) == 0 || len(out.AccessToken) > 8192 || strings.ContainsAny(out.AccessToken, "\x00\r\n") || !out.validLifetime(now) {
		return fail()
	}
	return out, nil
}
func (c Client) Issue(ctx context.Context, cfg Config, service string, access session.AccessContext) (Grant, error) {
	if cfg.Protocol == agentcontract.Version {
		a, witness, err := c.confirm(ctx, cfg, access)
		if err != nil {
			return Grant{}, err
		}
		return c.call(ctx, cfg, map[string]any{"operation": "issue_assignment", "request_id": uuid.New(), "service": service, "invocation_id": access.InvocationID, "assignment": map[string]any{"schema": "corporate-access.v1", "audience": a.Audience, "invocation_id": a.InvocationID, "agent": a.Agent, "author": a.Author, "readers": a.Readers, "provider_context": a.ProviderContext, "confirmation": map[string]any{"observed_at": a.Confirmation.ObservedAt, "digest": a.Confirmation.Digest}}, "context_observed_at": witness.ObservedAt})
	}
	// Keep the provider's projection separate from core-only run authority.
	// In particular, allowed_input_subjects never leaves Orpheus.
	body := map[string]any{"operation": "issue", "request_id": uuid.New(), "service": service, "invocation_id": access.InvocationID}
	for _, field := range cfg.ContextFields {
		switch field {
		case "subject":
			body[field] = access.Subject
		case "scope_context":
			body[field] = access.ScopeContext
		case "context_observed_at":
			body[field] = access.ContextObservedAt
		}
	}
	return c.call(ctx, cfg, body)
}
func (c Client) Renew(ctx context.Context, cfg Config, service string, invocation uuid.UUID, previous Grant) (Grant, error) {
	// Derived from the persisted lease: a lost response retries the same operation.
	requestID := uuid.NewSHA1(invocation, []byte(service+":"+previous.CredentialID+":"+previous.ExpiresAt.Format(time.RFC3339Nano)))
	next, err := c.call(ctx, cfg, struct {
		Operation    string    `json:"operation"`
		RequestID    uuid.UUID `json:"request_id"`
		Service      string    `json:"service"`
		InvocationID uuid.UUID `json:"invocation_id"`
		CredentialID string    `json:"credential_id"`
	}{"renew", requestID, service, invocation, previous.CredentialID})
	if err != nil {
		return Grant{}, err
	}
	if next.AccessToken != previous.AccessToken || next.CredentialID != previous.CredentialID || next.ScopeKey != previous.ScopeKey || !next.RunExpiresAt.Equal(previous.RunExpiresAt) || next.ExpiresAt.Before(previous.ExpiresAt) {
		return Grant{}, session.Problem(503, "service_credentials_unavailable", "Provider changed the frozen grant.")
	}
	return next, nil
}
func (s State) Scopes() map[string]string {
	out := map[string]string{}
	for code, e := range s.Entries {
		out[code] = e.Grant.ScopeKey
	}
	return out
}
func (s State) Expired(now time.Time) bool {
	for _, e := range s.Entries {
		if !now.Before(e.Grant.ExpiresAt) || !now.Before(e.Grant.RunExpiresAt) {
			return true
		}
	}
	return false
}

func (c Client) confirm(ctx context.Context, cfg Config, access session.AccessContext) (agentcontract.Assignment, agentcontract.Witness, error) {
	var a agentcontract.Assignment
	if cfg.Validate() != nil {
		return a, agentcontract.Witness{}, session.Problem(503, "service_credentials_unavailable", "Invalid stored provider configuration.")
	}
	denied := session.Problem(403, "audience_unconfirmed", "Connector did not confirm the run audience.")
	if access.Assignment == nil {
		return a, agentcontract.Witness{}, denied
	}
	a, err := agentcontract.Decode(*access.Assignment)
	if err != nil || a.InvocationID != access.InvocationID || a.Author.Namespace != access.Subject.Namespace || a.Author.ID != access.Subject.ID {
		return a, agentcontract.Witness{}, denied
	}
	lookup := c.LookupSecret
	if lookup == nil {
		lookup = os.Getenv
	}
	client := c.HTTP
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport, Timeout: providerRequestTimeout}
	}
	connector := AudienceConnector{Endpoint: cfg.AudienceEndpoint, SecretEnv: cfg.AudienceSecretEnv}
	if len(cfg.AudienceConnectors) > 0 {
		var ok bool
		connector, ok = cfg.AudienceConnectors[a.Delivery.Connector+"/"+a.Delivery.Source]
		if !ok {
			return a, agentcontract.Witness{}, denied
		}
	}
	witness, err := agentcontract.Confirm(ctx, client, connector.Endpoint, lookup(connector.SecretEnv), a)
	if err != nil {
		return a, witness, denied
	}
	return a, witness, nil
}
func (c Client) RenewConfirmed(ctx context.Context, cfg Config, service string, access session.AccessContext, previous Grant) (Grant, error) {
	if cfg.Protocol == agentcontract.Version {
		if _, _, err := c.confirm(ctx, cfg, access); err != nil {
			return Grant{}, err
		}
	}
	return c.Renew(ctx, cfg, service, access.InvocationID, previous)
}
func (c Client) Revoke(ctx context.Context, cfg Config, service string, invocation uuid.UUID, previous Grant) error {
	if cfg.Protocol != agentcontract.Version {
		return nil
	}
	raw, err := c.request(ctx, cfg, map[string]any{"operation": "revoke", "request_id": uuid.NewSHA1(invocation, []byte("revoke:"+service+":"+previous.CredentialID)), "service": service, "invocation_id": invocation, "credential_id": previous.CredentialID})
	if err != nil {
		return err
	}
	var result struct {
		Revoked bool `json:"revoked"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Revoked {
		return session.Problem(503, "service_credentials_unavailable", "Revocation is unconfirmed.")
	}
	return nil
}

func (c Client) Confirm(ctx context.Context, cfg Config, access session.AccessContext) error {
	_, _, err := c.confirm(ctx, cfg, access)
	return err
}
