// Package inputmasker calls the trusted strict masker without exposing its
// local manifests or forwarding originals through redirects or environment proxies.
package inputmasker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("input masker unavailable")

// Mask is atomic: callers receive all segments in order, or no result.
func Mask(ctx context.Context, endpoint string, timeout time.Duration, texts []string, token string) ([]string, error) {
	if endpoint == "" || timeout <= 0 {
		return nil, ErrUnavailable
	}
	raw, err := json.Marshal(struct {
		Texts  []string `json:"texts"`
		Locale string   `json:"locale"`
	}{texts, "ru"})
	if err != nil {
		return nil, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/mask/segments", bytes.NewReader(raw))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	const maxResponseBytes = 16 << 20
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return nil, ErrUnavailable
	}
	var out struct {
		Texts []*string `json:"masked_texts"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Texts) != len(texts) {
		return nil, ErrUnavailable
	}
	masked := make([]string, len(texts))
	for i, value := range out.Texts {
		if value == nil || (*value == "" && texts[i] != "") {
			return nil, ErrUnavailable
		}
		masked[i] = *value
	}
	return masked, nil
}
