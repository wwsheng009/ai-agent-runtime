package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestWriteFileAtomicPreservesExistingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mode.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := writeFileAtomic(path, []byte("new"), writeFileModeDefault); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "new" {
		t.Fatalf("unexpected content %q err %v", raw, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Windows only emulates the read-only bit; exact POSIX modes are not
	// enforceable there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600 preserved, got %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("leftover temp file after atomic write: %s", entry.Name())
		}
	}
}

func TestWriteToolPreservesUTF8BOM(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bom.txt")
	seed := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello\n")...)
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	tool := NewWriteTool()
	tool.SetBasePath(root)
	ctx := toolctx.WithSessionID(context.Background(), "test-bom-"+t.Name())
	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path": "bom.txt",
		"content":   "hello world\n",
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("write failed: %v %+v", err, result)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("expected UTF-8 BOM preserved, got % x", raw)
	}
	if string(raw[3:]) != "hello world\n" {
		t.Fatalf("unexpected decoded content %q", string(raw[3:]))
	}
	if result.Metadata["encoding"] != fileEncodingUTF8BOM.String() {
		t.Fatalf("expected encoding metadata, got %#v", result.Metadata["encoding"])
	}
}

func TestEditToolPreservesUTF16LE(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "utf16.txt")
	encoded := encodeFileText("alpha\nbeta\n", fileEncodingUTF16LE)
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	ctx := toolctx.WithSessionID(context.Background(), "test-utf16-"+t.Name())
	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path":  "utf16.txt",
		"old_string": "beta",
		"new_string": "gamma",
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("edit failed: %v %+v", err, result)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	text, enc := decodeFileBytes(raw)
	if enc != fileEncodingUTF16LE {
		t.Fatalf("expected utf-16le preserved, got %v", enc)
	}
	if !strings.Contains(text, "gamma") || !strings.Contains(text, "alpha") {
		t.Fatalf("unexpected decoded content %q", text)
	}
}

func TestViewRecordsLedgerAndFreshWriteCarriesMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	ctx := toolctx.WithSessionID(context.Background(), "test-ledger-fresh-"+t.Name())

	view := NewViewTool()
	view.SetBasePath(root)
	if result, err := view.Execute(ctx, map[string]interface{}{"file_path": "note.txt"}); err != nil || result == nil || !result.Success {
		t.Fatalf("view failed: %v %+v", err, result)
	}

	write := NewWriteTool()
	write.SetBasePath(root)
	result, err := write.Execute(ctx, map[string]interface{}{"file_path": "note.txt", "content": "v2\n"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("write failed: %v %+v", err, result)
	}
	if result.Metadata["read_before_write"] != "fresh" {
		t.Fatalf("expected fresh read_before_write, got %#v", result.Metadata["read_before_write"])
	}
}

func TestWriteRefusesStaleOverwriteAfterExternalChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	ctx := toolctx.WithSessionID(context.Background(), "test-ledger-stale-"+t.Name())

	view := NewViewTool()
	view.SetBasePath(root)
	if result, err := view.Execute(ctx, map[string]interface{}{"file_path": "note.txt"}); err != nil || result == nil || !result.Success {
		t.Fatalf("view failed: %v %+v", err, result)
	}

	// Another actor changes the file after the session read it.
	if err := os.WriteFile(path, []byte("v2 external\n"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}

	write := NewWriteTool()
	write.SetBasePath(root)
	result, err := write.Execute(ctx, map[string]interface{}{"file_path": "note.txt", "content": "v3\n"})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected stale overwrite refusal, got %+v", result)
	}
	if result.Metadata["stale_write"] != true {
		t.Fatalf("expected stale_write metadata, got %#v", result.Metadata)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "v2 external\n" {
		t.Fatalf("file must stay untouched, got %q", raw)
	}

	// Explicit confirmation with the current hash is the escape hatch.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	result, err = write.Execute(ctx, map[string]interface{}{
		"file_path":       "note.txt",
		"content":         "v3\n",
		"expected_sha256": fileBytesSHA256(current),
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("explicit confirmation should succeed: %v %+v", err, result)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "v3\n" {
		t.Fatalf("unexpected final content %q", raw)
	}
}

func TestWriteRefusesBinaryOverwrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0x00, 0x00, 0x00, 0x03, 0x00}, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	write := NewWriteTool()
	write.SetBasePath(root)
	ctx := toolctx.WithSessionID(context.Background(), "test-binary-"+t.Name())
	result, err := write.Execute(ctx, map[string]interface{}{"file_path": "blob.bin", "content": "text\n"})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected binary refusal, got %+v", result)
	}
	if raw, _ := os.ReadFile(path); len(raw) != 8 {
		t.Fatalf("binary file must stay untouched, got %d bytes", len(raw))
	}
}
