package migrate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

func testMigrations() []Migration {
	return []Migration{
		{Version: 1, Name: "v1", UpSQL: `CREATE TABLE demo (id TEXT PRIMARY KEY)`},
		{Version: 2, Name: "v2", UpSQL: `ALTER TABLE demo ADD COLUMN note TEXT`},
	}
}

func TestLatestVersion(t *testing.T) {
	if got := LatestVersion(nil); got != 0 {
		t.Fatalf("LatestVersion(nil) = %d, want 0", got)
	}
	// 顺序无关：取最大值而不是最后一项。
	migrations := []Migration{{Version: 3}, {Version: 1}, {Version: 7}, {Version: 5}}
	if got := LatestVersion(migrations); got != 7 {
		t.Fatalf("LatestVersion = %d, want 7", got)
	}
}

// 04 §5 Phase 5 交付 6 / 风险 R12：库的 schema 版本高于本二进制时必须拒绝
// 打开（降级不安全），而不是把新结构当旧结构读写。
func TestApplyRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migrate.db")
	db, err := sqliteutil.OpenFileCtx(ctx, path, true)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := Apply(ctx, db, testMigrations()); err != nil {
		t.Fatalf("apply v1/v2: %v", err)
	}

	// 模拟"更新的二进制"（已知到 v3）写下的库：多出一条 v3 记录。
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (3, 'v3', '')`); err != nil {
		t.Fatalf("seed future migration: %v", err)
	}

	err = Apply(ctx, db, testMigrations())
	if err == nil {
		t.Fatal("Apply on newer store must fail")
	}
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("err = %v, want ErrSchemaNewer", err)
	}
	// 消息必须可操作：带上库里版本与本二进制版本。
	if !strings.Contains(err.Error(), "v3") || !strings.Contains(err.Error(), "v2") {
		t.Fatalf("err = %q, want both versions (v3 / v2) in the message", err.Error())
	}

	// 已知到 v3 的二进制可以正常打开（同一库，更高版本集）。
	known := append(testMigrations(), Migration{Version: 3, Name: "v3", UpSQL: `ALTER TABLE demo ADD COLUMN extra TEXT`})
	if err := Apply(ctx, db, known); err != nil {
		t.Fatalf("Apply with matching binary version: %v", err)
	}
}

// 版本拒绝不得破坏"幂等重开"这一既有契约：已应用的迁移跳过、不重复执行。
func TestApplyIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := sqliteutil.OpenFileCtx(ctx, filepath.Join(t.TempDir(), "idem.db"), true)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		if err := Apply(ctx, db, testMigrations()); err != nil {
			t.Fatalf("apply #%d: %v", i, err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("schema_migrations rows = %d, want 2", count)
	}
}
