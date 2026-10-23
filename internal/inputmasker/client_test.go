package inputmasker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMaskAtomicResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"complete", `{"masked_texts":["[PERSON_1]",""],"version":"v1"}`, 200, true},
		{"missing", `{}`, 200, false},
		{"partial", `{"masked_texts":["[PERSON_1]"]}`, 200, false},
		{"extra", `{"masked_texts":["[PERSON_1]","","extra"]}`, 200, false},
		{"null", `{"masked_texts":[null,""]}`, 200, false},
		{"wrong-type", `{"masked_texts":[42,""]}`, 200, false},
		{"empty", `{"masked_texts":["",""]}`, 200, false},
		{"truncated", `{"masked_texts":["[PERSON_1]",""`, 200, false},
		{"trailing", `{"masked_texts":["[PERSON_1]",""]} {}`, 200, false},
		{"unavailable", `original must never escape`, 503, false},
		{"busy", `original must never escape`, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request struct {
					Texts  []string `json:"texts"`
					Locale string   `json:"locale"`
				}
				if r.Method != "POST" || r.URL.Path != "/v1/mask/segments" || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Locale != "ru" || !reflect.DeepEqual(request.Texts, []string{"original", ""}) {
					t.Error("masker request does not match strict API")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			got, err := Mask(t.Context(), server.URL, time.Second, []string{"original", ""}, "")
			if tc.ok {
				if err != nil || !reflect.DeepEqual(got, []string{"[PERSON_1]", ""}) {
					t.Fatal(got, err)
				}
			} else if !errors.Is(err, ErrUnavailable) || got != nil || strings.Contains(err.Error(), "original") {
				t.Fatal("failure returned partial text or exposed response", err)
			}
			if calls != 1 {
				t.Fatal("unexpected retry", calls)
			}
		})
	}
}

func TestMaskDoesNotFollowRedirect(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			destinationCalls := 0
			destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls++ }))
			defer destination.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, status) }))
			defer server.Close()
			got, err := Mask(t.Context(), server.URL, time.Second, []string{"original"}, "")
			if !errors.Is(err, ErrUnavailable) || got != nil || destinationCalls != 0 {
				t.Fatal("redirect leaked input", err)
			}
		})
	}
}

func TestMaskTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{"masked_texts":["late"]}`)
	}))
	defer server.Close()
	for _, parentCancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if parentCancelled {
			cancel()
		}
		got, err := Mask(ctx, server.URL, time.Millisecond, []string{"original"}, "")
		cancel()
		if !errors.Is(err, ErrUnavailable) || got != nil {
			t.Fatal("timeout or cancellation returned input", err)
		}
	}
	for _, endpoint := range []string{"", ":bad-url"} {
		got, err := Mask(t.Context(), endpoint, time.Second, []string{"original"}, "")
		if !errors.Is(err, ErrUnavailable) || got != nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
	if _, err := Mask(t.Context(), server.URL, 0, []string{"original"}, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unbounded timeout accepted")
	}
}

func TestMaskAuthorization(t *testing.T) {
	for _, token := range []string{"", "synthetic-only-token"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			want := ""
			if token != "" {
				want = "Bearer " + token
			}
			if r.Header.Get("Authorization") != want {
				t.Error("wrong Bearer authorization")
			}
			_, _ = io.WriteString(w, `{"masked_texts":["masked"]}`)
		}))
		_, err := Mask(t.Context(), server.URL, time.Second, []string{"original"}, token)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
