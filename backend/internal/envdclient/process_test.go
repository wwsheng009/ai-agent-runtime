package envdclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	if err := client.SendSignal(ctx, 1, "SIGUSR1"); err == nil {
		t.Fatal("SendSignal accepted an unsupported signal")
	}
	if err := client.SendInput(ctx, 1, nil); err != nil {
		t.Fatalf("SendInput with no bytes = %v, want nil", err)
	}
	if err := client.UpdatePty(ctx, 1, 0, 24); err == nil {
		t.Fatal("UpdatePty accepted zero columns")
	}
}

// TestProcessControlUnary pins the wire shape of the control RPCs.
//
// These three are unary (not client-streaming): they work over plain HTTP/1.1,
// and the field layout follows the protobuf messages, not the guessed nested
// form the first implementation used.
func TestProcessControlUnary(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Errorf("decode %s request: %v", r.URL.Path, err)
		}
		bodies = append(bodies, body)
		if contentType := r.Header.Get("Content-Type"); contentType != "application/json" {
			t.Errorf("%s content-type = %q, want application/json", r.URL.Path, contentType)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	client, err := Dial(Config{Addr: srv.URL, User: "n9e"})
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
	if err := client.SendPtyInput(ctx, 42, []byte("hi")); err != nil {
		t.Fatalf("SendPtyInput: %v", err)
	}

	if len(bodies) != 4 {
		t.Fatalf("recorded %d request bodies, want 4", len(bodies))
	}

	signalBody := bodies[0]
	if signalBody["signal"] != "SIGNAL_SIGTERM" {
		t.Errorf("SendSignal signal = %v, want SIGNAL_SIGTERM", signalBody["signal"])
	}
	if pid := signalBody["process"].(map[string]any)["pid"]; pid != float64(42) {
		t.Errorf("SendSignal pid = %v, want 42", pid)
	}

	inputBody := bodies[1]
	if pid := inputBody["process"].(map[string]any)["pid"]; pid != float64(42) {
		t.Errorf("SendInput pid = %v, want 42", pid)
	}
	encoded := inputBody["input"].(map[string]any)["stdin"].(string)
	if decoded, err := base64.StdEncoding.DecodeString(encoded); err != nil || string(decoded) != "hello" {
		t.Errorf("SendInput stdin = %q (decoded %q, err %v), want hello", encoded, decoded, err)
	}

	updateBody := bodies[2]
	size := updateBody["pty"].(map[string]any)["size"].(map[string]any)
	if size["cols"] != float64(80) || size["rows"] != float64(24) {
		t.Errorf("UpdatePty size = %v, want 80x24", size)
	}

	ptyInput := bodies[3]["input"].(map[string]any)["pty"].(string)
	if decoded, err := base64.StdEncoding.DecodeString(ptyInput); err != nil || string(decoded) != "hi" {
		t.Errorf("SendPtyInput pty = %q (decoded %q, err %v), want hi", ptyInput, decoded, err)
	}
}

// TestProcessEventPtyData covers the pty arm of the data event: it is base64
// bytes, not a file descriptor.
func TestProcessEventPtyData(t *testing.T) {
	var event ProcessEvent
	if err := json.Unmarshal([]byte(`{"event":{"data":{"pty":"cHR5LW91dHB1dAo="}}}`), &event); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if event.Kind != EventData {
		t.Fatalf("kind = %v, want data", event.Kind)
	}
	if string(event.Pty) != "pty-output\n" {
		t.Fatalf("pty = %q, want pty-output\\n", event.Pty)
	}
	if len(event.Stdout) != 0 || len(event.Stderr) != 0 {
		t.Fatalf("stdout/stderr = %q/%q, want empty", event.Stdout, event.Stderr)
	}
}

// TestStartRequestPtyShape pins the start payload for a pty process.
func TestStartRequestPtyShape(t *testing.T) {
	payload, err := json.Marshal(StartRequest{
		Process: ProcessConfig{Cmd: "/bin/sh"},
		Pty:     &PTY{Size: &PTYSize{Cols: 80, Rows: 24}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	size := body["pty"].(map[string]any)["size"].(map[string]any)
	if size["cols"] != float64(80) || size["rows"] != float64(24) {
		t.Fatalf("pty size = %v, want 80x24", size)
	}
	if _, ok := body["stdin"]; ok {
		t.Fatalf("start payload still carries stdin: %v", body)
	}
}
