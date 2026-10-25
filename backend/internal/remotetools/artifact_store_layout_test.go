package remotetools_test

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestArtifactStoreLayoutIsOneFilePerPayload pins the storage medium: a plain
// directory, one `.log` file per payload, no index and no database.
//
// This matters operationally - backup, permissions review and disk accounting all
// assume it - so the shape is asserted rather than left to the doc comment.
func TestArtifactStoreLayoutIsOneFilePerPayload(t *testing.T) {
	dir := t.TempDir()
	store, err := remotetools.NewArtifactStore(dir, 1<<20)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}

	id, err := store.Put([]byte("hello world"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(id, "art_") || len(id) != len("art_")+32 {
		t.Fatalf("id shape = %q, want art_<32 hex>", id)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file, got %d: %v", len(entries), entries)
	}
	name := entries[0].Name()
	if name != id+".log" {
		t.Fatalf("file name = %q, want %q", name, id+".log")
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Size() != int64(len("hello world")) {
		t.Fatalf("stored size = %d, want %d", info.Size(), len("hello world"))
	}
	// The capture may contain secrets from a shell command; it must not be
	// world-readable. POSIX pins the exact 0600 the store requests. Windows
	// has no mode bits - ACLs govern access and Go reports 0666 for every
	// writable file and 0444 for read-only ones. The store's chmod(0600)
	// maps to "clear the read-only attribute" there, so the only property
	// Go can observe is that the file stayed in the writable 0666 form
	// rather than being flipped to read-only.
	perm := info.Mode().Perm()
	if runtime.GOOS == "windows" {
		if perm != 0o666 {
			t.Fatalf("artifact file mode = %o, want 666 (Go's canonical writable form on windows)", perm)
		}
	} else if perm != 0o600 {
		t.Fatalf("artifact file mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o700 {
		t.Fatalf("artifact root mode = %o, want 700", perm)
	}
}

// TestArtifactStoreIsContentAddressed pins dedupe: the id is derived from the
// payload, so re-archiving the same capture neither grows the store nor rewrites
// the file.
func TestArtifactStoreIsContentAddressed(t *testing.T) {
	dir := t.TempDir()
	store, err := remotetools.NewArtifactStore(dir, 1<<20)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	payload := []byte("identical capture")

	first, err := store.Put(payload)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second, err := store.Put(payload)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if first != second {
		t.Fatalf("same payload produced two ids: %s vs %s", first, second)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("dedupe failed: %d files for one payload", len(entries))
	}
}

// TestArtifactStoreMaxBytesIsPerPayloadNotATotalQuota is the clarification that
// matters most operationally: maxBytes bounds one capture, it does not cap the
// directory.
//
// Verified behaviour: with maxBytes=1 KiB, six sub-limit payloads coexist and the
// directory holds well over 1 KiB. Anyone sizing disk from this setting alone
// will under-provision, so the semantics are pinned here.
func TestArtifactStoreMaxBytesIsPerPayloadNotATotalQuota(t *testing.T) {
	dir := t.TempDir()
	const perArtifact = 1024
	store, err := remotetools.NewArtifactStore(dir, perArtifact)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}

	// A payload over the limit is rejected.
	if _, err := store.Put(make([]byte, perArtifact+1)); err == nil {
		t.Fatal("a payload larger than maxBytes must be rejected")
	}

	// Many sub-limit payloads all succeed and coexist.
	for i := 0; i < 5; i++ {
		payload := make([]byte, 900)
		for j := range payload {
			payload[j] = byte('a' + i)
		}
		if _, err := store.Put(payload); err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var total int64
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		total += info.Size()
	}
	if total <= perArtifact {
		t.Fatalf("expected the directory to exceed the per-artifact cap (%d), got %d bytes in %d files - "+
			"if this ever changes to a real quota, update the docs and the sizing guidance",
			perArtifact, total, len(entries))
	}
}

// TestArtifactStoreReadWindowIsByteAddressable pins the paging primitive the
// artifact_read tool is built on: offset/limit pick an exact byte window, and eof
// is true only when the window reaches the end.
func TestArtifactStoreReadWindowIsByteAddressable(t *testing.T) {
	dir := t.TempDir()
	store, err := remotetools.NewArtifactStore(dir, 1<<20)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	id, err := store.Put([]byte("hello world"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	head, size, eof, err := store.Read(id, 0, 4)
	if err != nil {
		t.Fatalf("Read head: %v", err)
	}
	if head != "hell" || size != 11 || eof {
		t.Fatalf("head window = %q size=%d eof=%v, want \"hell\" 11 false", head, size, eof)
	}

	tail, _, eof, err := store.Read(id, 6, 100)
	if err != nil {
		t.Fatalf("Read tail: %v", err)
	}
	if tail != "world" || !eof {
		t.Fatalf("tail window = %q eof=%v, want \"world\" true", tail, eof)
	}

	// Past the end: empty page, eof, no error.
	past, _, pastEOF, err := store.Read(id, 999, 10)
	if err != nil {
		t.Fatalf("Read past end: %v", err)
	}
	if past != "" || !pastEOF {
		t.Fatalf("past-end window = %q eof=%v, want \"\" true", past, pastEOF)
	}
}
