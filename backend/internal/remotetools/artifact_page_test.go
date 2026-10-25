package remotetools_test

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// bigCaptureCommand produces a >100 KiB capture with one command and no shell
// metacharacters. POSIX targets have `seq`; Windows has no seq on PATH, so the
// equivalent cmd.exe for-loop is used - the pattern every other Windows-aware
// fixture in this package follows.
func bigCaptureCommand() string {
	if runtime.GOOS == "windows" {
		return `for /l %i in (1,1,20000) do @echo %i`
	}
	return "seq 1 20000"
}

// bigCaptureAllowlist returns the allowlist entry bigCaptureCommand needs.
func bigCaptureAllowlist() []string {
	if runtime.GOOS == "windows" {
		return []string{"for"}
	}
	return []string{"seq"}
}

// bigCaptureBytes is the size of bigCaptureCommand's output on this platform.
// POSIX: 20000 lines of digits plus LF. Windows: the same digits plus CRLF.
func bigCaptureBytes() int {
	if runtime.GOOS == "windows" {
		return 128894
	}
	return 108894
}

// newArtifactConfig builds an ops config whose shell output folds into an
// artifact. modelVisibleCap bounds the shell body (so archiving happens) and
// also caps the artifact window, which is the interaction under test.
func newArtifactConfig(t *testing.T, root string, modelVisibleCap int) *remotetools.Config {
	t.Helper()
	cfg := newOpsConfig(t, root, bigCaptureAllowlist())
	cfg.Artifacts.Root = filepath.Join(t.TempDir(), "artifacts")
	cfg.Artifacts.MaxBytes = 8 << 20
	cfg.Limits.ModelVisibleCap = modelVisibleCap
	cfg.Limits.MaxBytesPerCall = 4 << 20
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return cfg
}

// artifactPage splits a rendered page into its header and its window. Every page
// carries "artifact … | window=[a,b) | eof=… | next_offset=N", a blank line,
// then the bytes: the header is what a model pages with, since a successful
// result reaches the model as its body and a model cannot count bytes.
func artifactPage(t *testing.T, body string) (header, window string) {
	t.Helper()
	idx := strings.Index(body, "\n\n")
	if idx < 0 {
		t.Fatalf("page has no header separator:\n%s", body)
	}
	return body[:idx], body[idx+2:]
}

// artifactNextOffset reads the continuation offset the page states, and reports
// whether another page exists.
func artifactNextOffset(t *testing.T, header string) (int, bool) {
	t.Helper()
	if strings.Contains(header, "eof=true") {
		return 0, false
	}
	const marker = "next_offset="
	idx := strings.Index(header, marker)
	if idx < 0 {
		t.Fatalf("a non-EOF page must state next_offset:\n%s", header)
	}
	rest := header[idx+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	value, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("unparsable next_offset %q:\n%s", rest[:end], header)
	}
	return value, true
}

// TestRemoteArtifactReadPageIsNotFolded is the regression guard for a real
// defect: artifact_read exists to page an archived capture, but its result used
// to be folded a second time by the output shaper.
//
// That broke three things at once:
//   - a request for N bytes returned fewer than N;
//   - the fold dropped the middle of the page - the bytes the caller asked for;
//   - suggested_next_offset described a window that no longer matched the body.
//
// The probe that found this saw limit=4096/32768/61440/65536 all return exactly
// 1024 bytes, with the shaper's "bytes omitted" marker in every one.
func TestRemoteArtifactReadPageIsNotFolded(t *testing.T) {
	root := t.TempDir()
	cfg := newArtifactConfig(t, root, 8*1024)
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": bigCaptureCommand(),
	})
	if !strings.Contains(body, "artifact_read") {
		t.Skipf("no artifact pointer produced; shell body:\n%s", body)
	}
	id := extractArtifactID(t, body)

	// A page that fits asks for exactly what it gets. The header is framing, not
	// data: the window behind it holds exactly the requested bytes.
	small := callTool(t, ctx, session, "remote_dev__artifact_read", map[string]interface{}{
		"artifact_id": id, "offset": 0, "limit": 4096,
	})
	header, window := artifactPage(t, small)
	if len(window) != 4096 {
		t.Fatalf("requested 4096 bytes, got a %d-byte window - the page was reshaped", len(window))
	}
	if !strings.Contains(header, "window=[0,4096)") {
		t.Fatalf("the header must state the window it delivered, got:\n%s", header)
	}
	if _, ok := artifactNextOffset(t, header); !ok {
		t.Fatal("a 4096-byte page of a larger capture must state where to continue")
	}
	if strings.Contains(window, "bytes omitted") {
		t.Fatalf("an artifact page must never be folded:\n%s", window)
	}
}

