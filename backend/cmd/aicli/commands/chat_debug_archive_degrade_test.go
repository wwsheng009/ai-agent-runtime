package commands

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// P2.13/G5/D3：快照失败默认降级为「归档继续 + manifest 标记 unavailable」，
// 严格模式（--require-snapshot）保留失败语义。
func TestDebugArchiveDegradesWhenSnapshotFails(t *testing.T) {
	sessionDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create sqlite session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	runtimeSession, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}

	// 注入快照失败：把运行时 ID 改成库中不存在的值，SnapshotSession 返回
	// ErrSessionNotFound；会话库文件本身仍然存在，其余归档项不受影响。
	runtimeSession.ID = "missing-session-for-degrade"

	outputPath := filepath.Join(t.TempDir(), "degrade.zip")
	session := &ChatSession{RuntimeSession: runtimeSession, SessionManager: manager, SessionDir: sessionDir}
	result, err := exportChatDebugArchive(session, chatDebugArchiveOptions{OutputPath: outputPath})
	if err != nil {
		t.Fatalf("degrade 模式下归档必须继续产出: %v", err)
	}

	manifest := readDebugArchiveManifest(t, outputPath)
	var unavailable *chatDebugArchiveItem
	for index := range manifest.Skipped {
		if manifest.Skipped[index].Label == "session_file" {
			unavailable = &manifest.Skipped[index]
		}
	}
	if unavailable == nil || strings.TrimSpace(unavailable.UnavailableReason) == "" {
		t.Fatalf("manifest.skipped 必须包含带 unavailable_reason 的 session_file 项: %+v", manifest)
	}
	if len(result.Skipped) == 0 {
		t.Fatalf("result.Skipped 应记录降级项")
	}
	for _, name := range zipEntryNames(t, outputPath) {
		if strings.HasPrefix(name, "session_file/") {
			t.Fatalf("降级时不得打包未经一致性校验的会话库: %s", name)
		}
	}
}

func TestDebugArchiveRequireSnapshotKeepsStrictMode(t *testing.T) {
	sessionDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create sqlite session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	runtimeSession, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}
	runtimeSession.ID = "missing-session-for-strict"

	session := &ChatSession{RuntimeSession: runtimeSession, SessionManager: manager, SessionDir: sessionDir}
	outputPath := filepath.Join(t.TempDir(), "strict.zip")
	if _, err := exportChatDebugArchive(session, chatDebugArchiveOptions{
		OutputPath:      outputPath,
		RequireSnapshot: true,
	}); err == nil {
		t.Fatal("严格模式下快照失败必须中止归档")
	}
}

func readDebugArchiveManifest(t *testing.T, path string) chatDebugArchiveManifest {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open debug archive: %v", err)
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if entry.Name != "manifest.json" {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatalf("open manifest entry: %v", err)
		}
		data, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			t.Fatalf("read manifest entry: %v", readErr)
		}
		var manifest chatDebugArchiveManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		return manifest
	}
	t.Fatalf("manifest.json missing from debug archive")
	return chatDebugArchiveManifest{}
}

func TestSweepStaleChatDebugSnapshotDirs(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, ".aicli-session-snapshot-stale")
	fresh := filepath.Join(root, ".aicli-session-snapshot-fresh")
	unrelated := filepath.Join(root, "unrelated-dir")
	for _, dir := range []string{stale, fresh, unrelated} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, dir := range []string{stale, unrelated} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", dir, err)
		}
	}

	if removed := sweepStaleChatDebugSnapshotDirs(root, 24*time.Hour); removed != 1 {
		t.Fatalf("expected exactly one stale snapshot dir removed, got %d", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale snapshot dir must be removed, stat err=%v", err)
	}
	for _, dir := range []string{fresh, unrelated} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("dir must be preserved: %s (%v)", dir, err)
		}
	}
}
