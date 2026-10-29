package envdclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// rangeStub serves a real file over /files with Range support, plus the one Stat
// call a tail read needs. It records every Range header it saw, which is how the
// "no whole-body retry" rule is proven rather than asserted in prose.
type rangeStub struct {
	server   *httptest.Server
	path     string
	content  string
	ranges   []string
	requests int
	// statusFor makes the next reply a fixed status, to test the 416 path.
	rejectWith int
}

func newRangeStub(t *testing.T, content string) *rangeStub {
	t.Helper()
	stub := &rangeStub{path: "/srv/app/data.bin", content: content}
	mux := http.NewServeMux()
	mux.HandleFunc(FilesPath, func(w http.ResponseWriter, r *http.Request) {
		stub.requests++
		requested := r.Header.Get("Range")
		stub.ranges = append(stub.ranges, requested)
		if stub.rejectWith != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(stub.content)))
			w.WriteHeader(stub.rejectWith)
			_, _ = w.Write([]byte(`{"code":"416","message":"range not satisfiable"}`))
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		if requested == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(stub.content)))
			_, _ = w.Write([]byte(stub.content))
			return
		}
		var off, end int
		if _, err := fmt.Sscanf(requested, "bytes=%d-%d", &off, &end); err != nil {
			// `bytes=10-` (open-ended) parses with the second value missing.
			if _, openErr := fmt.Sscanf(requested, "bytes=%d-", &off); openErr != nil {
				http.Error(w, "bad range", http.StatusBadRequest)
				return
			}
			end = len(stub.content) - 1
		}
		if off >= len(stub.content) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(stub.content)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end >= len(stub.content) {
			end = len(stub.content) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, end, len(stub.content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(stub.content[off : end+1]))
	})
	mux.HandleFunc("/filesystem.Filesystem/Stat", func(w http.ResponseWriter, r *http.Request) {
		entry := map[string]any{
			"name": "data.bin", "path": stub.path, "type": "FILE_TYPE_FILE",
			"size": fmt.Sprintf("%d", len(stub.content)), "mode": 420,
			"permissions": "-rw-r--r--", "owner": "n9e", "group": "n9e",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"entry": entry})
	})
	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *rangeStub) client(t *testing.T) *Client {
	t.Helper()
	client, err := Dial(Config{Addr: s.server.URL, User: "n9e", AccessToken: "tok"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return client
}

func TestReadRangeFetchesTheMiddle(t *testing.T) {
	body := strings.Repeat(".", 100) + "MIDDLE" + strings.Repeat(".", 394)
	stub := newRangeStub(t, body)

	read, err := stub.client(t).ReadRange(context.Background(), stub.path, 100, 6)
	if err != nil {
		t.Fatalf("read range: %v", err)
	}
	if string(read.Data) != "MIDDLE" {
		t.Fatalf("data = %q, want the bytes at that offset", read.Data)
	}
	if stub.ranges[0] != "bytes=100-105" {
		t.Fatalf("range = %q", stub.ranges[0])
	}
	if read.StatusCode != http.StatusPartialContent || read.Total != int64(len(body)) {
		t.Fatalf("status=%d total=%d", read.StatusCode, read.Total)
	}
	// Partial must be judged against the end of *this* window. Saying false here
	// would tell a paging caller that the file ended at byte 106.
	if !read.Partial {
		t.Fatal("a middle window with bytes after it must report Partial=true")
	}
}

func TestReadRangeToTheEndOfTheFile(t *testing.T) {
	body := strings.Repeat("x", 50)
	stub := newRangeStub(t, body)

	read, err := stub.client(t).ReadRange(context.Background(), stub.path, 40, 0)
	if err != nil {
		t.Fatalf("read to EOF: %v", err)
	}
	if string(read.Data) != strings.Repeat("x", 10) {
		t.Fatalf("data = %q, want the last 10 bytes", read.Data)
	}
	if read.Partial {
		t.Fatal("a window that reaches the end must not claim more data")
	}
}

// A window entirely past EOF must error. ReadFile may fall back to a whole-body
// read; this one must never do that, or a 5 GB file would be pulled into memory to
// answer a request for 16 bytes.
func TestReadRangeNeverRetries416AsWholeBody(t *testing.T) {
	body := strings.Repeat("y", 64)
	stub := newRangeStub(t, body)

	_, err := stub.client(t).ReadRange(context.Background(), stub.path, 1000, 16)
	if !IsFilesCode(err, FilesRangeUnsatisfiable) {
		t.Fatalf("want range_unsatisfiable, got %v", err)
	}
	if len(stub.ranges) != 1 || stub.ranges[0] == "" {
		t.Fatalf("requests = %v, want exactly one ranged request and no whole-body fallback", stub.ranges)
	}
	// The refusal carries the real size, which is how a caller re-resolves a tail.
	var filesErr *FilesError
	if !asFilesError(err, &filesErr) || filesErr.Total != 64 {
		t.Fatalf("error should carry total=64, got %+v", filesErr)
	}
}

func TestReadRangeTailResolvesAgainstStat(t *testing.T) {
	body := strings.Repeat("a", 90) + "TAIL"
	stub := newRangeStub(t, body)

	read, err := stub.client(t).ReadRange(context.Background(), stub.path, -4, 0)
	if err != nil {
		t.Fatalf("tail read: %v", err)
	}
	if string(read.Data) != "TAIL" {
		t.Fatalf("data = %q, want the last 4 bytes", read.Data)
	}
	if stub.ranges[0] != "bytes=90-" {
		t.Fatalf("range = %q, want it resolved from the size", stub.ranges[0])
	}
}

// A tail longer than the file is the file, not an error.
func TestReadRangeTailLongerThanFile(t *testing.T) {
	body := "short"
	stub := newRangeStub(t, body)

	read, err := stub.client(t).ReadRange(context.Background(), stub.path, -9999, 0)
	if err != nil {
		t.Fatalf("tail beyond start: %v", err)
	}
	if string(read.Data) != body {
		t.Fatalf("data = %q, want the whole file", read.Data)
	}
	if len(stub.ranges) != 1 || stub.ranges[0] != "" {
		t.Fatalf("ranges = %v, want a single whole-body read with no Range header", stub.ranges)
	}
}

func TestProbeCapabilitiesReportsWhatItMeasured(t *testing.T) {
	body := strings.Repeat("z", 200)
	stub := newRangeStub(t, body)

	caps, err := stub.client(t).ProbeCapabilities(context.Background(), stub.path, false)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !caps.RangeRead {
		t.Fatal("a 206 mid-file answer must set RangeRead")
	}
	if caps.RangeWrite {
		t.Fatal("nothing was measured for partial writes, so it must be reported false")
	}
	if caps.ProcessChannel || caps.HasDD || caps.LineWindow {
		t.Fatal("process probes must not run unless asked for")
	}
	if !strings.Contains(strings.Join(stub.ranges, "|"), "bytes=1-1") {
		t.Fatalf("ranges = %v, want a second request that proves the offset is honoured", stub.ranges)
	}
}

func TestProbeCapabilitiesWithoutSampleIsNotSilentlyTrue(t *testing.T) {
	stub := newRangeStub(t, "abc")
	caps, err := stub.client(t).ProbeCapabilities(context.Background(), "", false)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if caps.RangeRead {
		t.Fatal("\"not measured\" must not be reported as \"supported\"")
	}
	if len(stub.ranges) != 0 {
		t.Fatalf("no read should have happened, ranges = %v", stub.ranges)
	}
}

// TestLineWindowReportRejectsEveryRealDeploymentDifference pins the acceptance
// predicate case by case. Each row is a machine that exists in the wild and would
// otherwise produce a window that cannot be trusted about the end of the file.
func TestLineWindowReportRejectsEveryRealDeploymentDifference(t *testing.T) {
	correct := []byte(LineWindowProbeStdout)
	tests := []struct {
		name   string
		stdout []byte
		stderr []byte
		code   int32
		want   bool
	}{
		{"gnu awk answer", correct, []byte("LINECOUNT=2\n"), 0, true},
		{"nul rewritten by the transport", []byte("a\xEF\xBF\xBDb\nlast-unterminated\n"), []byte("LINECOUNT=2\n"), 0, false},
		{"final unterminated line dropped", []byte("a\x00b\n"), []byte("LINECOUNT=1\n"), 0, false},
		{"no /dev/stderr side channel", correct, nil, 0, false},
		{"count uses the wc -l rule", correct, []byte("LINECOUNT=1\n"), 0, false},
		{"awk is not installed", nil, []byte("sh: awk: not found"), 127, false},
		{"zero exit but no output", nil, nil, 0, false},
		{"window carries stray diagnostics", correct, []byte("LINECOUNT=2\nwarning: something\n"), 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := lineWindowReport(test.stdout, test.stderr, test.code); got != test.want {
				t.Fatalf("verdict = %v, want %v", got, test.want)
			}
		})
	}
}

// TestLineWindowProbeExpectsTwoRecords documents what the probe sample asserts, so a
// future edit to the script cannot silently change the meaning of "supported".
func TestLineWindowProbeExpectsTwoRecords(t *testing.T) {
	if !strings.Contains(LineWindowProbeScript, `-v a=1 -v b=2`) {
		t.Fatal("the probe asks for a two-record window; if that changed, the expectations changed with it")
	}
	if !strings.HasSuffix(LineWindowProbeStdout, "\n") {
		t.Fatal("awk's print terminates the unterminated final line; that is part of what the probe proves")
	}
	if strings.Contains(LineWindowProbeStdout, "\r") {
		t.Fatal("the probe sample must not depend on carriage returns")
	}
}
func TestProbeCapabilitiesDetectsMissingChannel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client, err := Dial(Config{Addr: server.URL, User: "n9e", AccessToken: "bad"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, probeErr := client.ProbeCapabilities(context.Background(), "/srv/app/x", false); probeErr == nil {
		t.Fatal("an unauthorized target must fail the probe loudly, not report empty capabilities")
	}
}

// asFilesError keeps the typed-error check local to the tests that need a field.
func asFilesError(err error, target **FilesError) bool {
	fileErr, ok := err.(*FilesError)
	if ok {
		*target = fileErr
	}
	return ok
}
