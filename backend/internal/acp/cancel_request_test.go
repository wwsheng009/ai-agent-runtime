package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func pipeConns(t *testing.T) (agent *Conn, client *Conn, cleanup func()) {
	t.Helper()
	clientReader, agentWriter := io.Pipe()
	agentReader, clientWriter := io.Pipe()
	agentConn := NewConn(agentReader, agentWriter)
	clientConn := NewConn(clientReader, clientWriter)
	return agentConn, clientConn, func() {
		clientReader.Close()
		agentWriter.Close()
		agentReader.Close()
		clientWriter.Close()
	}
}

// TestCallSendsCancelRequestOnCtxCancel verifies that a client-side Call whose
// context is cancelled emits a $/cancel_request notification to the agent.
// The wire side is inspected directly: Conn dispatch short-circuits
// $/cancel_request before the inbound handler, so the agent handler never
// sees it (by design, mirroring LSP transport behaviour).
func TestCallSendsCancelRequestOnCtxCancel(t *testing.T) {
	t.Parallel()

	// io.Pipe returns (reader, writer); client writes into pipeW and we read
	// the raw NDJSON lines from pipeR. The client's stdin is a pipe that never
	// yields data so Serve stays alive.
	stdinR, stdinW := io.Pipe()
	pipeR, pipeW := io.Pipe()
	defer pipeR.Close()
	defer pipeW.Close()
	defer stdinW.Close()
	client := NewConn(stdinR, pipeW)
	// Serve is not required: Call only needs the write side to be drained.

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Call(ctx, MethodSessionPrompt, PromptRequest{
			SessionID: "sess_x",
			Prompt:    []ContentBlock{TextContent("hi")},
		}, nil)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		type wireMsg struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		lineCh := make(chan string, 1)
		go func() {
			buf := make([]byte, 4096)
			n, err := pipeR.Read(buf)
			if err != nil {
				lineCh <- ""
				return
			}
			lineCh <- string(buf[:n])
		}()
		var line string
		select {
		case line = <-lineCh:
		case <-deadline:
			t.Fatal("no $/cancel_request on the wire after ctx cancel")
		}
		if strings.TrimSpace(line) == "" {
			// Read raced with pipe close or returned no data; keep polling
			// until the deadline before declaring failure.
			select {
			case <-deadline:
				t.Fatal("wire read failed before observing $/cancel_request")
			default:
				continue
			}
		}
		var msg wireMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Method != MethodCancelRequest {
			continue
		}
		var params CancelRequestParams
		if len(msg.Params) > 0 {
			_ = json.Unmarshal(msg.Params, &params)
		}
		if len(params.RequestID) == 0 || string(params.RequestID) == "null" {
			t.Fatalf("cancel_request requestId missing: %s", params.RequestID)
		}
		break
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected ctx error from Call")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return after cancel")
	}
}

// TestIDKeyStringifiedNumericID ensures a stringified numeric id (some
// clients send `"requestId":"12"` for a prompt issued with numeric id 12)
// still matches the in-flight request registered under the numeric form.
func TestIDKeyStringifiedNumericID(t *testing.T) {
	t.Parallel()

	if got := idKey(json.RawMessage(`12`)); got != "12" {
		t.Fatalf("idKey(12) = %q, want 12", got)
	}
	if got := idKey(json.RawMessage(`"12"`)); got != "12" {
		t.Fatalf(`idKey("12") = %q, want 12`, got)
	}
	if got := idKey(json.RawMessage(`"sess_1"`)); got != "sess_1" {
		t.Fatalf(`idKey("sess_1") = %q, want sess_1`, got)
	}
	if got := idKey(json.RawMessage(`  "12"  `)); got != "12" {
		t.Fatalf(`idKey(padded "12") = %q, want 12`, got)
	}
}

// TestServerCancelRequestAbortsPrompt verifies that the agent honours an
// inbound $/cancel_request by aborting the in-flight session/prompt handler
// and replying stopReason=cancelled.
func TestServerCancelRequestAbortsPrompt(t *testing.T) {
	t.Parallel()

	agent, client, cleanup := pipeConns(t)
	defer cleanup()

	started := make(chan struct{})
	backend := &fakeBackend{
		promptFn: func(ctx context.Context, req PromptRequest, emit Emitter) (PromptResponse, error) {
			close(started)
			select {
			case <-ctx.Done():
				return PromptResponse{}, ctx.Err()
			case <-time.After(5 * time.Second):
				return PromptResponse{StopReason: StopReasonEndTurn}, nil
			}
		},
	}
	server := NewServer(agent, backend, ServerOptions{AgentInfo: Implementation{Name: "aicli"}})
	go func() { _ = server.Serve(context.Background()) }()
	go func() { _ = client.Serve(context.Background()) }()

	ctx := context.Background()
	_ = client.Call(ctx, MethodInitialize, InitializeRequest{ProtocolVersion: ProtocolVersion}, &InitializeResponse{})
	var newResp NewSessionResponse
	_ = client.Call(ctx, MethodSessionNew, NewSessionRequest{}, &newResp)

	promptDone := make(chan PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		var resp PromptResponse
		if err := client.Call(ctx, MethodSessionPrompt, PromptRequest{
			SessionID: newResp.SessionID,
			Prompt:    []ContentBlock{TextContent("long")},
		}, &resp); err != nil {
			promptErr <- err
			return
		}
		promptDone <- resp
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not start")
	}

	// Capture the in-flight prompt request id from the client conn's pending
	// ordering: ids are allocated sequentially, so find it via the cancel map
	// on the agent side instead — send cancel for the currently open request.
	go func() {
		// Small settle delay so the agent has registered the prompt id.
		time.Sleep(50 * time.Millisecond)
		agent.inboundCancelsMu.Lock()
		var id string
		for key := range agent.inboundCancels {
			id = key
		}
		agent.inboundCancelsMu.Unlock()
		_ = client.Notify(MethodCancelRequest, CancelRequestParams{
			RequestID: json.RawMessage(id),
		})
	}()

	select {
	case resp := <-promptDone:
		if resp.StopReason != StopReasonCancelled {
			t.Fatalf("stopReason = %q, want cancelled", resp.StopReason)
		}
	case err := <-promptErr:
		t.Fatalf("prompt error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not abort after $/cancel_request")
	}
}
