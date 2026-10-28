package remotetools_test

// M0 vertical slice for the envd execution backend: the same server, the same
// profile gating, the same audit wrapper, but `view` reading a file that lives
// on a remote envd daemon. The stub below answers exactly the RPCs that path
// uses — filesystem.Stat and process.Start — so the wiring is proven without a
// live target, and the two jail phases (lexical, then target-side canonical)
// are each pinned by a test that also asserts envd was never contacted.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// envdStub stands in for the envd daemon.
type envdStub struct {
	server *httptest.Server
	// files maps a canonical target-side path to its content.
	files map[string]string
	// links maps a path to what a target-side realpath would report for it;
	// this is how a symlink escape is simulated.
	links map[string]string
	// Counters let a test prove the jail refused before envd was contacted.
	statCalls    atomic.Int64
	processCalls atomic.Int64
}

func newEnvdStub(t *testing.T, files, links map[string]string) *envdStub {
	t.Helper()

	stub := &envdStub{files: files, links: links}
	mux := http.NewServeMux()

	mux.HandleFunc("/filesystem.Filesystem/Stat", func(w http.ResponseWriter, r *http.Request) {
		stub.statCalls.Add(1)
		var in struct {
			Path string `json:"path"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &in)
		content, ok := stub.files[in.Path]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"code":"not_found","message":"no such file"}`)
			return
		}
		response := map[string]any{
			"entry": map[string]any{
				"name":        base(in.Path),
				"path":        in.Path,
				"type":        "FILE_TYPE_FILE",
				"size":        fmt.Sprintf("%d", len(content)),
				"mode":        420,
				"permissions": "-rw-r--r--",
				"owner":       "dev",
				"group":       "dev",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})

	mux.HandleFunc("/process.Process/Start", func(w http.ResponseWriter, r *http.Request) {
		stub.processCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		payload := body
		if len(body) > 5 {
			// Connect envelope: one flag byte, four big-endian length bytes.
			payload = body[5:]
		}
		var req envdclient.StartRequest
		_ = json.Unmarshal(payload, &req)

		w.Header().Set("Content-Type", "application/connect+json")
		last := func() string {
			if len(req.Process.Args) == 0 {
				return ""
			}
			return req.Process.Args[len(req.Process.Args)-1]
		}
		switch req.Process.Cmd {
		case "/bin/sh":
			// The path canonicalizer: report the link target when one is mapped.
			target := last()
			resolved := target
			if link, ok := stub.links[target]; ok {
				resolved = link
			}
			writeProcessStream(t, w, []byte(resolved+"\n"), nil, 0)
		case "/bin/cat":
			content, ok := stub.files[last()]
			if !ok {
				writeProcessStream(t, w, nil, []byte("cat: no such file\n"), 1)
				return
			}
			writeProcessStream(t, w, []byte(content), nil, 0)
		default:
			writeProcessStream(t, w, nil, []byte("stub: unexpected command "+req.Process.Cmd+"\n"), 127)
		}
	})

	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)
	return stub
}

// base returns the last slash-separated element of a target-side path.
func base(target string) string {
	trimmed := strings.TrimRight(target, "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return trimmed[idx+1:]
	}
	return trimmed
}

func writeProcessStream(t *testing.T, w io.Writer, stdout, stderr []byte, code int32) {
	t.Helper()
	writeEnvDFrame(t, w, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 7}}})
	data := map[string]any{}
	if len(stdout) > 0 {
		data["stdout"] = base64.StdEncoding.EncodeToString(stdout)
	}
	if len(stderr) > 0 {
		data["stderr"] = base64.StdEncoding.EncodeToString(stderr)
	}
	if len(data) > 0 {
		writeEnvDFrame(t, w, map[string]any{"event": map[string]any{"data": data}})
	}
	end := map[string]any{"exited": true, "status": fmt.Sprintf("exit status %d", code)}
	if code != 0 {
		end["exit_code"] = code
	}
	writeEnvDFrame(t, w, map[string]any{"event": map[string]any{"end": end}})
}

func writeEnvDFrame(t *testing.T, w io.Writer, payload any) {
	t.Helper()
	frame, err := envdclient.EncodeJSONFrame(payload)
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	if _, err := w.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// newEnvDConfig builds the smallest valid envd-backend config.
func newEnvDConfig(addr string) *remotetools.Config {
	cfg := remotetools.DefaultConfig()
	cfg.Backend = remotetools.BackendEnvD
	cfg.Workspace = "/srv/app"
	cfg.ToolPrefix = "remote_envd"
	cfg.TargetName = "envd"
	cfg.EnvD = remotetools.EnvDConfig{Addr: addr, User: "dev"}
	return cfg
}

func TestEnvDNormalizeValidatesTargetSideInvariants(t *testing.T) {
	t.Run("absolute workspace is preserved verbatim", func(t *testing.T) {
		cfg := newEnvDConfig("http://127.0.0.1:49983")
		cfg.Workspace = "/srv/app/"
		if err := cfg.Normalize(); err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		// A Windows host must not rewrite the target path into backslashes.
		if cfg.Workspace != "/srv/app" {
			t.Fatalf("workspace = %q, want %q", cfg.Workspace, "/srv/app")
		}
		if len(cfg.AllowedRoots) != 1 || cfg.AllowedRoots[0] != "/srv/app" {
			t.Fatalf("allowed roots = %v, want the workspace only", cfg.AllowedRoots)
		}
	})

	t.Run("relative workspace is refused", func(t *testing.T) {
		cfg := newEnvDConfig("http://127.0.0.1:49983")
		cfg.Workspace = "srv/app"
		err := cfg.Normalize()
		if err == nil {
			t.Fatal("relative envd workspace was accepted")
		}
		if !strings.Contains(err.Error(), "absolute POSIX path") {
			t.Fatalf("error = %q, want the absolute POSIX path message", err)
		}
	})

	t.Run("missing endpoint identity is refused", func(t *testing.T) {
		cfg := newEnvDConfig("http://127.0.0.1:49983")
		cfg.EnvD.Addr = ""
		if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "envd.addr") {
			t.Fatalf("missing addr error = %v", err)
		}
		cfg = newEnvDConfig("http://127.0.0.1:49983")
		cfg.EnvD.User = ""
		if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "envd.user") {
			t.Fatalf("missing user error = %v", err)
		}
	})

	t.Run("http transport is refused for envd", func(t *testing.T) {
		cfg := newEnvDConfig("http://127.0.0.1:49983")
		cfg.Transport = "http"
		if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "stdio") {
			t.Fatalf("http transport error = %v", err)
		}
	})

	t.Run("unknown backend is refused", func(t *testing.T) {
		cfg := newEnvDConfig("http://127.0.0.1:49983")
		cfg.Backend = "sftp"
		if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "unknown backend") {
			t.Fatalf("unknown backend error = %v", err)
		}
	})
}

