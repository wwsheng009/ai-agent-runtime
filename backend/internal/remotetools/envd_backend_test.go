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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// envdStub stands in for the envd daemon: the filesystem unary family, the
// process stream, and /metrics. Handlers run on their own goroutines, so all
// state is guarded by mu.
type envdStub struct {
	server *httptest.Server
	mu     sync.Mutex
	// files maps a canonical target-side path to its content.
	files map[string]string
	// links maps a path to what a target-side realpath would report for it;
	// this is how a symlink escape is simulated.
	links map[string]string
	// dirs pins explicit ListDir responses for a path; absent paths fall back to
	// synthesizing one level from files.
	dirs map[string][]map[string]any
	// metricsBody is the raw /metrics response.
	metricsBody string

	// filesUnavailable flips the native endpoint off, modelling a deployment where
	// it is not routed (or a proxy that strips the token header): the answer then
	// is the mux's plain 404, which is the only reply that justifies the fallback.
	filesUnavailable bool
	// unreadable lists paths the TARGET USER may not open. The endpoint still
	// serves them, because envd's /files opens files as the daemon user; that
	// asymmetry is exactly what the read gate exists to close.
	unreadable map[string]bool
	// modes pins the permission bits Stat reports, so the gate's world-readable
	// fast path is testable.
	modes map[string]uint32
	// filesLog records one entry per /files request: "path range=<header>".
	filesLog []string
	// grepFrames splits the stubbed search output into N data frames, so a test can
	// tell "the client stopped reading early" apart from "the client drained
	// everything and trimmed the string". streamCut records that a later write
	// failed, which is exactly what happens when the reader cancels.
	grepFrames int
	streamCut  atomic.Bool

	statCalls    atomic.Int64
	processCalls atomic.Int64
	// catCalls and readableChecks count the two specific process-channel calls, so
	// a test can assert "no cat was needed" without subtracting the jail's own
	// target-side canonicalization, which spawns once per resolved path.
	catCalls       atomic.Int64
	readableChecks atomic.Int64
	removed        []string
	moved          [][2]string
	created        []string
	// controls records process control RPCs (SendInput/SendSignal/Update) with
	// their request bodies.
	controls []string
}

// controlLog returns a copy of the recorded process control calls.
func (s *envdStub) controlLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.controls...)
}

// canRead answers the way the target kernel would for the executing user.
func (s *envdStub) canRead(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.unreadable[target]
}

// filesRequests copies the recorded /files requests.
func (s *envdStub) filesRequests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.filesLog...)
}

// disableFiles models an environment without the native endpoint.
func (s *envdStub) disableFiles() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filesUnavailable = true
}

// markUnreadable declares that the executing user may not open a path.
func (s *envdStub) markUnreadable(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unreadable == nil {
		s.unreadable = map[string]bool{}
	}
	s.unreadable[target] = true
}

// pinMode sets the permission bits Stat reports for one path.
func (s *envdStub) pinMode(target string, mode uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modes == nil {
		s.modes = map[string]uint32{}
	}
	s.modes[target] = mode
}

func (s *envdStub) modeFor(target string) (uint32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mode, ok := s.modes[target]
	return mode, ok
}

// envdStubPermissions renders the textual form Stat reports for a pinned mode,
// so a test that pins 0o640 sees "-rw-r-----" the way the daemon would answer.
func envdStubPermissions(mode uint32) string {
	const units = "rwx"
	text := []byte{'-'}
	for class := 2; class >= 0; class-- {
		for bit := 2; bit >= 0; bit-- {
			if mode&(1<<uint(class*3+bit)) != 0 {
				// bit 2 is the read position, so it maps to units[0].
				text = append(text, units[2-bit])
			} else {
				text = append(text, '-')
			}
		}
	}
	return string(text)
}

func (s *envdStub) content(target string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.files[target]
	return value, ok
}

func (s *envdStub) put(target, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[target] = value
}

func (s *envdStub) history() (removed []string, moved [][2]string, created []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.removed...), append([][2]string(nil), s.moved...), append([]string(nil), s.created...)
}

