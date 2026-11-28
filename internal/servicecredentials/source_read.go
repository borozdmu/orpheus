package servicecredentials

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/agentcontract"
	"github.com/orpheus-agents/orpheus/internal/session"
)

var sourceName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// SourceRead is a connector request to check a specific artifact at its next
// transfer boundary. It carries neither a provider credential nor a destination.
type SourceRead struct {
	Request, Invocation                                             uuid.UUID
	Assignment, Job, Artifact, Source, Revision, Purpose, Signature string
}

func (r SourceRead) material() []byte {
	out, _ := json.Marshal([]string{"orpheus.source-read.v1", r.Request.String(), r.Invocation.String(),
		r.Assignment, r.Job, r.Artifact, r.Source, r.Revision, r.Purpose})
	return out
}

func (r SourceRead) Digest() string {
	return fmt.Sprintf("%x", sha256.Sum256(r.material()))
}

func (r SourceRead) valid() bool {
	return r.Request != uuid.Nil && r.Invocation != uuid.Nil && hashPattern.MatchString(r.Artifact) &&
		hashPattern.MatchString(r.Assignment) && hashPattern.MatchString(r.Signature) &&
		validText(r.Job, 200) && sourceName.MatchString(r.Source) && validText(r.Revision, 128) &&
		(r.Purpose == "restore" || r.Purpose == "upload" || r.Purpose == "publish")
}

func validText(s string, limit int) bool {
	return s != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= limit &&
		strings.IndexFunc(s, func(r rune) bool { return r < 0x20 }) < 0
}

type sourceCheck struct {
	Kind     string `json:"kind"`
	Source   string `json:"source_id"`
	Revision string `json:"source_revision"`
}

// Provider's existing check_assignment contract hashes sorted, ASCII-escaped JSON.
func (c sourceCheck) digest() string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(c)
	var canonical strings.Builder
	for _, r := range strings.TrimSuffix(b.String(), "\n") {
		if r < 0x7f {
			canonical.WriteRune(r)
		} else {
			for _, unit := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&canonical, `\u%04x`, unit)
			}
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(canonical.String())))
}

func (c Client) CheckSourceRead(ctx context.Context, entry Entry, access session.AccessContext, r SourceRead) (bool, error) {
	denied := session.Problem(403, "source_read_unconfirmed", "Source access could not be confirmed.")
	if !r.valid() || access.Assignment == nil || access.InvocationID != r.Invocation || entry.Config.Protocol != agentcontract.Version {
		return false, denied
	}
	a, err := agentcontract.Decode(*access.Assignment)
	if err != nil || a.InvocationID != r.Invocation || a.Confirmation.Digest != r.Assignment ||
		a.Author.Namespace != access.Subject.Namespace || a.Author.ID != access.Subject.ID {
		return false, denied
	}
	connector := AudienceConnector{Endpoint: entry.Config.AudienceEndpoint, SecretEnv: entry.Config.AudienceSecretEnv}
	if len(entry.Config.AudienceConnectors) > 0 {
		var ok bool
		connector, ok = entry.Config.AudienceConnectors[a.Delivery.Connector+"/"+a.Delivery.Source]
		if !ok {
			return false, denied
		}
	}
	lookup := c.LookupSecret
	if lookup == nil {
		lookup = os.Getenv
	}
	key := lookup(connector.SecretEnv)
	if len(key) < 32 || a.Verify(key, time.Now()) != nil {
		return false, denied
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(r.material())
	signature, _ := hex.DecodeString(r.Signature)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return false, denied
	}
	check := sourceCheck{"mcp_source_read", r.Source, r.Revision}
	// The issuer reconfirms this stored assignment through its owning connector,
	// checks current entitlements, seals the decision and audits the request.
	raw, err := c.request(ctx, entry.Config, map[string]any{
		"operation": "check_assignment", "service": "kord", "request_id": r.Request,
		"invocation_id": r.Invocation, "credential_id": entry.Grant.CredentialID,
		"assignment_digest": r.Assignment, "check": check,
	})
	if err != nil {
		return false, err
	}
	var result struct {
		Request    uuid.UUID `json:"request_id"`
		Invocation uuid.UUID `json:"invocation_id"`
		Assignment string    `json:"assignment_digest"`
		Check      string    `json:"check_sha256"`
		Allowed    *bool     `json:"allowed"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || !errors.Is(d.Decode(new(any)), io.EOF) || result.Allowed == nil ||
		result.Request != r.Request || result.Invocation != r.Invocation || result.Assignment != r.Assignment ||
		result.Check != check.digest() {
		return false, denied
	}
	return *result.Allowed, nil
}
