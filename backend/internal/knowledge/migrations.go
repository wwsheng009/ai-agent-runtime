package knowledge

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
)

// migrationFS 持有 knowledge.db 的 DDL。schema 的唯一落盘位置是
// migrations/*.sql；Go 代码不再内联 DDL（04 §4.3 为事实源）。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrations 返回按版本号升序排列的 knowledge.db 迁移。
//
// 文件名约定 `NNNN_name.sql`（NNNN 为十进制版本号），与 internal/migrate
// 的 schema_migrations 记录方式一致；migrate.Apply 负责幂等（已应用版本跳过）。
func Migrations() ([]migrate.Migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("knowledge: read migrations: %w", err)
	}
	out := make([]migrate.Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("knowledge: read migration %s: %w", entry.Name(), err)
		}
		out = append(out, migrate.Migration{
			Version: version,
			Name:    name,
			UpSQL:   string(body),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// parseMigrationName 解析 `NNNN_name.sql`，版本号必须为正整数。
func parseMigrationName(fileName string) (int, string, error) {
	base := strings.TrimSuffix(fileName, ".sql")
	parts := strings.SplitN(base, "_", 2)
	version, err := strconv.Atoi(parts[0])
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("knowledge: migration %q must start with a positive version number", fileName)
	}
	name := base
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		name = parts[1]
	}
	return version, name, nil
}