// isDir reports whether the stub treats a path as a directory.
func (s *envdStub) isDir(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.dirs[target]; ok {
		return true
	}
	for candidate := range s.files {
		if strings.HasPrefix(candidate, target+"/") {
			return true
		}
	}
	return false
}

// entriesFor returns the ListDir payload for one path: an explicit pin when set,
// otherwise one synthesized level from the file map.
func (s *envdStub) entriesFor(target string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pinned, ok := s.dirs[target]; ok {
		return append([]map[string]any(nil), pinned...)
	}
	seen := map[string]bool{}
	var out []map[string]any
	prefix := target + "/"
	for candidate, content := range s.files {
		if !strings.HasPrefix(candidate, prefix) {
			continue
		}
		rest := strings.TrimPrefix(candidate, prefix)
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		if seen[rest] {
			continue
		}
		seen[rest] = true
		out = append(out, map[string]any{
			"name":        rest,
			"path":        candidate,
			"type":        "FILE_TYPE_FILE",
			"size":        fmt.Sprintf("%d", len(content)),
			"mode":        420,
			"permissions": "-rw-r--r--",
			"owner":       "dev",
			"group":       "dev",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["name"]) < fmt.Sprint(out[j]["name"])
	})
	return out
}

func newEnvdStub(t *testing.T, files, links map[string]string) *envdStub {
	t.Helper()

	stub := &envdStub{
		files:       map[string]string{},
		links:       map[string]string{},
		dirs:        map[string][]map[string]any{},
		metricsBody: `{"ts":1,"cpu_count":4,"mem_total":4294967296,"mem_used":1073741824,"disk_used":2097152,"disk_total":10737418240}`,
	}
	for key, value := range files {
		stub.files[key] = value
	}
	for key, value := range links {
		stub.links[key] = value
	}
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, payload any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}
	writeConnectError := func(w http.ResponseWriter, status int, code, message string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"code":%q,"message":%q}`, code, message)
	}

	mux.HandleFunc("/filesystem.Filesystem/Stat", func(w http.ResponseWriter, r *http.Request) {
		stub.statCalls.Add(1)
		var in struct {
			Path string `json:"path"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &in)
		if stub.isDir(in.Path) {
			writeJSON(w, map[string]any{"entry": map[string]any{
				"name": base(in.Path), "path": in.Path, "type": "FILE_TYPE_DIRECTORY",
				"size": "4096", "mode": 511, "permissions": "drwxr-xr-x",
			}})
			return
		}
		content, ok := stub.content(in.Path)
		if !ok {
			writeConnectError(w, http.StatusNotFound, "not_found", "no such file")
			return
		}
		mode, permissions := uint32(420), "-rw-r--r--"
		if pinned, ok := stub.modeFor(in.Path); ok {
			mode = pinned
			permissions = envdStubPermissions(pinned)
		}
		writeJSON(w, map[string]any{"entry": map[string]any{
			"name": base(in.Path), "path": in.Path, "type": "FILE_TYPE_FILE",
			"size": fmt.Sprintf("%d", len(content)), "mode": mode, "permissions": permissions,
			"owner": "dev", "group": "dev",
		}})
	})

	mux.HandleFunc("/filesystem.Filesystem/ListDir", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path  string `json:"path"`
			Depth uint32 `json:"depth"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &in)
		writeJSON(w, map[string]any{"entries": stub.entriesFor(in.Path)})
	})

	mux.HandleFunc("/filesystem.Filesystem/MakeDir", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &in)
		if stub.isDir(in.Path) {
			writeConnectError(w, http.StatusBadRequest, "already_exists", "path already exists")
			return
		}
		stub.mu.Lock()
		stub.created = append(stub.created, in.Path)
		stub.dirs[in.Path] = []map[string]any{}
		stub.mu.Unlock()
		writeJSON(w, map[string]any{})
	})

	mux.HandleFunc("/filesystem.Filesystem/Remove", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &in)
		_, isFile := stub.content(in.Path)
		if !isFile && !stub.isDir(in.Path) {
			writeConnectError(w, http.StatusNotFound, "not_found", "no such path")
			return
		}
		stub.mu.Lock()
		stub.removed = append(stub.removed, in.Path)
		delete(stub.files, in.Path)
		delete(stub.dirs, in.Path)
		stub.mu.Unlock()
		writeJSON(w, map[string]any{})
	})

	mux.HandleFunc("/filesystem.Filesystem/Move", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &in)
		if _, ok := stub.content(in.Source); !ok {
			writeConnectError(w, http.StatusNotFound, "not_found", "no such source")
			return
		}
		stub.mu.Lock()
		stub.moved = append(stub.moved, [2]string{in.Source, in.Destination})
		stub.files[in.Destination] = stub.files[in.Source]
		delete(stub.files, in.Source)
		stub.mu.Unlock()
		writeJSON(w, map[string]any{})
	})

	mux.HandleFunc("/filesystem.Filesystem/WatchDir", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/connect+json")
		writeEnvDFrame(t, w, map[string]any{"start": map[string]any{}})
		writeEnvDFrame(t, w, map[string]any{"filesystem": map[string]any{"name": "probe.txt", "type": "EVENT_TYPE_CREATE"}})
	})

	// Process control RPCs (unary): record what was sent so a test can assert the
	// pty session actually drove the target.
	for _, method := range []string{"SendInput", "SendSignal", "Update"} {
		name := method
		mux.HandleFunc("/process.Process/"+name, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			stub.mu.Lock()
			stub.controls = append(stub.controls, name+" "+strings.TrimSpace(string(body)))
			stub.mu.Unlock()
			writeJSON(w, map[string]any{})
		})
	}

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, stub.metricsBody)
	})

	// The native file endpoint. Its measured shape (envd 0.2.11): JSON 404 for a
	// missing path, 400 mentioning "is a directory", 206 with Content-Range for a
	// byte range, and - deliberately - no permission check, because the daemon
	// opens files as itself rather than as the requesting user.
	mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("path")
		username := r.URL.Query().Get("username")
		requested := r.Header.Get("Range")
		stub.mu.Lock()
		stub.filesLog = append(stub.filesLog, target+" range="+requested)
		unavailable := stub.filesUnavailable
		stub.mu.Unlock()

		if unavailable {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found\n"))
			return
		}
		if username == "" {
			_, _ = w.Write([]byte("Query argument username is required, but not found"))
			return
		}
		if stub.isDir(target) {
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", fmt.Sprintf("path '%s' is a directory", target))
			return
		}
		content, ok := stub.content(target)
		if !ok {
			writeConnectError(w, http.StatusNotFound, "not_found", fmt.Sprintf("path '%s' does not exist", target))
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		if requested != "" && len(content) > 0 {
			var start, end int
			if _, err := fmt.Sscanf(requested, "bytes=%d-%d", &start, &end); err == nil && start < len(content) {
				if end >= len(content) {
					end = len(content) - 1
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte(content[start : end+1]))
				return
			}
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write([]byte(content))
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
		arg := func(index int) string {
			if index < 0 || index >= len(req.Process.Args) {
				return ""
			}
			return req.Process.Args[index]
		}
		last := func() string {
			if len(req.Process.Args) == 0 {
				return ""
			}
			return req.Process.Args[len(req.Process.Args)-1]
		}

		switch req.Process.Cmd {
		case "/bin/sh":
			// The argv shape is ["-c", script, label, args...]: the label says
			// which synthetic operation this invocation stands for.
			switch arg(2) {
			case "aicli-envd-resolve":
				target := last()
				resolved := target
				stub.mu.Lock()
				if link, ok := stub.links[target]; ok {
					resolved = link
				}
				stub.mu.Unlock()
				writeProcessStream(t, w, []byte(resolved+"\n"), nil, 0)
			case "aicli-envd-grep":
				pattern, root := arg(3), arg(4)
				stub.mu.Lock()
				var matches []string
				for candidate, content := range stub.files {
					if !strings.HasPrefix(candidate, root) {
						continue
					}
					for number, line := range strings.Split(content, "\n") {
						if strings.Contains(line, pattern) {
							matches = append(matches, fmt.Sprintf("%s:%d:%s", candidate, number+1, line))
							break
						}
					}
				}
				frames := stub.grepFrames
				stub.mu.Unlock()
				sort.Strings(matches)
				out := strings.Join(matches, "\n")
				if out != "" {
					out += "\n"
				}
				// The engine marker goes first, the way the real script does it: a
				// capped read must still report which dialect produced it.
				if frames > 0 {
					cut := false
					if !writeEnvDFrameSoft(w, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 9}}}) {
						cut = true
					}
					marker := base64.StdEncoding.EncodeToString([]byte("__TOOL__=rg\n"))
					if !cut && !writeEnvDFrameSoft(w, map[string]any{"event": map[string]any{"data": map[string]any{"stdout": marker}}}) {
						cut = true
					}
					for index := 0; index < frames && !cut; index++ {
						chunk := fmt.Sprintf("/srv/app/f%d.md:1:%s\n", index, strings.Repeat("x", 64))
						encoded := base64.StdEncoding.EncodeToString([]byte(chunk))
						if !writeEnvDFrameSoft(w, map[string]any{"event": map[string]any{"data": map[string]any{"stdout": encoded}}}) {
							cut = true
						}
					}
					if !cut && !writeEnvDFrameSoft(w, map[string]any{"event": map[string]any{"end": map[string]any{"exited": true, "status": "exit status 0"}}}) {
						cut = true
					}
					if cut {
						// The reader stopped consuming: that is the whole point of a
						// capped search, and it is what a test must be able to see.
						stub.streamCut.Store(true)
					}
					return
				}
				writeProcessStream(t, w, []byte("__TOOL__=rg\n"+out), nil, 0)
			case "aicli-envd-write":
				stub.put(arg(3), arg(4))
				writeProcessStream(t, w, nil, nil, 0)
			case "aicli-envd-readable":
				// `test -r "$1"`, answered by the stub the way the target kernel
				// would: exit 1 with a permission complaint when the executing user
				// cannot open the path.
				stub.readableChecks.Add(1)
				if stub.canRead(arg(3)) {
					writeProcessStream(t, w, nil, nil, 0)
					return
				}
				writeProcessStream(t, w, nil, []byte(fmt.Sprintf("sh: 1: test: cannot open %s: Permission denied\n", arg(3))), 1)
			default:
				// No label means a caller-supplied command (the shell tool), not one
				// of the backend's internal fixed scripts.
				writeProcessStream(t, w, []byte("ran: "+strings.Join(req.Process.Args[1:], " ")+"\n"), nil, 0)
			}
		case "/bin/cat":
			stub.catCalls.Add(1)
			content, ok := stub.content(last())
			if !ok {
				writeProcessStream(t, w, nil, []byte("cat: no such file\n"), 1)
				return
			}
			writeProcessStream(t, w, []byte(content), nil, 0)
		case "/usr/bin/find":
			root, pattern := arg(0), arg(2)
			stub.mu.Lock()
			var hits []string
			for candidate := range stub.files {
				if !strings.HasPrefix(candidate, root+"/") {
					continue
				}
				if matchesBaseGlob(base(candidate), pattern) {
					hits = append(hits, candidate)
				}
			}
			stub.mu.Unlock()
			sort.Strings(hits)
			if len(hits) == 0 {
				writeProcessStream(t, w, nil, nil, 0)
				return
			}
			writeProcessStream(t, w, []byte(strings.Join(hits, "\n")+"\n"), nil, 0)
		default:
			writeProcessStream(t, w, nil, []byte("stub: unexpected command "+req.Process.Cmd+"\n"), 127)
		}
	})

	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)
	return stub
}

// matchesBaseGlob implements the tiny subset of base-name globbing the find stub
// needs: "*.ext" and exact names.
func matchesBaseGlob(name, pattern string) bool {
	switch {
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(name, strings.TrimPrefix(pattern, "*"))
	default:
		return name == pattern
	}
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
	if !writeEnvDFrameSoft(w, payload) {
		t.Fatalf("write frame: stream closed by the client")
	}
}

// writeEnvDFrameSoft writes one frame and reports whether it landed. A failed
// write is not a stub bug: it means the reader stopped consuming, which is the
// behaviour a capped search is supposed to produce.
func writeEnvDFrameSoft(w io.Writer, payload any) bool {
	frame, err := envdclient.EncodeJSONFrame(payload)
	if err != nil {
		return false
	}
	if _, err := w.Write(frame); err != nil {
		return false
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return true
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

// TestEnvDServerTierSurface pins the档位 table the merge plan promised: a shared
// name means the same thing on both backends, and envd-only tools join the same
// ladder instead of defining a second one.
func TestEnvDServerTierSurface(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/README.md": "# hello envd\n"}, nil)

	t.Run("readonly", func(t *testing.T) {
		server, err := remotetools.NewServer(newEnvDConfig(stub.server.URL), "envd")
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		defer server.Close()
		want := []string{
			"remote_envd__glob", "remote_envd__grep", "remote_envd__host_metrics",
			"remote_envd__ls", "remote_envd__view", "remote_envd__watch_dir",
		}
		got := server.ToolNames()
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("readonly surface = %v, want %v", got, want)
		}
	})

	t.Run("standard adds the file mutation surface", func(t *testing.T) {
		cfg := newEnvDConfig(stub.server.URL)
		cfg.Profile = remotetools.ProfileStandard
		server, err := remotetools.NewServer(cfg, "envd")
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		defer server.Close()
		want := []string{
			"remote_envd__append_write", "remote_envd__apply_patch", "remote_envd__edit",
			"remote_envd__glob", "remote_envd__grep", "remote_envd__host_metrics",
			"remote_envd__ls", "remote_envd__mkdir", "remote_envd__move",
			"remote_envd__remove", "remote_envd__view", "remote_envd__watch_dir",
			"remote_envd__write",
		}
		got := server.ToolNames()
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("standard surface = %v, want %v", got, want)
		}
	})

	t.Run("ops adds the process and interactive surface", func(t *testing.T) {
		cfg := newEnvDConfig(stub.server.URL)
		cfg.Profile = remotetools.ProfileOps
		cfg.Shell.AllowedCommands = []string{"echo"}
		server, err := remotetools.NewServer(cfg, "envd")
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		defer server.Close()
		// The full ops surface, asserted exactly: a tier may only advertise tools
		// this backend actually implements.
		want := []string{
			"remote_envd__append_write", "remote_envd__apply_patch", "remote_envd__edit",
			"remote_envd__glob", "remote_envd__grep", "remote_envd__host_metrics",
			"remote_envd__ls", "remote_envd__mkdir", "remote_envd__move",
			"remote_envd__pty_close", "remote_envd__pty_read", "remote_envd__pty_resize",
			"remote_envd__pty_start", "remote_envd__pty_write", "remote_envd__remove",
			"remote_envd__shell", "remote_envd__view", "remote_envd__watch_dir",
			"remote_envd__write",
		}
		got := server.ToolNames()
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("ops surface = %v, want %v", got, want)
		}
	})

	t.Run("readOnly override drops mutations", func(t *testing.T) {
		cfg := newEnvDConfig(stub.server.URL)
		cfg.Profile = remotetools.ProfileOps
		cfg.ReadOnly = true
		server, err := remotetools.NewServer(cfg, "envd")
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		defer server.Close()
		for _, name := range server.ToolNames() {
			if strings.HasSuffix(name, "__write") || strings.HasSuffix(name, "__remove") ||
				strings.HasSuffix(name, "__mkdir") || strings.HasSuffix(name, "__move") ||
				strings.HasSuffix(name, "__edit") || strings.HasSuffix(name, "__append_write") ||
				strings.HasSuffix(name, "__apply_patch") {
				t.Fatalf("readOnly override leaked a mutation tool: %v", server.ToolNames())
			}
		}
	})
}

// containsName reports whether one exposed tool name is present.
func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
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
	var hasView bool
	for _, declared := range tools.Tools {
		if declared.Name == "remote_envd__view" {
			hasView = true
		}
	}
	if !hasView {
		t.Fatalf("tools missing remote_envd__view: %d advertised", len(tools.Tools))
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
