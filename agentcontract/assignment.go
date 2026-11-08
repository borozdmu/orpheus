// Package agentcontract defines the transport-independent corporate assignment.
// It contains identity evidence and delivery routing, never service permissions.
package agentcontract

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const Version = "corporate-assignment.v1"

type Principal struct {
	BotID       string `json:"bot_id,omitzero"`
	Namespace   string `json:"namespace"`
	ID          string `json:"id"`
	PrincipalID string `json:"principal_id"`
}
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Route struct {
	Connector string `json:"connector"`
	Source    string `json:"source"`
	Chat      string `json:"chat"`
	Thread    string `json:"thread"`
	Message   string `json:"message"`
}
type Confirmation struct {
	Epoch      int64     `json:"epoch"`
	Revision   string    `json:"revision"`
	ObservedAt time.Time `json:"observed_at"`
	Digest     string    `json:"digest"`
	Receipt    string    `json:"receipt"`
}
type Assignment struct {
	Audience        Audience                   `json:"audience"`
	ProviderContext map[string]json.RawMessage `json:"provider_context"`
	Schema          string                     `json:"schema"`
	InvocationID    uuid.UUID                  `json:"invocation_id"`
	Agent           string                     `json:"agent"`
	Author          Principal                  `json:"author"`
	Readers         []Principal                `json:"readers"`
	Text            string                     `json:"text"`
	Attachments     []Attachment               `json:"attachments"`
	Delivery        Route                      `json:"delivery"`
	Confirmation    Confirmation               `json:"confirmation"`
}

func plain(s string) bool {
	return s != "" && len(s) <= 256 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func (a Assignment) Validate() error {
	fail := errors.New("invalid corporate assignment")
	if a.Audience.Validate() != nil || a.Audience.ChannelID != a.Delivery.Chat {
		return fail
	}
	if a.Schema != Version || a.InvocationID == uuid.Nil || !plain(a.Agent) || !plain(a.Author.PrincipalID) || !plain(a.Author.ID) || !plain(a.Author.Namespace) || a.Confirmation.ObservedAt.IsZero() || len(a.Readers) == 0 || len(a.Readers) > 4096 || len(a.Text) > 1<<20 || len(a.Attachments) > 128 {
		return fail
	}
	if (a.Delivery.Connector != "telegram" && a.Delivery.Connector != "mattermost") || !plain(a.Delivery.Source) || !plain(a.Delivery.Chat) || !plain(a.Delivery.Message) {
		return fail
	}
	seen := map[string]bool{}
	author := false
	for _, r := range a.Readers {
		key := r.Namespace + ":" + r.ID
		if !plain(r.Namespace) || !plain(r.ID) || !plain(r.PrincipalID) || seen[key] {
			return fail
		}
		seen[key] = true
		author = author || r == a.Author
	}
	if !author {
		return fail
	}
	for _, f := range a.Attachments {
		if !plain(f.ID) || len(f.Name) > 1024 {
			return fail
		}
	}
	return nil
}

// Digest covers the entire immutable assignment, including route, author, text,
// attachments and observation time. Receipt is a server-side witness, not a token.
func (a Assignment) Digest() string {
	a.Confirmation.Digest = ""
	a.Confirmation.Receipt = ""
	a.Readers = slices.Clone(a.Readers)
	slices.SortFunc(a.Readers, func(x, y Principal) int { return strings.Compare(x.Namespace+":"+x.ID, y.Namespace+":"+y.ID) })
	b, _ := json.Marshal(a)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (a *Assignment) Seal(secret string) error {
	if a.Validate() != nil || len(secret) < 32 {
		return errors.New("audience witness unavailable")
	}
	a.Confirmation.Digest = a.Digest()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(Version + ":" + a.Confirmation.Digest))
	a.Confirmation.Receipt = hex.EncodeToString(mac.Sum(nil))
	return nil
}
func (a Assignment) Verify(secret string, now time.Time) error {
	copy := a
	if a.Validate() != nil || now.Before(a.Confirmation.ObservedAt) || now.Sub(a.Confirmation.ObservedAt) > 20*time.Minute || copy.Seal(secret) != nil || !hmac.Equal([]byte(a.Confirmation.Digest), []byte(copy.Confirmation.Digest)) || !hmac.Equal([]byte(a.Confirmation.Receipt), []byte(copy.Confirmation.Receipt)) {
		return errors.New("audience witness rejected")
	}
	return nil
}
func Decode(raw []byte) (Assignment, error) {
	var a Assignment
	err := json.Unmarshal(raw, &a)
	if err == nil {
		err = a.Validate()
	}
	return a, err
}
