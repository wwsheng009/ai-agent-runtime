package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// ============================================================================
// 打开前健康探测（可选，默认关闭）。
//
// 2026-09-26 的 WAL 损坏事故复盘（P0）要求在运行时打开库前做只读健康探测，
// 并在损坏时 fail-closed 或给出可见降级。这里提供 opt-in 的严格模式：
//
//	AICLI_SQLITE_HEALTH_PROBE=quick
//
// 开启后，runtime store 在第一次打开、执行迁移之前运行只读 PRAGMA quick_check：
//   - 返回 ok          → 正常继续；
//   - 返回错误明细/执行失败 → 打开直接失败（fail-closed），进程以可见错误退出，
//     不再在损坏库上写入；
//   - 未设置或其它值    → 完全跳过（零开销，默认）。
//
// 默认关闭的原因：quick_check 需要全量扫描数据库文件（807MB 库实测约 3s，
// 数 GB 库可达分钟级），不适合每次 CLI 启动都做；建议在 runtime-server、
// 维护脚本或排障会话中显式开启。只对文件型库探测，内存库跳过。
// ============================================================================

// RuntimeStoreHealthProbeEnv 控制打开前 quick_check 健康探测；默认关闭，
// 只有显式设置为 "quick" 才开启（未设置/其它值=零开销跳过）。
const RuntimeStoreHealthProbeEnv = sqliteutil.HealthProbeEnv

// runtimeStoreHealthProbeTimeout 是单次探测的总预算。
const runtimeStoreHealthProbeTimeout = 60 * time.Second

func runtimeStoreHealthProbeEnabled() bool {
	return sqliteutil.HealthProbeEnabled()
}

// probeRuntimeStoreHealth 由 init 在迁移之前调用；未开启或内存库时零开销。
func (s *SQLiteRuntimeStore) probeRuntimeStoreHealth(ctx context.Context) error {
	if s == nil || s.db == nil || !s.fileBacked || !runtimeStoreHealthProbeEnabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, runtimeStoreHealthProbeTimeout)
	defer cancel()
	result, err := sqliteutil.QuickCheck(probeCtx, s.db)
	if err != nil {
		return fmt.Errorf(
			"runtime store health probe: quick_check 未能完成（库可能损坏；设置 %s 可跳过探测）：%w",
			RuntimeStoreHealthProbeEnv, err)
	}
	if strings.EqualFold(strings.TrimSpace(result), "ok") {
		return nil
	}
	return fmt.Errorf(
		"runtime store health probe: quick_check 未通过（库已损坏；修复前禁止写入，设置 %s 可跳过探测）：%s",
		RuntimeStoreHealthProbeEnv, clampRuntimeHealthDetail(result))
}

// clampRuntimeHealthDetail 截断 quick_check 明细：损坏库可能逐行输出上千条。
func clampRuntimeHealthDetail(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "unknown"
	}
	const (
		maxLines = 3
		maxRunes = 400
	)
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("…（共 %d 行，已截断）", len(lines)))
	}
	joined := strings.Join(lines, "\n")
	if runes := []rune(joined); len(runes) > maxRunes {
		joined = string(runes[:maxRunes]) + "…（已截断）"
	}
	return joined
}
