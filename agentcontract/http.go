package agentcontract

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

type Witness struct {
	InvocationID string    `json:"invocation_id"`
	Digest       string    `json:"digest"`
	ObservedAt   time.Time `json:"observed_at"`
}

// Handler rechecks the signed, frozen assignment through the owning connector.
// No client may substitute a route, author or roster and obtain a fresh witness.
func Handler(secret func() string, confirm func(context.Context, Assignment) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		key := secret()
		if len(key) < 32 || !hmac.Equal([]byte(r.Header.Get("Authorization")), []byte("Bearer "+key)) {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		var a Assignment
		d := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
		d.DisallowUnknownFields()
		if r.Method != "POST" || d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF || a.Verify(key, time.Now()) != nil || confirm(r.Context(), a) != nil {
			http.Error(w, "audience unconfirmed", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Witness{a.InvocationID.String(), a.Confirmation.Digest, time.Now().UTC()})
	})
}
func Confirm(ctx context.Context, client *http.Client, endpoint, secret string, a Assignment) (Witness, error) {
	denied := errors.New("audience confirmation unavailable")
	var out Witness
	if len(secret) < 32 {
		return out, denied
	}
	b, err := json.Marshal(a)
	if err != nil {
		return out, denied
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
	if err != nil {
		return out, denied
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return out, denied
	}
	defer func() { _ = resp.Body.Close() }()
	d := json.NewDecoder(io.LimitReader(resp.Body, 4097))
	d.DisallowUnknownFields()
	if resp.StatusCode != 200 || d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || out.InvocationID != a.InvocationID.String() || out.Digest != a.Digest() || time.Since(out.ObservedAt) < -5*time.Second || time.Since(out.ObservedAt) > time.Minute {
		return Witness{}, denied
	}
	return out, nil
}
