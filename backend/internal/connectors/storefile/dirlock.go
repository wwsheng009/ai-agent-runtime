// 本文件实现 storefile 的跨进程数据目录锁（docs/bots/07-config-ux.md §5.6
// 「写入并发」、docs/bots/03-host-integration.md §7）：
//
//   - 锁路径固定为 <stateDir>/connectors.lock（目录）；aicli 与 runtime-server
//     都由同一份 config.yaml 推导 statePath，因此两个宿主天然落到同一把锁上；
//   - 获取用 os.Mkdir 的原子性（等价 O_CREATE|O_EXCL）：失败即有他人持有；
//     锁目录内写 owner 文件（token/pid/时间）用于诊断与陈旧判断，目录 mtime 兜底；
//   - 持有超过 LockOptions.StaleAfter 未释放（进程崩溃/挂起）时，等待者可原子
//     rename 窃取并重试获取；Mkdir 与 rename 的原子性保证同一时刻只有一个持有者；
//   - 获取失败按 RetryInterval 重试，超过 AcquireTimeout 返回错误（调用方写失败）。
//
// 边界（刻意不做分布式强保证）：
//   - 只约束经 storefile 的 state.json / config.yaml / secrets 写入，不约束其它
//     组件（如 aicli 的 config manager）对 config.yaml 的写入路径；
//   - 锁无租约续约：持有者不做心跳，进程被挂起超过 StaleAfter 后锁可被合法窃取，
//     因此不能把锁当作长事务或互斥执行器使用；
//   - 释放时先以 token 校验持有者再删除，校验与删除之间仍有极小窗口（恰好被窃取）；
//     文件本身用临时文件 + rename 原子替换，最坏情况是后写覆盖前写，不会写坏文件。
package storefile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// dirLockName 是数据目录锁的目录名（位于 state.json 同级的 stateDir 下）。
	dirLockName = "connectors.lock"
	// dirLockOwnerFile 记录当前持有者（token/pid/获取时间），用于诊断与陈旧判断。
	dirLockOwnerFile = "owner"

	defaultLockRetryInterval  = 25 * time.Millisecond
	defaultLockStaleAfter     = 30 * time.Second
	defaultLockAcquireTimeout = 10 * time.Second
)

// LockOptions 控制数据目录锁的重试/陈旧/等待上限；零值字段用默认值。
// 测试可缩短 StaleAfter 以快速验证窃取路径；宿主通常保持默认。
type LockOptions struct {
	// RetryInterval 是抢锁失败后的重试间隔（默认 25ms）。
	RetryInterval time.Duration
	// StaleAfter 是锁的陈旧阈值：持有超过该时长未释放即可被窃取（默认 30s）。
	StaleAfter time.Duration
	// AcquireTimeout 是单次抢锁的最长等待时间（默认 10s）。
	AcquireTimeout time.Duration
}

func (o LockOptions) withDefaults() LockOptions {
	if o.RetryInterval <= 0 {
		o.RetryInterval = defaultLockRetryInterval
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = defaultLockStaleAfter
	}
	if o.AcquireTimeout <= 0 {
		o.AcquireTimeout = defaultLockAcquireTimeout
	}
	return o
}

// WithLockOptions 覆盖数据目录锁参数（链式；测试/特殊部署用）。
func (s *ConfigStore) WithLockOptions(opts LockOptions) *ConfigStore {
	if s != nil {
		s.lockOptions = opts.withDefaults()
	}
	return s
}

// DirLockPath 返回本 store 使用的数据目录锁路径；未配置 state/config 路径时为空
// （此时不抢锁，直接内存态写入）。
func (s *ConfigStore) DirLockPath() string {
	if s == nil {
		return ""
	}
	base := strings.TrimSpace(s.statePath)
	if base == "" {
		base = strings.TrimSpace(s.configPath)
	}
	if base == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(base), dirLockName)
}

// lockDir 获取数据目录锁并返回释放函数；路径不可推导时返回 no-op。
func (s *ConfigStore) lockDir() (func(), error) {
	path := s.DirLockPath()
	if path == "" {
		return func() {}, nil
	}
	return AcquireDirLock(path, s.lockOptions)
}

// AcquireDirLock 获取 path 处的目录锁；path 通常来自 ConfigStore.DirLockPath()。
// 返回的释放函数幂等；锁已被他人窃取时释放不动他人的锁。
func AcquireDirLock(path string, opts LockOptions) (func(), error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("storefile: lock path is required")
	}
	opts = opts.withDefaults()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("storefile: create lock parent dir: %w", err)
	}
	deadline := time.Now().Add(opts.AcquireTimeout)
	for {
		err := os.Mkdir(path, 0o755)
		if err == nil {
			token := newLockToken()
			// owner 文件仅用于诊断/陈旧判断；写失败不致命（mtime 兜底）。
			_ = os.WriteFile(filepath.Join(path, dirLockOwnerFile), []byte(formatLockOwner(token)), 0o644)
			released := false
			return func() {
				if released {
					return
				}
				released = true
				releaseDirLock(path, token)
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("storefile: create dir lock %s: %w", path, err)
		}
		// 已存在：陈旧则原子窃取（rename 只有一个赢家），否则重试等待。
		if lockDirStale(path, opts.StaleAfter) {
			stalePath := fmt.Sprintf("%s.stale-%d-%d", path, os.Getpid(), time.Now().UnixNano())
			if renameErr := os.Rename(path, stalePath); renameErr == nil {
				_ = os.RemoveAll(stalePath)
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("storefile: acquire dir lock %s timeout after %s", path, opts.AcquireTimeout)
		}
		time.Sleep(opts.RetryInterval)
	}
}

func releaseDirLock(path, token string) {
	data, err := os.ReadFile(filepath.Join(path, dirLockOwnerFile))
	if err != nil || lockOwnerToken(string(data)) != token {
		// 锁已被窃取/易主：绝不删除他人的锁。
		return
	}
	_ = os.RemoveAll(path)
}

func lockDirStale(path string, staleAfter time.Duration) bool {
	now := time.Now()
	if data, err := os.ReadFile(filepath.Join(path, dirLockOwnerFile)); err == nil {
		if acquired, ok := parseLockOwnerTime(string(data)); ok {
			return now.Sub(acquired) > staleAfter
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) > staleAfter
}

func newLockToken() string {
	return strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func formatLockOwner(token string) string {
	return token + "\n" + strconv.Itoa(os.Getpid()) + "\n" + time.Now().UTC().Format(time.RFC3339Nano) + "\n"
}

func lockOwnerToken(data string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(data), "\n")
	return strings.TrimSpace(line)
}

func parseLockOwnerTime(data string) (time.Time, bool) {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) < 3 {
		return time.Time{}, false
	}
	acquired, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lines[2]))
	if err != nil {
		return time.Time{}, false
	}
	return acquired, true
}
