package servicecredentials

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus/internal/session"
)

// Real HTTP framing over an in-memory connection exercises both the default
// client's timeout and the request context without network or wall-clock waits.
func TestProviderProjectionDeadline(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", strings.Repeat("k", 32))
	for _, custom := range []bool{false, true} {
		for _, test := range []struct {
			name                   string
			delay, caller, elapsed time.Duration
			wantError              bool
		}{
			{"fast", time.Second, 2 * time.Minute, time.Second, false},
			{"fresh_projection", 31 * time.Second, 2 * time.Minute, 31 * time.Second, false},
			{"bounded_wait", 61 * time.Second, 2 * time.Minute, time.Minute, true},
			{"caller_deadline", 31 * time.Second, 2 * time.Second, 2 * time.Second, true},
		} {
			t.Run(fmt.Sprintf("%s/custom=%t", test.name, custom), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var responses sync.WaitGroup
					var requests atomic.Int64
					defer responses.Wait()
					transport := &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
						client, server := net.Pipe()
						responses.Go(func() {
							defer func() { _ = server.Close() }()
							request, err := http.ReadRequest(bufio.NewReader(server))
							if err != nil {
								t.Error(err)
								return
							}
							defer func() { _ = request.Body.Close() }()
							requests.Add(1)
							var body any
							if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
								t.Error(err)
								return
							}
							time.Sleep(test.delay)
							raw, err := json.Marshal(testGrant())
							if err != nil {
								t.Error(err)
								return
							}
							_, _ = fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(raw), raw)
						})
						return client, nil
					}}
					previous := http.DefaultTransport
					http.DefaultTransport = transport
					defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
					client := Client{}
					if custom {
						client.HTTP = &http.Client{Transport: transport}
					}
					ctx, cancel := context.WithTimeout(t.Context(), test.caller)
					defer cancel()
					before := time.Now()
					_, err := client.Issue(ctx, testConfig("https://provider.invalid/credentials"), "kord", session.AccessContext{InvocationID: uuid.New()})
					if (err != nil) != test.wantError || time.Since(before) != test.elapsed {
						t.Fatalf("error=%v elapsed=%v, want error=%v elapsed=%v", err, time.Since(before), test.wantError, test.elapsed)
					}
					if requests.Load() != 1 {
						t.Fatalf("provider request was retried: %d requests", requests.Load())
					}
				})
			})
		}
	}
}
