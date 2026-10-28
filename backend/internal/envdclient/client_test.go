package envdclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestFrameRoundTrip covers the envelope both ways.
func TestFrameRoundTrip(t *testing.T) {
	payload := []byte(`{"process":{"cmd":"/bin/echo"}}`)
	frame := EncodeFrame(flagMessage, payload)

	if len(frame) != frameHeaderBytes+len(payload) {
		t.Fatalf("frame length = %d, want %d", len(frame), frameHeaderBytes+len(payload))
	}
	if frame[0] != flagMessage {
		t.Fatalf("frame flag = 0x%02x, want 0x%02x", frame[0], flagMessage)
	}

	got, err := ReadFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Flags != flagMessage {
		t.Errorf("decoded flag = 0x%02x, want 0x%02x", got.Flags, flagMessage)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("decoded payload = %q, want %q", got.Payload, payload)
	}
}

// TestReadFrameTruncatedHeader pins the truncated-header behaviour.
//
// A dropped connection must not look like a clean end of stream, otherwise a
// killed process is reported as a successful one.
func TestReadFrameTruncatedHeader(t *testing.T) {
	_, err := ReadFrame(bytes.NewReader([]byte{flagMessage, 0x00}))
	if err == nil {
		t.Fatal("ReadFrame on a 2-byte header returned no error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("truncated header reported as clean EOF: %v", err)
	}
}

// TestReadFrameInflatedLength rejects an implausible length prefix instead of
// attempting the allocation.
func TestReadFrameInflatedLength(t *testing.T) {
	header := []byte{flagMessage, 0xFF, 0xFF, 0xFF, 0xFF}
	_, err := ReadFrame(bytes.NewReader(header))
	if err == nil {
		t.Fatal("ReadFrame accepted a 4 GiB length prefix")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error = %v, want a limit message", err)
	}
}

// TestProcessEventStart decodes a real start frame captured from envd.
func TestProcessEventStart(t *testing.T) {
	var event ProcessEvent
	if err := json.Unmarshal([]byte(`{"event":{"start":{"pid":645}}}`), &event); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if event.Kind != EventStart {
		t.Fatalf("kind = %v, want start", event.Kind)
	}
	if event.Start == nil || event.Start.Pid != 645 {
		t.Fatalf("start = %+v, want pid 645", event.Start)
	}
}

// TestProcessEventDataBase64 decodes a real data frame.
//
// envd sends bytes fields base64-encoded; "aGVsbG8tZW52ZAo=" is "hello-envd\n".
func TestProcessEventDataBase64(t *testing.T) {
	var event ProcessEvent
	if err := json.Unmarshal([]byte(`{"event":{"data":{"stdout":"aGVsbG8tZW52ZAo="}}}`), &event); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if event.Kind != EventData {
		t.Fatalf("kind = %v, want data", event.Kind)
	}
	if string(event.Stdout) != "hello-envd\n" {
		t.Fatalf("stdout = %q, want %q", event.Stdout, "hello-envd\n")
	}
}

// TestProcessEventEndStatusZero covers the omitempty case: a clean exit omits
// exit_code entirely, and must still be reported as 0.
func TestProcessEventEndStatusZero(t *testing.T) {
	var event ProcessEvent
	if err := json.Unmarshal([]byte(`{"event":{"end":{"exited":true,"status":"exit status 0"}}}`), &event); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if event.Kind != EventEnd {
		t.Fatalf("kind = %v, want end", event.Kind)
	}
	if got := event.End.ExitStatus(); got != 0 {
		t.Fatalf("ExitStatus = %d, want 0", got)
	}
}

// TestProcessEventEndStatusNonZero covers the case where only the status string
// carries the code, so a failed process is not reported as successful.
func TestProcessEventEndStatusNonZero(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int32
	}{
		{"status string only", `{"event":{"end":{"exited":true,"status":"exit status 3"}}}`, 3},
		{"numeric field", `{"event":{"end":{"exited":true,"exit_code":7,"status":"exit status 7"}}}`, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var event ProcessEvent
			if err := json.Unmarshal([]byte(tc.body), &event); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := event.End.ExitStatus(); got != tc.want {
				t.Fatalf("ExitStatus = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestProcessEventEndSignalStatus covers processes that died from a signal.
//
// envd omits exit_code in that case and the status string carries "signal: X".
// Reporting such an end as 0 would turn a killed process into a successful one;
// the client reports 128+signum instead and refuses to guess at unknown names.
func TestProcessEventEndSignalStatus(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int32
	}{
		{"sigkill", `{"event":{"end":{"exited":true,"status":"signal: killed"}}}`, 137},
		{"sigterm", `{"event":{"end":{"exited":true,"status":"signal: terminated"}}}`, 143},
		{"sigint", `{"event":{"end":{"exited":true,"status":"signal: interrupt"}}}`, 130},
		{"unknown signal name", `{"event":{"end":{"exited":true,"status":"signal: frobnicated"}}}`, 255},
		{"unparseable status", `{"event":{"end":{"exited":true,"status":"terminated abruptly"}}}`, 255},
		{"empty status is success", `{"event":{"end":{"exited":true}}}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var event ProcessEvent
			if err := json.Unmarshal([]byte(tc.body), &event); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := event.End.ExitStatus(); got != tc.want {
				t.Fatalf("ExitStatus = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestProcessEventMissingArm rejects a frame with no event arm, since silently
// decoding it as an empty event would hide a protocol mismatch.
func TestProcessEventMissingArm(t *testing.T) {
	var event ProcessEvent
	err := json.Unmarshal([]byte(`{"pid":1}`), &event)
	if err == nil {
		t.Fatal("frame without an event arm decoded without error")
	}
}

// TestEntrySizeForms covers both wire forms of the size field.
//
// envd sends size as a JSON string ("4096"); the numeric form must also decode
// rather than surfacing as a corrupt listing.
func TestEntrySizeForms(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int64
	}{
		{"string size", `{"name":"tmp","type":"FILE_TYPE_DIRECTORY","path":"/tmp","size":"4096","mode":493}`, 4096},
		{"numeric size", `{"name":"tmp","type":"FILE_TYPE_DIRECTORY","path":"/tmp","size":4096,"mode":493}`, 4096},
		{"absent size", `{"name":"tmp","type":"FILE_TYPE_DIRECTORY","path":"/tmp"}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var entry Entry
			if err := json.Unmarshal([]byte(tc.body), &entry); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := entry.SizeBytes(); got != tc.want {
				t.Fatalf("SizeBytes = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestEntryRealPayload decodes a verbatim Stat response from envd.
func TestEntryRealPayload(t *testing.T) {
	body := `{"entry":{"name":"tmp","type":"FILE_TYPE_DIRECTORY","path":"/tmp","size":"4096","mode":511,"permissions":"dtrwxrwxrwx","owner":"root","group":"root","modifiedTime":"2026-10-27T15:30:03.889878Z"}}`
	var resp StatResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Entry == nil {
		t.Fatal("entry is nil")
	}
	if !resp.Entry.IsDir() {
		t.Errorf("IsDir = false, want true for %s", resp.Entry.Type)
	}
	if resp.Entry.Mode != 511 {
		t.Errorf("Mode = %d, want 511", resp.Entry.Mode)
	}
	if resp.Entry.SizeBytes() != 4096 {
		t.Errorf("SizeBytes = %d, want 4096", resp.Entry.SizeBytes())
	}
}

// TestDecodeUnaryError checks that a Connect error body becomes a typed error,
// so callers can branch on the code instead of matching prose.
func TestDecodeUnaryError(t *testing.T) {
	err := decodeUnaryError(401, []byte(`{"code":"unauthenticated","message":"no user specified"}`))
	var connectErr *ConnectError
	if !errors.As(err, &connectErr) {
		t.Fatalf("error type = %T, want *ConnectError", err)
	}
	if !connectErr.IsUnauthenticated() {
		t.Errorf("IsUnauthenticated = false for code %q", connectErr.Code)
	}
	if !strings.Contains(connectErr.Error(), "no user specified") {
		t.Errorf("Error() = %q, want it to include the server message", connectErr.Error())
	}
}

// TestDecodeUnaryErrorNotFound covers the missing-path case.
func TestDecodeUnaryErrorNotFound(t *testing.T) {
	err := decodeUnaryError(404, []byte(`{"code":"not_found","message":"file not found: x"}`))
	var connectErr *ConnectError
	if !errors.As(err, &connectErr) {
		t.Fatalf("error type = %T, want *ConnectError", err)
	}
	if !connectErr.IsNotFound() {
		t.Errorf("IsNotFound = false for code %q", connectErr.Code)
	}
}

// TestParseEndStream covers the terminal frame.
func TestParseEndStream(t *testing.T) {
	if err := parseEndStream([]byte(`{}`)); err != nil {
		t.Errorf("empty end-stream = %v, want nil", err)
	}
	err := parseEndStream([]byte(`{"error":{"code":"internal","message":"boom"}}`))
	if err == nil {
		t.Fatal("error end-stream returned nil")
	}
	var connectErr *ConnectError
	if !errors.As(err, &connectErr) || connectErr.Code != "internal" {
		t.Errorf("error = %v, want an internal ConnectError", err)
	}
}

// TestNormalizeAddr covers scheme and port inference.
func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"10.0.0.9", "http://10.0.0.9:49983", false},
		{"10.0.0.9:50000", "http://10.0.0.9:50000", false},
		{"http://10.0.0.9", "http://10.0.0.9:49983", false},
		{"https://envd.example.com", "https://envd.example.com:49983", false},
		{"http://10.0.0.9:49983/", "http://10.0.0.9:49983", false},
		{"", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := normalizeAddr(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeAddr(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeAddr(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeAddr(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDialRequiresUser locks in the one precondition envd enforces itself.
func TestDialRequiresUser(t *testing.T) {
	if _, err := Dial(Config{Addr: "10.0.0.9"}); err == nil {
		t.Fatal("Dial accepted a config with no user")
	}
}

// roundTripFunc adapts a function to http.RoundTripper so a test can inspect the
// outgoing request without a network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestAccessTokenHeader covers the hardening header.
//
// envd only enforces X-Access-Token after a token has been installed via
// /init, and an untokened daemon is an open remote shell for anyone who can
// reach its port — so the client must send the header exactly when configured.
func TestAccessTokenHeader(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"token set", "secret-token", "secret-token"},
		{"token empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotToken, gotUser string
			hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotToken = r.Header.Get(accessTokenHeader)
				gotUser, _, _ = r.BasicAuth()
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"cpu_count":1}`)),
				}, nil
			})}

			client, err := Dial(Config{
				Addr:        "127.0.0.1:49983",
				User:        "n9e",
				AccessToken: tc.token,
				HTTPClient:  hc,
			})
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			if _, err := client.Metrics(context.Background()); err != nil {
				t.Fatalf("Metrics: %v", err)
			}
			if gotToken != tc.want {
				t.Fatalf("%s = %q, want %q", accessTokenHeader, gotToken, tc.want)
			}
			if gotUser != "n9e" {
				t.Fatalf("Basic user = %q, want n9e", gotUser)
			}
		})
	}
}

// TestAppendLimited pins the retention helper's edge cases.
func TestAppendLimited(t *testing.T) {
	cases := []struct {
		name    string
		initial string
		chunk   string
		limit   int
		want    string
	}{
		{"under limit", "ab", "cd", 10, "abcd"},
		{"exactly full", "ab", "cd", 4, "abcd"},
		{"chunk clipped", "ab", "cdef", 4, "abcd"},
		{"already full", "abcd", "ef", 4, "abcd"},
		{"no cap", "ab", "cd", 0, "abcd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(appendLimited([]byte(tc.initial), []byte(tc.chunk), tc.limit))
			if got != tc.want {
				t.Fatalf("appendLimited(%q, %q, %d) = %q, want %q", tc.initial, tc.chunk, tc.limit, got, tc.want)
			}
		})
	}
}

// TestRunCapsBufferedOutput pins the memory bound: a chatty process must not make
// Run retain without limit, and the exit status must still be reported after the
// excess output is discarded.
func TestRunCapsBufferedOutput(t *testing.T) {
	const chunkSize = 1 << 20
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", chunkSize)))

	var body bytes.Buffer
	writeFrame := func(v any) {
		frame, err := EncodeJSONFrame(v)
		if err != nil {
			t.Fatalf("EncodeJSONFrame: %v", err)
		}
		body.Write(frame)
	}
	writeFrame(map[string]any{"event": map[string]any{"start": map[string]any{"pid": 1}}})
	for i := 0; i < 10; i++ {
		writeFrame(map[string]any{"event": map[string]any{"data": map[string]any{"stdout": encoded}}})
	}
	writeFrame(map[string]any{"event": map[string]any{"end": map[string]any{"exited": true, "status": "exit status 0"}}})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/connect+json")
		_, _ = w.Write(body.Bytes())
	}))
	defer srv.Close()

	client, err := Dial(Config{Addr: srv.URL, User: "n9e"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	stdout, _, code, err := client.Run(context.Background(), StartRequest{
		Process: ProcessConfig{Cmd: "/bin/echo"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if len(stdout) != DefaultMaxBufferedOutput {
		t.Fatalf("retained %d bytes, want the %d byte cap", len(stdout), DefaultMaxBufferedOutput)
	}
}

// liveClient builds a client from the environment, or skips.
//
// The live tests are the ones that matter most: everything else here validates
// this client against its own assumptions, while these validate it against a real
// envd. They are gated so an offline machine still gets a clean test run.
func liveClient(t *testing.T) *Client {
	t.Helper()
	addr := os.Getenv("ENVD_TEST_ADDR")
	if addr == "" {
		t.Skip("set ENVD_TEST_ADDR (and optionally ENVD_TEST_USER / ENVD_TEST_TOKEN / ENVD_TEST_CA_FILE) to run live tests")
	}
	user := os.Getenv("ENVD_TEST_USER")
	if user == "" {
		user = "user"
	}
	client, err := Dial(Config{
		Addr:        addr,
		User:        user,
		AccessToken: os.Getenv("ENVD_TEST_TOKEN"),
		TLS:         liveTLSConfig(t),
		Timeout:     20 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return client
}

// liveTLSConfig returns a trust pool for ENVD_TEST_CA_FILE, or nil for plaintext.
func liveTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	caFile := strings.TrimSpace(os.Getenv("ENVD_TEST_CA_FILE"))
	if caFile == "" {
		return nil
	}
	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("read ENVD_TEST_CA_FILE: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatalf("ENVD_TEST_CA_FILE %q contains no certificates", caFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// TestLiveMetrics proves reachability and that the metrics payload decodes.
func TestLiveMetrics(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	metrics, err := client.Metrics(ctx)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if metrics.CPUCount <= 0 {
		t.Errorf("CPUCount = %d, want > 0", metrics.CPUCount)
	}
	if metrics.MemTotal <= 0 {
		t.Errorf("MemTotal = %d, want > 0", metrics.MemTotal)
	}
	t.Logf("envd host: %d cpus, %d MiB ram, disk %d/%d",
		metrics.CPUCount, metrics.MemTotal>>20, metrics.DiskUsed, metrics.DiskTotal)
}

// TestLiveRunEcho proves the whole streaming path: envelope out, framed events
// back, base64 decoding, and exit status.
func TestLiveRunEcho(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stdout, stderr, code, err := client.Run(ctx, StartRequest{
		Process: ProcessConfig{
			Cmd:  "/bin/echo",
			Args: []string{"hello-envd"},
			Cwd:  "/tmp",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(string(stdout), "hello-envd") {
		t.Errorf("stdout = %q, want it to contain hello-envd", stdout)
	}
}

// TestLiveRunExitCode proves a non-zero exit is reported as such, not as success.
func TestLiveRunExitCode(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, _, code, err := client.Run(ctx, StartRequest{
		Process: ProcessConfig{
			Cmd:  "/bin/sh",
			Args: []string{"-c", "exit 7"},
			Cwd:  "/tmp",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

// TestLiveStatAndList proves the unary filesystem family and the string size field.
func TestLiveStatAndList(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	entry, err := client.Stat(ctx, "/tmp")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !entry.IsDir() {
		t.Errorf("IsDir(%s) = false, want true", entry.Path)
	}
	if entry.Permissions == "" {
		t.Error("Permissions is empty, want a rendered mode string")
	}

	entries, err := client.ListDir(ctx, "/tmp", 1)
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	t.Logf("listed %d entries under /tmp", len(entries))
	for _, e := range entries {
		if e.Name == "" {
			t.Errorf("entry with empty name: %+v", e)
		}
	}
}

// TestLiveNotFound proves a missing path is a typed not_found error, so the tool
// layer can turn it into a clean "no such file" result.
func TestLiveNotFound(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := client.Stat(ctx, "/tmp/definitely-not-here-envdclient")
	if err == nil {
		t.Fatal("Stat on a missing path returned no error")
	}
	var connectErr *ConnectError
	if !errors.As(err, &connectErr) || !connectErr.IsNotFound() {
		t.Fatalf("error = %v (%T), want a not_found ConnectError", err, err)
	}
}

// TestLiveBadUserIsUnauthenticated proves the client surfaces envd's own auth
// rejection as a typed error rather than a generic HTTP failure.
func TestLiveBadUserIsUnauthenticated(t *testing.T) {
	addr := os.Getenv("ENVD_TEST_ADDR")
	if addr == "" {
		t.Skip("set ENVD_TEST_ADDR to run live tests")
	}
	// An empty user is rejected locally by Dial, so use a client built directly to
	// exercise the server path. The access token (when the target has one) still
	// travels, otherwise the token check would answer first and hide the user
	// check this test exists for.
	client := &Client{
		base:  mustNormalize(t, addr),
		user:  "",
		token: strings.TrimSpace(os.Getenv("ENVD_TEST_TOKEN")),
		hc:    httpClientFor(t),
		tmo:   20 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := client.Stat(ctx, "/tmp")
	if err == nil {
		t.Fatal("Stat with an empty user returned no error")
	}
	var connectErr *ConnectError
	if !errors.As(err, &connectErr) || !connectErr.IsUnauthenticated() {
		t.Fatalf("error = %v (%T), want an unauthenticated ConnectError", err, err)
	}
}

// mustNormalize normalizes an address or fails the test.
func mustNormalize(t *testing.T, addr string) string {
	t.Helper()
	base, err := normalizeAddr(addr)
	if err != nil {
		t.Fatalf("normalizeAddr(%q): %v", addr, err)
	}
	return base
}

// httpClientFor returns the default transport used by Dial, for tests that build a
// Client directly.
func httpClientFor(t *testing.T) *http.Client {
	t.Helper()
	client, err := Dial(Config{Addr: "127.0.0.1:1", User: "user", TLS: liveTLSConfig(t)})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return client.hc
}