// TestRemoteArtifactReadHonoursItsOwnWindow pins the upper bound: a request for
// more than the tool's window returns the window, not a folded head+tail slice
// of it. The delivered window must equal the clamped size whenever the request
// fits, and the header must state it - that is what makes the next_offset a
// pager follows trustworthy.
func TestRemoteArtifactReadHonoursItsOwnWindow(t *testing.T) {
	root := t.TempDir()
	// 8 KiB model window -> artifact window is also 8 KiB, so a 32 KiB request
	// is clamped rather than folded.
	cfg := newArtifactConfig(t, root, 8*1024)
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": bigCaptureCommand(),
	})
	if !strings.Contains(body, "artifact_read") {
		t.Skipf("no artifact pointer produced; shell body:\n%s", body)
	}
	id := extractArtifactID(t, body)

	huge := callTool(t, ctx, session, "remote_dev__artifact_read", map[string]interface{}{
		"artifact_id": id, "offset": 0, "limit": 32 * 1024,
	})
	header, window := artifactPage(t, huge)
	if len(window) != 8*1024-512 {
		t.Fatalf("expected a %d-byte clamped window, got %d", 8*1024-512, len(window))
	}
	if !strings.Contains(header, fmt.Sprintf("window=[0,%d)", 8*1024-512)) {
		t.Fatalf("the header must state the clamped window, got:\n%s", header)
	}
	if strings.Contains(window, "bytes omitted") {
		t.Fatalf("a clamped page must not be folded:\n%s", window)
	}
}

// TestRemoteArtifactReadPagesCoverEveryByte walks the whole capture through
// successive offsets and checks that the bytes reassemble exactly. This is the
// property that matters most: a paging tool whose pages overlap or skip data
// silently corrupts what the model reads.
func TestRemoteArtifactReadPagesCoverEveryByte(t *testing.T) {
	root := t.TempDir()
	cfg := newArtifactConfig(t, root, 8*1024)
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	captureBytes := bigCaptureBytes()
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": bigCaptureCommand(),
	})
	if !strings.Contains(body, "artifact_read") {
		t.Skipf("no artifact pointer produced; shell body:\n%s", body)
	}
	id := extractArtifactID(t, body)

	var reassembled strings.Builder
	offset := 0
	for page := 0; page < 200; page++ {
		chunk := callTool(t, ctx, session, "remote_dev__artifact_read", map[string]interface{}{
			"artifact_id": id, "offset": offset, "limit": 4096,
		})
		if chunk == "" {
			break
		}
		header, window := artifactPage(t, chunk)
		reassembled.WriteString(window)
		next, more := artifactNextOffset(t, header)
		if !more {
			break
		}
		// The page states where to continue, so the walk never has to infer an
		// offset from a length it would have to measure itself.
		offset = next
	}
	if got := reassembled.Len(); got != captureBytes {
		t.Fatalf("paged %d bytes, capture holds %d - pages skipped or duplicated data", got, captureBytes)
	}
}

// TestRemoteShellWithoutStoreDoesNotPromiseArtifact pins the failure mode this
// probe uncovered: with no artifact store the shell output still folds (the
// model window is a hard limit), but nothing is archived and artifact_read is
// not even registered.
//
// The notice used to keep saying "The complete capture is archived: read the
// middle with artifact_read ... offset=N, limit=N". That is worse than losing
// the bytes: it instructs the model to call a tool the server does not expose,
// with offsets into data that was never written.
func TestRemoteShellWithoutStoreDoesNotPromiseArtifact(t *testing.T) {
	root := t.TempDir()
	cfg := newOpsConfig(t, root, bigCaptureAllowlist())
	cfg.Limits.ModelVisibleCap = 1024 // force a fold
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	// Precondition: the paging tool really is absent on this surface.
	for _, name := range server.ToolNames() {
		if strings.HasSuffix(name, "__artifact_read") {
			t.Fatalf("this scenario needs no artifact store, but %s is registered", name)
		}
	}

	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": bigCaptureCommand(),
	})
	if !strings.Contains(body, "output window") {
		t.Skipf("capture was not folded; body:\n%s", body)
	}
	if strings.Contains(body, "The complete capture is archived") {
		t.Fatalf("the notice must not claim an archive that does not exist:\n%s", body)
	}
	if strings.Contains(body, "read the middle with artifact_read") {
		t.Fatalf("the notice must not point at a tool this surface does not expose:\n%s", body)
	}
	if !strings.Contains(body, "NOT kept") {
		t.Fatalf("the notice must state that the omitted bytes are gone:\n%s", body)
	}
}

// TestRemoteShellFoldedOutputPointsAtArtifact is the counterpart that makes the
// paging test meaningful: folding is only acceptable because the omitted middle
// stays reachable. If this ever stops holding, the fold is silent data loss.
func TestRemoteShellFoldedOutputPointsAtArtifact(t *testing.T) {
	root := t.TempDir()
	cfg := newArtifactConfig(t, root, 2*1024)
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": bigCaptureCommand(),
	})
	if !strings.Contains(body, "output window") {
		t.Skipf("capture was not folded; shell body:\n%s", body)
	}
	if !strings.Contains(body, "artifact_read") {
		t.Fatalf("a folded capture must expose its archived middle:\n%s", body)
	}
	if !strings.Contains(body, "art_") {
		t.Fatalf("the pointer must carry an artifact id:\n%s", body)
	}
}
