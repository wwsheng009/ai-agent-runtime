package knowledge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// 本文件是 04 §5 Phase 5 与风险 R12 的第三段：库文件损坏时的自愈路径。
//
// 口径：knowledge.db 是**派生数据**（随时可由工作区重建），所以损坏时最合理的
// 处置不是让用户卡在"打不开"，而是：
//
//  1. 把损坏文件（含 -wal/-shm 副文件）**改名留证**——绝不删除，留待排查；
//  2. 建一个空库，让 owner 照常重新索引。重建期索引为空 → 判定与注入按既有
//     degraded 语义降级：状态面给出 `store_recovered_from` 与降级原因，绝不把
//     "空索引"当成"没有知识"以外的含义。
//
// 只有 owner 能走这条路（reader 没有写权限，只报错、把处置权交还 owner）。
// 版本不匹配（ErrSchemaNewer / 旧库）**不属于**损坏：新库被旧代码打开时改名
// 重建会毁掉新数据，必须继续走"拒绝 + 提示"。

// corruptStoreSuffix 是留证文件的后缀（追加时间戳，绝不覆盖历史留证）。
const corruptStoreSuffix = ".corrupt-"

// isCorruptStoreError 判断错误是否属于"文件损坏 / 不是数据库"这一类。
//
// 匹配顺序：驱动错误码（CORRUPT / NOTADB，含扩展码）优先，消息模式兜底——
// 错误可能在 database/sql 与 VFS 层被重新包装，码未必还在。
func isCorruptStoreError(err error) bool {
	if err == nil {
		return false
	}
	// 版本不匹配是"拒绝"而不是"损坏"：绝不能走重建（否则毁掉更新的库）。
	if errors.Is(err, migrate.ErrSchemaNewer) || strings.Contains(err.Error(), "older than this binary") {
		return false
	}
	var serr *sqlite3.Error
	if errors.As(err, &serr) {
		// 取低 8 位：扩展码（如 SQLITE_CORRUPT_VTAB）的主码仍是 CORRUPT。
		code := int(serr.Code()) & 0xff
		if code == int(sqlite3.CORRUPT) || code == int(sqlite3.NOTADB) {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"database disk image is malformed",
		"file is not a database",
		"not a database",
		"database corrupt",
		"malformed database",
	} {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

// quarantineCorruptStore 把损坏的库文件改名留证，返回留证路径。
//
// 副文件（-wal/-shm）跟着改名：留下孤儿的 -wal 会在下一次打开时污染重建出的
// 新库。副文件缺失或改名失败不阻断主流程（主文件已留证，重建仍可进行）。
func quarantineCorruptStore(path string, now time.Time) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("knowledge: corrupt store path is empty")
	}
	target := path + corruptStoreSuffix + now.UTC().Format("20060102T150405")
	if _, err := os.Stat(target); err == nil {
		// 同一秒内二次留证：加纳秒后缀，绝不覆盖既有证据。
		target = fmt.Sprintf("%s%s%d", path, corruptStoreSuffix, now.UTC().UnixNano())
	}
	if err := os.Rename(path, target); err != nil {
		return "", fmt.Errorf("knowledge: quarantine corrupt store: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			_ = os.Rename(path+suffix, target+suffix)
		}
	}
	return target, nil
}

// openStoreWithRecovery 在 owner 角色下对"库损坏"做一次自愈：留证 + 重建 + 重开。
//
// 非损坏错误（版本不匹配 / 权限 / 锁 / 目录不可写）原样返回——这条路径只处理
// "文件坏了"，不替调用方吞掉其它故障。
func openStoreWithRecovery(ctx context.Context, cfg Config, own *ownership, cause error) (Store, string, error) {
	if own.readOnly() || !isCorruptStoreError(cause) {
		return nil, "", cause
	}
	path := cfg.storePath()
	if sqliteutil.IsMemoryDSN(path) {
		return nil, "", cause
	}
	quarantined, err := quarantineCorruptStore(path, time.Now())
	if err != nil {
		return nil, "", fmt.Errorf("%w (quarantine failed: %v)", cause, err)
	}
	store, err := OpenStore(ctx, path, false)
	if err != nil {
		return nil, quarantined, fmt.Errorf("knowledge: rebuild after corrupt store: %w", err)
	}
	return store, quarantined, nil
}
