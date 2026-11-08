package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestClosedUnixHandoff(t *testing.T) {
	id := strings.Repeat("e", 64)
	a := assignment{id: id, binding: binding{Fingerprint: strings.Repeat("0", 64), Run: uuid.New(), Session: uuid.New()}, action: "cancel"}
	socket := filepath.Join(t.TempDir(), "exec.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/executions/"+id+"/cancel" {
			t.Error("wrong execution route")
		}
		var b binding
		if json.NewDecoder(r.Body).Decode(&b) != nil || b != a.binding {
			t.Error("incorrect cancel binding")
		}
		w.WriteHeader(409)
		w.Write([]byte(`{"error":"job_binding_conflict"}`))
	})}
	go srv.Serve(listener)
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	result, err := unixBackend(socket).exchange(t.Context(), a)
	if err != nil || result.State != "rejected" {
		t.Fatal(result, err)
	}
}

func TestReceiptChecks(t *testing.T) {
	id := strings.Repeat("e", 64)
	usage := counters{Input: new(int64(4)), Cached: new(int64(1)), Output: new(int64(2))}
	r := receipt{ID: id, State: "done", Usage: &usage}
	if !r.valid(id) || r.valid(strings.Repeat("b", 64)) {
		t.Fatal("invalid result identity check")
	}
	usage.Cached = new(int64(5))
	if r.valid(id) {
		t.Fatal("overreported cache")
	}
	r.State = "running"
	if r.valid(id) {
		t.Fatal("unexpected unfinished receipt")
	}
}
