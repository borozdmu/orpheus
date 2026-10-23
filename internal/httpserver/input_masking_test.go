//go:build integration

package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/session"
	"github.com/orpheus-agents/orpheus/internal/store"
)

const maskingName = "Иванов Пётр Сергеевич"
const maskingBody = `{"allow_multiple_runs":true,"configuration":{"agent":{"profile":"default","instructions":"Помоги Иванов Пётр Сергеевич"},"sandbox":{"template":"codex"}},"messages":[{"text":"Иванов Пётр Сергеевич просит перенести встречу","external_key":"post:1","metadata":{"technical_id":"Иванов Пётр Сергеевич","nested":{"id":9007199254740993}}}]}`
const maskingMessage = `{"messages":[{"text":"Иванов Пётр Сергеевич просит перенести встречу","external_key":"post:2","metadata":{"technical_id":"Иванов Пётр Сергеевич"}}]}`

func inputMaskingProfiles(t *testing.T, endpoint string, enabled bool) config.Profiles {
	t.Helper()
	p, err := config.ReadProfiles(strings.NewReader(fmt.Sprintf(`[input_masker]
url = %q
timeout_seconds = 2
[templates.codex]
[profiles.default]
harness = "codex"
model = "fixture"
instructions = "Помоги Иванов Пётр Сергеевич"
mask_input = %t
[profiles.default.auth]
mode = "api_key"
api_key_env = "OPENAI_API_KEY"
`, endpoint, enabled)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func maskingRunState(t *testing.T, s *store.Store, a session.Acceptance, finish bool) {
	t.Helper()
	err := s.Mutate(t.Context(), a.SessionID, false, func(tx pgx.Tx, rec *store.SessionRecord) error {
		r, err := store.GetRun(t.Context(), tx, a.SessionID, a.RunID)
		if err != nil {
			return err
		}
		if finish {
			return store.Finish(t.Context(), tx, rec, &r, session.Completed, nil, nil)
		}
		r.Status = session.Running
		return store.PublishRun(t.Context(), tx, rec, &r)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func maskingCounts(t *testing.T, s *store.Store) [5]int {
	t.Helper()
	var out [5]int
	err := s.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM sessions), (SELECT count(*) FROM runs), (SELECT count(*) FROM messages), (SELECT count(*) FROM session_events), (SELECT count(*) FROM idempotency_keys)`).Scan(&out[0], &out[1], &out[2], &out[3], &out[4])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func checkMaskedAdmission(t *testing.T, s *store.Store, a session.Acceptance) {
	t.Helper()
	m, err := store.GetMessage(t.Context(), s.Pool, a.MessageID)
	if err != nil || strings.Contains(m.Text, maskingName) || !strings.Contains(m.Text, "PERSON") {
		t.Fatal("unmasked or missing persisted message", err)
	}
	// Opaque metadata is trusted-side data, never part of the model prompt.
	if !strings.Contains(string(m.Metadata), maskingName) || m.ExternalKey == nil {
		t.Fatal("opaque metadata or technical identifier changed")
	}
	r, err := store.GetSession(t.Context(), s.Pool, a.SessionID, false)
	if err != nil || strings.Contains(r.Configuration.Public.Agent.Instructions, maskingName) || !strings.Contains(r.Configuration.Public.Agent.Instructions, "PERSON") {
		t.Fatal("unmasked persisted instructions", err)
	}
	if r.SandboxID != nil {
		t.Fatal("admission created a sandbox")
	}
}

func TestInputMaskingAllAdmissionPaths(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			calls := 0
			fail := false
			masker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/mask/segments" {
					t.Error("wrong masker endpoint")
				}
				if fail {
					w.WriteHeader(503)
					return
				}
				var in struct {
					Texts []string `json:"texts"`
				}
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				for i := range in.Texts {
					in.Texts[i] = strings.ReplaceAll(in.Texts[i], maskingName, "[PERSON_test]")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"masked_texts": in.Texts})
			}))
			defer masker.Close()
			server, s := testServer(t)
			s.Profiles = inputMaskingProfiles(t, masker.URL, enabled)
			post := func(path, body string) session.Acceptance {
				t.Helper()
				key := uuid.NewString()
				a := decodeHTTP[session.Acceptance](t, externalRequest(t, server, "POST", path, body, key, 202))
				if enabled {
					checkMaskedAdmission(t, s, a)
				} else {
					m, err := store.GetMessage(t.Context(), s.Pool, a.MessageID)
					if err != nil || m.Text != maskingName+" просит перенести встречу" || calls != 0 {
						t.Fatal("disabled profile changed input or called masker", err)
					}
				}
				before := calls
				fail = true
				again := decodeHTTP[session.Acceptance](t, externalRequest(t, server, "POST", path, body, key, 202))
				if again != a || calls != before {
					t.Fatal("replay called masker or changed acceptance")
				}
				externalRequest(t, server, "POST", path, strings.Replace(body, "просит", "требует", 1), key, 409)
				if enabled {
					counts := maskingCounts(t, s)
					raw := externalRequest(t, server, "POST", path, body, uuid.NewString(), 503)
					if !strings.Contains(string(raw), "input_masker_unavailable") || maskingCounts(t, s) != counts {
						t.Fatal("masker failure persisted data or leaked error")
					}
				}
				fail = false
				return a
			}
			a := post("/api/v1/sessions", maskingBody)
			// The operator flipping the profile must not change an existing session.
			s.Profiles = inputMaskingProfiles(t, masker.URL, !enabled)
			maskingRunState(t, s, a, true)
			base := "/api/v1/sessions/" + a.SessionID.String() + "/runs"
			b := post(base, maskingMessage)
			maskingRunState(t, s, b, false)
			post(base+"/"+b.RunID.String()+"/messages", maskingMessage)
		})
	}
}

// Opt-in synthetic canary: real local masker, real HTTP admission and database,
// no worker, sandbox driver or model credentials involved.
func TestInputMaskingSynthetic(t *testing.T) {
	endpoint := os.Getenv("ORPHEUS_TEST_INPUT_MASKER_URL")
	if endpoint == "" {
		t.Skip("local masker canary is opt-in")
	}
	server, s := testServer(t)
	s.Profiles = inputMaskingProfiles(t, endpoint, true)
	a := decodeHTTP[session.Acceptance](t, externalRequest(t, server, "POST", "/api/v1/sessions", maskingBody, uuid.NewString(), 202))
	checkMaskedAdmission(t, s, a)
	s.Profiles = inputMaskingProfiles(t, "http://127.0.0.1:1", true)
	before := maskingCounts(t, s)
	externalRequest(t, server, "POST", "/api/v1/sessions", maskingBody, uuid.NewString(), 503)
	if maskingCounts(t, s) != before {
		t.Fatal("unavailable masker persisted data")
	}
	t.Log("synthetic: PERSON persisted, original absent from text/instructions; metadata trusted-side only; unavailable=503 unchanged DB; sandbox=0 model_sends=0 (no worker)")
}

func TestInputMaskingProfileInstructionsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		delay      bool
	}{
		{"success", `{"masked_texts":["[PERSON_test]","[PERSON_test]"]}`, 200, false},
		{"partial", `{"masked_texts":["[PERSON_test]"]}`, 200, false},
		{"null", `{"masked_texts":["[PERSON_test]",null]}`, 200, false},
		{"malformed", `{"masked_texts":`, 200, false},
		{"busy", `private-response`, 429, false},
		{"timeout", `{"masked_texts":["[PERSON_test]","[PERSON_test]"]}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FIXTURE_MASKER_TOKEN", "synthetic-only-token")
			masker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-only-token" {
					t.Error("configured token did not reach masker")
				}
				if tc.delay {
					time.Sleep(1100 * time.Millisecond)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer masker.Close()
			server, s := testServer(t)
			s.Profiles = inputMaskingProfiles(t, masker.URL, true)
			s.Profiles.InputMasker.TokenEnv = "FIXTURE_MASKER_TOKEN"
			s.Profiles.InputMasker.TimeoutSeconds = 1
			body := strings.Replace(maskingBody, `,"instructions":"Помоги Иванов Пётр Сергеевич"`, "", 1)
			before := maskingCounts(t, s)
			status, _, raw := requestHTTP(t, server, "POST", "/api/v1/sessions", body, "key", uuid.NewString())
			if tc.name == "success" {
				if status != 202 {
					t.Fatal("admission failed", status)
				}
				a := decodeHTTP[session.Acceptance](t, raw)
				checkMaskedAdmission(t, s, a)
				rec, err := store.GetSession(t.Context(), s.Pool, a.SessionID, false)
				stored, _ := json.Marshal(rec.Configuration)
				if err != nil || strings.Contains(string(stored), "synthetic-only-token") || strings.Contains(string(stored), "FIXTURE_MASKER_TOKEN") {
					t.Fatal("token source leaked to snapshot", err)
				}
			} else if status != 503 || maskingCounts(t, s) != before || strings.Contains(string(raw), "private-response") || strings.Contains(string(raw), "synthetic-only-token") {
				t.Fatal("invalid masker response accepted, persisted data or leaked details", status)
			}
		})
	}
}

func TestInputMaskingClientCannotOverridePolicyOrSendAttachments(t *testing.T) {
	server, s := testServer(t)
	s.Profiles = inputMaskingProfiles(t, "http://127.0.0.1:1", true)
	for _, body := range []string{
		strings.Replace(maskingBody, `"profile":"default"`, `"profile":"default","mask_input":false`, 1),
		strings.Replace(maskingBody, `"allow_multiple_runs":true`, `"allow_multiple_runs":true,"input_masked":false`, 1),
		strings.Replace(maskingBody, `"external_key":"post:1"`, `"external_key":"post:1","attachments":[{"url":"http://fixture.invalid/file"}]`, 1),
	} {
		before := maskingCounts(t, s)
		externalRequest(t, server, "POST", "/api/v1/sessions", body, uuid.NewString(), 422)
		if maskingCounts(t, s) != before {
			t.Fatal("invalid public request persisted data")
		}
	}
}
