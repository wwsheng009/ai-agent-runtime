package envdclient

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
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

// TestClientStreamingOverHTTP2 covers the success path of the client-streaming
// methods: with HTTP/2 negotiated, the request frame must carry the expected
// payload and a clean end-stream must decode as success.
func TestClientStreamingOverHTTP2(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frame, err := ReadFrame(r.Body)
		if err != nil {
			t.Errorf("ReadFrame(%s): %v", r.URL.Path, err)
			http.Error(w, "bad frame", http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(frame.Payload, &body); err != nil {
			t.Errorf("decode %s request: %v", r.URL.Path, err)
		}
		bodies = append(bodies, body)

		w.Header().Set("Content-Type", "application/connect+json")
		_, _ = w.Write(EncodeFrame(flagEndStream, []byte("{}")))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	client, err := Dial(Config{
		Addr: srv.URL,
		User: "n9e",
		TLS:  &tls.Config{InsecureSkipVerify: true},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ctx := context.Background()

	if err := client.SendSignal(ctx, 42, "SIGTERM"); err != nil {
		t.Fatalf("SendSignal: %v", err)
	}
	if err := client.SendInput(ctx, 42, []byte("hello")); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if err := client.UpdatePty(ctx, 42, 80, 24); err != nil {
		t.Fatalf("UpdatePty: %v", err)
	}

	if len(bodies) != 3 {
		t.Fatalf("recorded %d request bodies, want 3", len(bodies))
	}
	signal := bodies[0]["process"].(map[string]any)
	if signal["pid"] != float64(42) || signal["signal"] != "SIGTERM" {
		t.Errorf("SendSignal body = %v, want pid 42 / SIGTERM", signal)
	}
	input := bodies[1]["process"].(map[string]any)
	inputData := input["input"].(map[string]any)
	if decoded, err := base64.StdEncoding.DecodeString(inputData["data"].(string)); err != nil || string(decoded) != "hello" {
		t.Errorf("SendInput data = %v (decoded %q, err %v), want hello", inputData["data"], decoded, err)
	}
	pty := bodies[2]["process"].(map[string]any)
	ptySize := pty["pty"].(map[string]any)
	if ptySize["cols"] != float64(80) || ptySize["rows"] != float64(24) {
		t.Errorf("UpdatePty body = %v, want 80x24", ptySize)
	}
}
