package config

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
)

// InputMasker is an operator-only, trusted-side service. It is never included
// in the public configuration or passed to a sandbox.
type InputMasker struct {
	URL            string `toml:"url"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
	TokenEnv       string `toml:"token_env"`
	TokenFile      string `toml:"token_file"`
}

func (m InputMasker) validate() error {
	if m.TokenEnv != "" && (!envName.MatchString(m.TokenEnv) || m.TokenFile != "") {
		return errors.New("input_masker requires exactly one valid token source")
	}
	if m.URL == "" && m.TimeoutSeconds == 0 && m.TokenEnv == "" && m.TokenFile == "" {
		return nil
	}
	u, err := url.Parse(m.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(m.URL, "#") || m.TimeoutSeconds < 1 || m.TimeoutSeconds > 300 {
		return errors.New("invalid input_masker URL or timeout_seconds (1..300 required)")
	}
	return nil
}

// Token reads a credential only on the trusted side, without retaining it in
// configuration snapshots. Errors never include the source, value or OS error.
func (m InputMasker) Token() (string, error) {
	const maxTokenBytes = 4096
	var token string
	if m.TokenEnv != "" && m.TokenFile != "" {
		return "", errors.New("invalid input masker token configuration")
	}
	switch {
	case m.TokenEnv != "":
		token = os.Getenv(m.TokenEnv)
	case m.TokenFile != "":
		file, err := os.Open(m.TokenFile)
		if err != nil {
			return "", errors.New("input masker token unavailable")
		}
		defer func() { _ = file.Close() }()
		raw, err := io.ReadAll(io.LimitReader(file, maxTokenBytes+1))
		if err != nil {
			return "", errors.New("input masker token unavailable")
		}
		token = string(raw)
	default:
		return "", nil
	}
	if len(token) > maxTokenBytes {
		return "", errors.New("invalid input masker token")
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
		return "", errors.New("invalid input masker token")
	}
	return token, nil
}

func (m InputMasker) runtime() error {
	_, err := m.Token()
	if err != nil {
		return err
	}
	u, _ := url.Parse(m.URL) // ReadProfiles validated the address.
	if u != nil && u.Scheme == "http" && !net.ParseIP(u.Hostname()).IsLoopback() && !strings.EqualFold(u.Hostname(), "localhost") {
		slog.Warn("Input masker sends original text over HTTP outside loopback; configure HTTPS or protect the trusted network")
	}
	return nil
}
