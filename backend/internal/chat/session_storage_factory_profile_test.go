package chat

import (
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func TestNormalizePersistentSessionStorageConfigUsesBuildProfileDatabaseName(t *testing.T) {
	dir := t.TempDir()
	cfg := normalizePersistentSessionStorageConfig(DefaultPersistentSessionStorageConfig(dir))
	want := filepath.Join(dir, aiclipaths.DefaultSessionHistoryFileName)
	if cfg.Path != want {
		t.Fatalf("default session database path = %q, want %q", cfg.Path, want)
	}
}

func TestNormalizePersistentSessionStorageConfigPreservesExplicitDatabaseName(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = "custom-session-history.sqlite"
	cfg = normalizePersistentSessionStorageConfig(cfg)

	want := filepath.Join(dir, "custom-session-history.sqlite")
	if cfg.Path != want {
		t.Fatalf("explicit session database path = %q, want %q", cfg.Path, want)
	}
}
