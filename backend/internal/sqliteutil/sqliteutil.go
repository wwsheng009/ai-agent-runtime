// Package sqliteutil 提供共享文件 SQLite 的统一多进程并发基线。
//
// 背景：aicli local 模式与 runtime-server 可能同时打开同一批 SQLite 文件
// （多实例场景）。SQLite 是单写者数据库，多进程同库必然产生写锁竞争；
// 各 store 过去各自为政（有的无 WAL、无 busy_timeout、无连接池限制），
// 并发打开时表现为启动长时间无响应。本包把统一基线收敛到一处：
//
//   - journal_mode=WAL（多读单写，写入不阻塞读）
//   - busy_timeout（默认 5000ms，写锁冲突时在驱动内等待而非立即失败）
//   - MaxOpenConns(1)/MaxIdleConns(1)（单写者连接，避免同库多连接自锁）
//   - RetryLocked：仍遇到 "database is locked" 时的退避重试
//     （另一进程可能持有长写锁：迁移、大会话 flush、wal checkpoint）。
package sqliteutil

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DefaultBusyTimeoutMS 是统一写入等待上限（毫秒）。
const DefaultBusyTimeoutMS = 5000

// LockRetries 与退避参数：与 internal/chat 既有的锁重试语义一致。
const (
	LockRetries      = 10
	lockRetryBaseWait = 50 * time.Millisecond
	lockRetryMaxWait  = 500 * time.Millisecond
)

// IsLockedError 报告 err 是否为 SQLite 瞬时锁冲突
//（另一个连接/进程正持有数据库写锁）。
func IsLockedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", err)))
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}

// RetryLockedCtx 以退避重试方式执行 fn，受 ctx 约束；仅当 fn 返回 SQLite 锁冲突错误时
// 重试，其余错误（含重试耗尽）原样返回。ctx 取消时立即返回 ctx.Err()，避免调用方
// 在短生命周期操作（如健康检查）中因锁竞争而挂起。
func RetryLockedCtx(ctx context.Context, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 0 {
			wait := lockRetryWait(attempt - 1)
			onLockRetry(attempt, wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !IsLockedError(lastErr) || attempt >= LockRetries {
			return lastErr
		}
	}
}

// RetryLocked 以退避重试方式执行 fn；仅当 fn 返回 SQLite 锁冲突错误时
// 重试，其余错误（含重试耗尽）原样返回。fn 必须可在失败后安全重跑
// （典型用法：打开数据库并执行基线/迁移 PRAGMA）。
func RetryLocked(fn func() error) error {
	return RetryLockedCtx(context.Background(), fn)
}

// onLockRetry 是 RetryLocked 重试前的钩子（打日志），可被测试替换。
var onLockRetry = func(attempt int, wait time.Duration) {}

// SetOnLockRetry 注册锁重试回调（日志/上报）。
func SetOnLockRetry(fn func(attempt int, wait time.Duration)) {
	if fn != nil {
		onLockRetry = fn
	}
}

func lockRetryWait(retryIndex int) time.Duration {
	wait := lockRetryBaseWait * time.Duration(retryIndex+1)
	if wait > lockRetryMaxWait {
		wait = lockRetryMaxWait
	}
	return wait
}

// OpenFileCtx 打开文件型 SQLite 并施加统一并发基线，受 ctx 约束：
//
//   - PRAGMA journal_mode=WAL
//   - PRAGMA busy_timeout=<DefaultBusyTimeoutMS>
//   - 连接池限为单连接（单写者）
//
// 返回的 *sql.DB 已可直接使用；调用方仍需执行各自 schema/迁移 PRAGMA。
// failOnLock 为 true 时以 RetryLockedCtx 语义重试打开 & 基线 PRAGMA
//（另一进程长写锁场景），为 false 时单次尝试（内存库/测试路径）。
// ctx 取消时立即返回，避免健康检查等短生命周期调用被锁竞争拖住。
func OpenFileCtx(ctx context.Context, dsn string, failOnLock bool) (*sql.DB, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	apply := func() error {
		if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout="+intToStr(DefaultBusyTimeoutMS)); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
			return err
		}
		return nil
	}
	if failOnLock {
		if err := RetryLockedCtx(ctx, apply); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite apply baseline pragmas: %w", err)
		}
	} else if err := apply(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite apply baseline pragmas: %w", err)
	}
	return db, nil
}

// OpenFile 打开文件型 SQLite 并施加统一并发基线：
//
//   - PRAGMA journal_mode=WAL
//   - PRAGMA busy_timeout=<DefaultBusyTimeoutMS>
//   - 连接池限为单连接（单写者）
//
// 返回的 *sql.DB 已可直接使用；调用方仍需执行各自 schema/迁移 PRAGMA。
// failOnLock 为 true 时以 RetryLocked 语义重试打开 & 基线 PRAGMA
//（另一进程长写锁场景），为 false 时单次尝试（内存库/测试路径）。
func OpenFile(dsn string, failOnLock bool) (*sql.DB, error) {
	return OpenFileCtx(context.Background(), dsn, failOnLock)
}

// IsMemoryDSN 报告 dsn 是否为内存库（:memory: / mode=memory）。
func IsMemoryDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	return lower == ":memory:" || strings.Contains(lower, "mode=memory")
}

func intToStr(v int) string {
	if v == 0 {
		return "0"
	}
	// 避免 strconv 依赖的轻量整数转换（本包保持零外部依赖）。
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}