// TestEnvDServerExposesPrefixedView proves the slice reaches the MCP surface:
// the envd backend registers into the shared registry and the shared prefix
// rewriting produces the same name shape as the ssh/http transports.
func TestEnvDServerExposesPrefixedView(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/README.md": "# hello envd\n"}, nil)
	server, err := remotetools.NewServer(newEnvDConfig(stub.server.URL), "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	names := server.ToolNames()
	if len(names) != 1 || names[0] != "remote_envd__view" {
		t.Fatalf("tool names = %v, want exactly [remote_envd__view]", names)
	}
}

func connectEnvDServer(t *testing.T, server *remotetools.Server) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.ServeStdioOn(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ctx
}

// TestEnvDViewReadsThroughTheStub is the end-to-end slice: a model-visible
// remote read served by the shared server over the envd backend, audited by the
// shared wrapper.
func TestEnvDViewReadsThroughTheStub(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/README.md": "# hello envd\n"}, nil)
	auditPath := filepath.Join(t.TempDir(), "audit.ndjson")

	cfg := newEnvDConfig(stub.server.URL)
	cfg.Audit.Path = auditPath
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	session, ctx := connectEnvDServer(t, server)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "remote_envd__view" {
		t.Fatalf("tools = %d, want exactly one remote_envd__view", len(tools.Tools))
	}

	// The local spelling (file_path) and the bridge spelling (path) both work.
	for _, key := range []string{"file_path", "path"} {
		got := callTool(t, ctx, session, "remote_envd__view", map[string]interface{}{key: "README.md"})
		if !strings.Contains(got, "# hello envd") {
			t.Fatalf("%s = %q, want the file content", key, got)
		}
	}

	// A missing file comes back as a described refusal, not an opaque code.
	missing, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_envd__view",
		Arguments: map[string]interface{}{"file_path": "gone.md"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !missing.IsError {
		t.Fatalf("missing file was not reported as an error: %s", textContent(missing))
	}
	if !strings.Contains(textContent(missing), "does not exist") {
		t.Fatalf("missing file text = %q, want the described not-found message", textContent(missing))
	}

	// The shared audit wrapper recorded the calls, and recorded no content.
	if stub.statCalls.Load() == 0 {
		t.Fatal("expected the backend to have called filesystem.Stat")
	}
	server.Close()
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(string(raw), "remote_envd__view") {
		t.Fatalf("audit log has no envd view record: %s", raw)
	}
	if strings.Contains(string(raw), "hello envd") {
		t.Fatalf("audit log leaked file content: %s", raw)
	}
}

// TestEnvDViewRejectsEscapeBeforeEnvdIsContacted pins the first jail phase: a
// traversal path is refused locally, and envd is never asked about it.
func TestEnvDViewRejectsEscapeBeforeEnvdIsContacted(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/etc/passwd": "root:x:0:0\n"}, nil)
	server, err := remotetools.NewServer(newEnvDConfig(stub.server.URL), "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	session, ctx := connectEnvDServer(t, server)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_envd__view",
		Arguments: map[string]interface{}{"file_path": "../../etc/passwd"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("escape was not refused: %s", textContent(result))
	}
	if !strings.Contains(textContent(result), "outside the workspace") {
		t.Fatalf("refusal text = %q, want the workspace boundary message", textContent(result))
	}
	if got := stub.statCalls.Load(); got != 0 {
		t.Fatalf("stat calls = %d, want 0: the jail must refuse before contacting envd", got)
	}
}

// TestEnvDViewFollowsTargetSymlinkIntoRejection pins the second jail phase: a
// path that looks inside the workspace but canonicalizes outside it on the
// target is refused, and the escape target is never opened.
func TestEnvDViewFollowsTargetSymlinkIntoRejection(t *testing.T) {
	stub := newEnvdStub(t,
		map[string]string{"/etc/passwd": "root:x:0:0\n"},
		map[string]string{"/srv/app/link": "/etc/passwd"},
	)
	server, err := remotetools.NewServer(newEnvDConfig(stub.server.URL), "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	session, ctx := connectEnvDServer(t, server)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_envd__view",
		Arguments: map[string]interface{}{"file_path": "link"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("symlink escape was not refused: %s", textContent(result))
	}
	text := textContent(result)
	if !strings.Contains(text, "outside the workspace") || !strings.Contains(text, "/etc/passwd") {
		t.Fatalf("refusal text = %q, want the canonical path named in the boundary message", text)
	}
	if got := stub.statCalls.Load(); got != 0 {
		t.Fatalf("stat calls = %d, want 0: the canonical path must be rejected before it is opened", got)
	}
}
