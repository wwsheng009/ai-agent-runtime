package envdclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestListDecodesProcesses covers the unary process endpoint.
func TestListDecodesProcesses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/process.Process/List" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"processes":[{"pid":7,"tag":"build","config":{"cmd":"/bin/sh","args":["-c","true"],"cwd":"/tmp"}}]}`)
	}))
	defer srv.Close()

	client, err := Dial(Config{Addr: srv.URL, User: "n9e"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	procs, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("processes = %+v, want one", procs)
	}
	proc := procs[0]
	if proc.Pid != 7 || proc.Tag != "build" || proc.Config == nil ||
		proc.Config.Cmd != "/bin/sh" || len(proc.Config.Args) != 2 || proc.Config.Cwd != "/tmp" {
		t.Fatalf("process = %+v, want the stub's process", proc)
	}
}

// TestSendBidiRequiresHTTP2 pins the actionable error for client-streaming
// methods on a plaintext HTTP/1.1 connection.
func TestSendBidiRequiresHTTP2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := Dial(Config{Addr: srv.URL, User: "n9e"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	err = client.SendSignal(context.Background(), 42, "SIGTERM")
	if err == nil || !strings.Contains(err.Error(), "requires HTTP/2") {
		t.Fatalf("SendSignal error = %v, want the HTTP/2 requirement", err)
	}
}

// TestProcessRequestValidation covers the local guards that avoid pointless
// network calls.
func TestProcessRequestValidation(t *testing.T) {
	ctx := context.Background()
	client := &Client{}

	if _, err := client.Start(ctx, StartRequest{}); err == nil {
		t.Fatal("Start accepted an empty command")
	}
	if err := client.SendSignal(ctx, 1, ""); err == nil {
		t.Fatal("SendSignal accepted an empty signal")
	}
	if err := client.SendInput(ctx, 1, nil); err != nil {
		t.Fatalf("SendInput with no bytes = %v, want nil", err)
	}
	if err := client.UpdatePty(ctx, 1, 0, 24); err == nil {
		t.Fatal("UpdatePty accepted zero columns")
	}
}
