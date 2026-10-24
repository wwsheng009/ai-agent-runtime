package remotetools_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// newArtifactConfig builds an ops config whose shell output folds into an
// artifact. modelVisibleCap bounds the shell body (so archiving happens) and
// also caps the artifact window, which is the interaction under test.
func newArtifactConfig(t *testing.T, root string, modelVisibleCap int) *remotetools.Config {
	t.Helper()
	cfg := newOpsConfig(t, root, []string{"seq"})
	cfg.Artifacts.Root = filepath.Join(t.TempDir(), "artifacts")
	cfg.Artifacts.MaxBytes = 8 << 20
	cfg.Limits.ModelVisibleCap = modelVisibleCap
	cfg.Limits.MaxBytesPerCall = 4 << 20
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return cfg
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
		"command": "seq 1 20000",
	})
	if !strings.Contains(body, "artifact_read") {
		t.Skipf("no artifact pointer produced; shell body:\n%s", body)
	}
	id := extractArtifactID(t, body)

	// A page that fits asks for exactly what it gets.
	small := callTool(t, ctx, session, "remote_dev__artifact_read", map[string]interface{}{
		"artifact_id": id, "offset": 0, "limit": 4096,
	})
	if len(small) != 4096 {
		t.Fatalf("requested 4096 bytes, got %d - the page was reshaped", len(small))
	}
	if strings.Contains(small, "bytes omitted") {
		t.Fatalf("an artifact page must never be folded:\n%s", small)
	}
}

// TestRemoteArtifactReadHonoursItsOwnWindow pins the upper bound: a request for
// more than the tool's window returns the window, not a folded head+tail slice
// of it. The response size must equal the requested size whenever the request
// fits, which is what makes next_offset arithmetic trustworthy.
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
		"command": "seq 1 20000",
	})
	if !strings.Contains(body, "artifact_read") {
		t.Skipf("no artifact pointer produced; shell body:\n%s", body)
	}
	id := extractArtifactID(t, body)

	huge := callTool(t, ctx, session, "remote_dev__artifact_read", map[string]interface{}{
		"artifact_id": id, "offset": 0, "limit": 32 * 1024,
	})
	if len(huge) != 8*1024-512 {
		t.Fatalf("expected a %d-byte clamped page, got %d", 8*1024-512, len(huge))
	}
	if strings.Contains(huge, "bytes omitted") {
		t.Fatalf("a clamped page must not be folded:\n%s", huge)
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

	const captureBytes = 108894 // "seq 1 20000" | wc -c
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "seq 1 20000",
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
		reassembled.WriteString(chunk)
		offset += len(chunk)
		if offset >= captureBytes {
			break
		}
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
	cfg := newOpsConfig(t, root, []string{"seq"})
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
		"command": "seq 1 20000",
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
		"command": "seq 1 20000",
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
