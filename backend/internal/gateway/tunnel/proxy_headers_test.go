package tunnel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestServeProxyStripsCallerAdminTokenBeforeTunnel(t *testing.T) {
	var (
		peerCh   = make(chan *websocket.Conn, 1)
		sessCh   = make(chan *session, 1)
		observed = make(chan wire.StreamOpen, 1)
		errCh    = make(chan error, 1)
	)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer peer.Close()
		peerCh <- peer
		sess := <-sessCh

		var streamID uint32
		for i := 0; i < 2; i++ {
			_, payload, err := peer.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			frame, err := wire.Decode(payload)
			if err != nil {
				errCh <- err
				return
			}
			if i == 0 {
				if frame.Type != wire.TypeStreamOpen {
					errCh <- &unexpectedFrameError{got: frame.Type, want: wire.TypeStreamOpen}
					return
				}
				open, err := wire.DecodePayload[wire.StreamOpen](frame)
				if err != nil {
					errCh <- err
					return
				}
				streamID = frame.StreamID
				observed <- open
			} else if frame.Type != wire.TypeStreamEnd {
				errCh <- &unexpectedFrameError{got: frame.Type, want: wire.TypeStreamEnd}
				return
			}
		}

		// The fake agent completes the stream directly. This keeps the test
		// focused on the gateway's STREAM_OPEN header filtering, without
		// depending on the agent's own defense-in-depth filter.
		sess.dispatch(streamID, streamEvent{kind: streamHeaders, status: http.StatusNoContent})
		sess.dispatch(streamID, streamEvent{kind: streamEnd})
	}))
	t.Cleanup(peerServer.Close)

	agentConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(peerServer.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial fake agent: %v", err)
	}
	t.Cleanup(func() { _ = agentConn.Close() })
	peer := <-peerCh
	t.Cleanup(func() { _ = peer.Close() })

	server := NewServer(Config{})
	sess := newSession("node-header-filter", agentConn)
	server.registerSession("node-header-filter", sess)
	server.Registry().Upsert(&Node{NodeID: "node-header-filter", Routes: []string{"/"}})
	sessCh <- sess

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/analytics/overview", nil)
	req.Header.Set("X-Skills-Admin-Token", "caller-supplied-admin-token")
	req.Header.Set("X-AICLI-Token", "caller-supplied-write-token")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeProxy(rec, req, "node-header-filter", req.URL.Path)
		close(done)
	}()

	var open wire.StreamOpen
	select {
	case open = <-observed:
	case err := <-errCh:
		t.Fatalf("fake agent: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for STREAM_OPEN")
	}
	for name := range open.Headers {
		if strings.EqualFold(name, "X-Skills-Admin-Token") {
			t.Fatalf("caller admin token reached tunnel: headers=%v", open.Headers)
		}
		if strings.EqualFold(name, "X-AICLI-Token") {
			t.Fatalf("caller write token reached tunnel: headers=%v", open.Headers)
		}
	}

	select {
	case <-done:
	case err := <-errCh:
		t.Fatalf("fake agent: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not finish")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("proxy status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

type unexpectedFrameError struct {
	got  wire.FrameType
	want wire.FrameType
}

func (e *unexpectedFrameError) Error() string {
	return "unexpected frame: got " + e.got.String() + ", want " + e.want.String()
}
