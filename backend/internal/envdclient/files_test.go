package envdclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// filesStub mimics the endpoint's measured behaviour (envd 0.2.11, 2026-10-28):
// it answers with a JSON 404 for a missing path and a plain-text 404 when the
// route is absent, and those two mean opposite things to the caller.
type filesStub struct {
	server   *httptest.Server
	content  map[string]string
	dirPaths map[string]bool
	// noRoute makes the endpoint answer like an unregistered route.
	noRoute bool
	// clampRange=false makes a range whose end passes EOF answer 416, which is how
	// the ReadFile retry path is exercised.
	clampRange   bool
	unauthorized bool

	requests []string
	ranges   []string
}

func (f *filesStub) record(r *http.Request) {
	f.requests = append(f.requests, r.URL.Query().Get("path"))
	f.ranges = append(f.ranges, r.Header.Get("Range"))
}

func newFilesStub(t *testing.T, content map[string]string) *filesStub {
	t.Helper()
	stub := &filesStub{content: content, clampRange: true, dirPaths: map[string]bool{}}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != FilesPath {
			http.NotFound(w, r)
			return
		}
		stub.record(r)
		if stub.noRoute {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found\n"))
			return
		}
		if stub.unauthorized {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"missing signature query parameter"}`))
			return
		}
		if r.URL.Query().Get("username") == "" {
			_, _ = w.Write([]byte("Query argument username is required, but not found"))
			return
		}
		target := r.URL.Query().Get("path")
		if stub.dirPaths[target] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"code":400,"message":"path '%s' is a directory"}`, target)
			return
		}
		body, ok := stub.content[target]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"code":404,"message":"path '%s' does not exist"}`, target)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Last-Modified", "Wed, 28 Oct 2026 14:20:32 GMT")

		if rng := r.Header.Get("Range"); rng != "" {
			var start, end int
			if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
				http.Error(w, "bad range", http.StatusBadRequest)
				return
			}
			if start >= len(body) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(body)))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if end >= len(body) {
				if !stub.clampRange {
					w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(body)))
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				end = len(body) - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(body[start : end+1]))
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func filesClient(t *testing.T, stub *filesStub) *Client {
	t.Helper()
	client, err := Dial(Config{Addr: stub.server.URL, User: "n9e", AccessToken: "tok"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return client
}

func TestReadFileWholeBody(t *testing.T) {
	stub := newFilesStub(t, map[string]string{"/home/n9e/app.log": "hello world"})
	read, err := filesClient(t, stub).ReadFile(context.Background(), "/home/n9e/app.log", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(read.Data) != "hello world" {
		t.Fatalf("data = %q", read.Data)
	}
	if read.StatusCode != http.StatusOK || read.Total != 11 || read.Partial {
		t.Fatalf("status=%d total=%d partial=%v", read.StatusCode, read.Total, read.Partial)
	}
	if len(stub.ranges) != 1 || stub.ranges[0] != "" {
		t.Fatalf("no Range may be sent for an unbounded read, got %q", stub.ranges)
	}
}

// A bounded read is the point of this endpoint: the transfer must cost the
// window, not the file.
func TestReadFileWithCapUsesRangeAndReportsPartial(t *testing.T) {
	body := strings.Repeat("x", 5000)
	stub := newFilesStub(t, map[string]string{"/big": body})
	read, err := filesClient(t, stub).ReadFile(context.Background(), "/big", 1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if stub.ranges[0] != "bytes=0-1023" {
		t.Fatalf("range = %q", stub.ranges[0])
	}
	if len(read.Data) != 1024 || !read.Partial || read.Total != 5000 {
		t.Fatalf("len=%d partial=%v total=%d", len(read.Data), read.Partial, read.Total)
	}
	if read.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d", read.StatusCode)
	}
}

func TestReadFileCapLargerThanFile(t *testing.T) {
	stub := newFilesStub(t, map[string]string{"/small": "abc"})
	read, err := filesClient(t, stub).ReadFile(context.Background(), "/small", 4096)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(read.Data) != "abc" || read.Partial {
		t.Fatalf("data=%q partial=%v", read.Data, read.Partial)
	}
}

// 0.2.11 clamps a range end past EOF; a build that answers 416 instead must still
// produce the same bytes rather than an error, so the caller never sees a
// version-dependent failure.
func TestReadFileRetriesWhenRangeRejected(t *testing.T) {
	stub := newFilesStub(t, map[string]string{"/small": "abc"})
	stub.clampRange = false
	read, err := filesClient(t, stub).ReadFile(context.Background(), "/small", 4096)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(stub.requests) != 2 {
		t.Fatalf("expected a range attempt then a plain retry, got %d requests", len(stub.requests))
	}
	if stub.ranges[1] != "" {
		t.Fatalf("retry must drop the Range header, got %q", stub.ranges[1])
	}
	if string(read.Data) != "abc" {
		t.Fatalf("data = %q", read.Data)
	}
}

func TestReadFileErrorClassification(t *testing.T) {
	t.Run("missing path is an answer about the file", func(t *testing.T) {
		stub := newFilesStub(t, map[string]string{})
		_, err := filesClient(t, stub).ReadFile(context.Background(), "/gone", 10)
		if !IsFilesCode(err, FilesNotFound) {
			t.Fatalf("want not_found, got %v", err)
		}
		if IsFilesMissing(err) != true || IsFilesChannelIssue(err) {
			t.Fatalf("missing must not look like a channel issue: %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		stub := newFilesStub(t, map[string]string{})
		stub.dirPaths["/home/n9e"] = true
		_, err := filesClient(t, stub).ReadFile(context.Background(), "/home/n9e", 10)
		if !IsFilesCode(err, FilesIsDirectory) {
			t.Fatalf("want is_directory, got %v", err)
		}
	})

	// The mux's plain 404 means the endpoint is not there at all, which is the
	// only reason to fall back to a spawned cat.
	t.Run("unregistered route is a channel issue", func(t *testing.T) {
		stub := newFilesStub(t, map[string]string{})
		stub.noRoute = true
		_, err := filesClient(t, stub).ReadFile(context.Background(), "/any", 10)
		if !IsFilesCode(err, FilesChannelMissing) || !IsFilesChannelIssue(err) {
			t.Fatalf("want channel_missing, got %v", err)
		}
	})

	t.Run("unauthorized is a channel issue", func(t *testing.T) {
		stub := newFilesStub(t, map[string]string{})
		stub.unauthorized = true
		_, err := filesClient(t, stub).ReadFile(context.Background(), "/any", 10)
		if !IsFilesCode(err, FilesUnauthorized) || !IsFilesChannelIssue(err) {
			t.Fatalf("want unauthorized, got %v", err)
		}
	})

	t.Run("empty file answers 416 to a prefix range and still reads", func(t *testing.T) {
		// A zero-byte file cannot satisfy any range, so envd replies 416. The
		// caller must not turn that into an error: it retries once without the
		// header. This pins the empty-file roundtrip, one extra request included.
		stub := newFilesStub(t, map[string]string{"/empty": ""})
		read, err := filesClient(t, stub).ReadFile(context.Background(), "/empty", 4096)
		if err != nil {
			t.Fatalf("read empty file: %v", err)
		}
		if len(read.Data) != 0 || read.Partial {
			t.Fatalf("data=%q partial=%v", read.Data, read.Partial)
		}
		if len(stub.requests) != 2 {
			t.Fatalf("empty file should cost a range attempt and one retry, got %d requests", len(stub.requests))
		}
	})
}

func TestProbeFiles(t *testing.T) {
	t.Run("json 404 proves route and credentials", func(t *testing.T) {
		stub := newFilesStub(t, map[string]string{})
		if err := filesClient(t, stub).ProbeFiles(context.Background()); err != nil {
			t.Fatalf("probe: %v", err)
		}
		if len(stub.requests) != 1 {
			t.Fatalf("probe must send exactly one request, got %d", len(stub.requests))
		}
		if !strings.HasPrefix(stub.requests[0], "/.aicli-envd-files-probe-") {
			t.Fatalf("probe path = %q", stub.requests[0])
		}
	})

	for name, mutate := range map[string]func(*filesStub){
		"no route":     func(s *filesStub) { s.noRoute = true },
		"unauthorized": func(s *filesStub) { s.unauthorized = true },
	} {
		t.Run(name+" is reported", func(t *testing.T) {
			stub := newFilesStub(t, map[string]string{})
			mutate(stub)
			if err := filesClient(t, stub).ProbeFiles(context.Background()); err == nil {
				t.Fatal("probe must fail when the channel is not usable")
			}
		})
	}
}

// Paths are sent as query parameters, never interpolated into a command, so the
// spelling must survive verbatim.
func TestFilesURLKeepsPathVerbatim(t *testing.T) {
	for _, target := range []string{"/home/n9e/weird name.txt", "/home/n9e/q&x", "/home/n9e/中文.txt", "/home/n9e/a%b"} {
		stub := newFilesStub(t, map[string]string{target: "ok"})
		read, err := filesClient(t, stub).ReadFile(context.Background(), target, 0)
		if err != nil {
			t.Fatalf("read %q: %v", target, err)
		}
		if string(read.Data) != "ok" {
			t.Fatalf("read %q got %q", target, read.Data)
		}
	}
}

func TestParseContentRangeTotal(t *testing.T) {
	cases := map[string]int64{
		"bytes 0-9/3814": 3814,
		// A 416 carries the size in this form, so it is parsed too: knowing the
		// file is 3814 bytes makes the refusal actionable.
		"bytes */3814": 3814,
		"":             -1,
		"bytes 0-9/":   -1,
		"garbage":      -1,
	}
	for header, want := range cases {
		if got := parseContentRangeTotal(header); got != want {
			t.Fatalf("parseContentRangeTotal(%q) = %d, want %d", header, got, want)
		}
	}
}

func TestFilesErrorMessageIsBounded(t *testing.T) {
	reply := classifyFilesReply(http.StatusInternalServerError, "application/json", []byte(strings.Repeat("y", 900)), "/x")
	if len(reply.Message) > 210 {
		t.Fatalf("message length = %d", len(reply.Message))
	}
	if !strings.HasSuffix(reply.Message, "...") {
		t.Fatalf("message should be shortened, got %q", reply.Message)
	}
}
