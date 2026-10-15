package schedule

import (
	"context"
	"path/filepath"
	"testing"
)

// TestStoreSchemaVersionRoundTrip 钉住 S3 版本握手的两个事实：
// ① SupportedSchemaVersion 不低于 v6（交付账本引入档）；② 新库迁移后
// SchemaVersion 读到与支持档一致的版本——能力位标记与选主自检都依赖它。
func TestStoreSchemaVersionRoundTrip(t *testing.T) {
	if SupportedSchemaVersion() < 6 {
		t.Fatalf("supported schema = %d, want >= 6 (delivery ledger)", SupportedSchemaVersion())
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "schedule.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	version, err := store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != SupportedSchemaVersion() {
		t.Fatalf("store schema = %d, supported = %d", version, SupportedSchemaVersion())
	}
}
