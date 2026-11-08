// This binary consumes only single-run, reference-only assignments in an
// independently credentialed database. It has no model or tool credentials.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/google/uuid"
)

type workflow struct {
	Namespace string `json:"namespace"`
	Profile   string `json:"profile"`
	Template  string `json:"template"`
	Backend   string `json:"backend"`
	Timeout   int    `json:"run_timeout_seconds"`
	MaxTokens int64  `json:"max_session_tokens"`
	Capacity  int    `json:"max_concurrent_sessions"`
}

func readWorkflow(path string) (workflow, error) {
	var w workflow
	f, err := os.Open(path)
	if err != nil {
		return w, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&w); err != nil {
		return w, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) ||
		w.Namespace == "" || w.Profile == "" || w.Template == "" || w.Backend != "cxagent-unix-v1" ||
		w.Timeout <= 0 || w.MaxTokens <= 0 || w.Capacity <= 0 {
		return w, errors.New("invalid workflow")
	}
	return w, nil
}

var digest = regexp.MustCompile("^[0-9a-f]{64}$")

type binding struct {
	Fingerprint string    `json:"fingerprint"`
	Run         uuid.UUID `json:"run_id"`
	Session     uuid.UUID `json:"session_id"`
}
type assignment struct {
	id string
	binding
	action string
}
type counters struct {
	Input     *int64 `json:"input_tokens"`
	Output    *int64 `json:"output_tokens"`
	Cached    *int64 `json:"cached_input_tokens"`
	Reasoning *int64 `json:"reasoning_output_tokens"`
}
type receipt struct {
	ID    string    `json:"id"`
	State string    `json:"state"`
	Usage *counters `json:"usage,omitempty"`
}

func (r receipt) valid(id string) bool {
	if id != r.ID {
		return false
	}
	switch r.State {
	case "queued", "running", "cancelled", "unknown", "rejected":
		return r.Usage == nil
	case "done":
		u := r.Usage
		return u != nil && u.Input != nil && u.Output != nil && u.Cached != nil &&
			*u.Input >= 0 && *u.Input <= 1<<52 && *u.Output >= 0 && *u.Output <= 1<<52 &&
			*u.Cached >= 0 && *u.Cached <= *u.Input &&
			(u.Reasoning == nil || *u.Reasoning >= 0 && *u.Reasoning <= *u.Output)
	}
	return false
}

type backend struct{ client *http.Client }

func unixBackend(socket string) backend {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return backend{client: &http.Client{Transport: transport, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (b backend) exchange(ctx context.Context, a assignment) (receipt, error) {
	path := "http://cxagent/v1/executions/" + a.id
	if a.action == "cancel" {
		path += "/cancel"
	}
	method := http.MethodPost
	var body io.Reader
	if a.action == "get" {
		method = http.MethodGet
	} else {
		raw, err := json.Marshal(a.binding)
		if err != nil {
			return receipt{}, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return receipt{}, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("X-CX-Fingerprint", a.Fingerprint)
		req.Header.Set("X-CX-Session-ID", a.Session.String())
		req.Header.Set("X-CX-Run-ID", a.Run.String())
	}
	res, err := b.client.Do(req)
	if err != nil {
		return receipt{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil || len(raw) > 8192 {
		return receipt{}, errors.New("invalid backend reply")
	}
	if res.StatusCode != 200 && res.StatusCode != 202 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &failure) == nil &&
			(res.StatusCode == 404 && failure.Error == "job_not_found" ||
				res.StatusCode == 409 && failure.Error == "job_binding_conflict") {
			return receipt{ID: a.id, State: "rejected"}, nil
		}
		return receipt{}, errors.New("backend unavailable or invalid binding response")
	}
	var r receipt
	if json.Unmarshal(raw, &r) != nil || !r.valid(a.id) {
		return receipt{}, errors.New("invalid backend receipt")
	}
	return r, nil
}
