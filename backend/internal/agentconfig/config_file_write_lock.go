package agentconfig

import (
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// 配置文件写锁（方案 §3.4 M11 / §12 R4）。
//
// 本包内每一份**配置文件**的写入都是「读 → 改 → 写」：先读整份文件，做字段级或
// 文档级合并，再整份写回。若两个写入端并发执行，后写者会带着同一份旧快照落盘，
// 静默覆盖前者的字段（丢更新）。可达性成立：aicli TUI 与内嵌 runtime-server 的 API
// handler 调用的是同一批写入函数（`UpdateAICLIRoutingSection`、
// `UpdateWorkspaceRoutingSection`、`UpdateAICLIChatPreferences`、
// `UpdateAICLIThemePreferences`、`UpdateProviderConfig`、provider 管理/代理写入、
// `ApplyMergedDocumentChanges` 等），两个不同会话绑定同一工作区/同一份 config 时也会
// 落到同一份文件。
//
// 因此规则是**按目标文件**统一的一把锁，而不是按功能各写一把：
//
//   - 锁按**归一化后的目标文件路径**注册，进程内共享（本文件的 map）；
//   - 进程内互斥之外再取一把**跨进程 OS 级锁**，覆盖 aicli TUI 与内嵌 runtime-server
//     分属不同进程的场景（见 LockConfigFileWrite 的跨进程说明与降级策略）；
//   - 锁必须覆盖**整个「读-改-写」**（含读取），只锁写入等于没锁；
//   - 落盘的原子写（`writeFileAtomic`）仍负责单次写入不撕裂。
//
// 约定（避免同一路径重入自锁）：**导出的写入入口自己取锁**，内部 `...Locked` 变体假定
// 调用方已持有该路径的锁；一次事务涉及多个文件时用 `LockConfigFileWriteAll`（按归一化
// 锁键去重 + 字典序取锁，防 ABBA），不要手写多把锁的取放顺序。
type configFileWriteLockEntry struct {
	mu   sync.Mutex
	refs int
}

// configFileOSLockSuffix 是跨进程旁路锁文件的固定后缀：锁文件 = 归一化目标路径 +
// 该后缀（如 `config.yaml.lock`）。绝不锁目标文件本身，理由见 LockConfigFileWrite。
const configFileOSLockSuffix = ".lock"

var (
	configFileWriteLocksMu sync.Mutex
	configFileWriteLocks   = map[string]*configFileWriteLockEntry{}
)

// LockConfigFileWrite 获取目标配置文件的写锁，返回释放函数；释放函数可安全重复调用。
// 路径为空时返回 no-op 释放函数（调用方无需为此单独分支）。
//
// 加锁分两层：
//  1. **进程内**：按归一化路径共享的互斥锁（本文件内的 map，覆盖 goroutine 并发）；
//  2. **跨进程**：在旁路锁文件 `<归一化目标路径>.lock` 上取 OS 级锁（Windows
//     LockFileEx / Unix flock，均阻塞式），由平台文件实现（acquireConfigFileOSLock）。
//
// 为什么锁旁路文件而不是目标文件：目标文件由 writeFileAtomic「写临时文件 + rename」
// 替换，rename 后是**新的 inode / 新的文件对象**，锁目标文件会在替换那一刻失去互斥
// 意义（后续进程可在新对象上立刻加锁）。旁路锁文件只创建、不重命名，身份稳定。
//
// 为什么键是绝对路径：同一个文件可能以 `./.aicli/config.yaml` 与
// `$HOME/.aicli/config.yaml` 两种写法到达这里，若按原始字符串注册就会各拿一把锁、
// 各写一个锁文件，互斥形同虚设。归一化因此解析为绝对路径，并在可能时解析符号链接
// （父目录不存在时退化为只做 Abs+Clean）。
//
// 降级（静默，绝不 panic、绝不让写入失败）：锁文件所在目录不存在、创建/加锁失败
// （权限、只读盘、网络盘不支持字节区间锁或 flock 等）时退化为「仅进程内锁」并继续
// 写入流程——跨进程并发退回方案登记的「后写胜出」，由 writeFileAtomic 保证不撕裂。
// 锁是正确性增强，不是写入的前置条件，因此降级路径不返回错误。
//
// 释放：幂等（sync.Once），关闭 OS 句柄/fd，且**不删除锁文件**——删除会与另一个进程
// 的「打开 → 加锁」形成竞态（unlink 后双方可能各持一把锁）。0 字节残留无害。
//
// 用法：
//
//	unlock := LockConfigFileWrite(path)
//	defer unlock()
//	// 读-改-写
func LockConfigFileWrite(path string) func() {
	key := normalizeConfigWriteLockKey(path)
	if key == "" {
		return func() {}
	}

	configFileWriteLocksMu.Lock()
	entry := configFileWriteLocks[key]
	if entry == nil {
		entry = &configFileWriteLockEntry{}
		configFileWriteLocks[key] = entry
	}
	// 先加引用再等锁：等待者持有引用时条目不会被回收，避免「释放者删表 → 等待者
	// 拿到孤儿锁」。
	entry.refs++
	configFileWriteLocksMu.Unlock()

	entry.mu.Lock()

	// 进程内互斥已持有，再取跨进程 OS 锁：同一进程内的 goroutine 不会各自打开
	// 锁文件互相阻塞（同一进程对同一文件重复加锁会自锁），也避免无谓的系统调用。
	osUnlock, ok := acquireConfigFileOSLock(key)
	if !ok || osUnlock == nil {
		// 静默降级：仅进程内锁（原因见函数注释）。
		osUnlock = func() {}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			// 逆序释放：先放 OS 锁再放进程内锁；osUnlock 自身幂等且只关闭句柄。
			osUnlock()
			entry.mu.Unlock()
			configFileWriteLocksMu.Lock()
			entry.refs--
			if entry.refs <= 0 {
				delete(configFileWriteLocks, key)
			}
			configFileWriteLocksMu.Unlock()
		})
	}
}

// LockConfigFileWriteAll 为一次事务涉及的多个目标文件取共享配置写锁，返回统一释放
// 函数（按取锁逆序释放，可重复调用）。
//
// 用途：读 A 文件 → 合并 → 写 B 文件这类跨文件读-改-写（例如 runtime-server 的
// skills_runtime 策略落盘可能是「读有效来源文件 → 写快照/基础文件」），只锁写入端会
// 让另一端在读取窗口里丢更新，两侧都必须锁住。
//
// 顺序：先按归一化锁键去重，再按字典序取锁。两个调用方若以相反顺序请求同一对文件，
// 定序是避免 ABBA 死锁的唯一保证——因此**凡是取多把锁都必须走本函数**。
func LockConfigFileWriteAll(paths ...string) func() {
	keys := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		key := normalizeConfigWriteLockKey(path)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	unlocks := make([]func(), 0, len(keys))
	for _, key := range keys {
		unlocks = append(unlocks, LockConfigFileWrite(key))
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(unlocks) - 1; i >= 0; i-- {
				unlocks[i]()
			}
		})
	}
}

// normalizeConfigWriteLockKey 归一化锁键：
//   - 绝对路径（Abs 失败时保留 Clean 结果）；
//   - 尽力解析符号链接（整条路径存在时解析整条，否则解析父目录）；
//   - Windows 下大小写不敏感（同一文件的不同大小写写法必须命中同一把锁）。
func normalizeConfigWriteLockKey(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	key, err := filepath.Abs(trimmed)
	if err != nil || strings.TrimSpace(key) == "" {
		key = filepath.Clean(trimmed)
	}
	key = resolveSymlinksBestEffort(key)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

// resolveSymlinksBestEffort 解析符号链接；文件不存在时退化为解析父目录，仍然失败则
// 原样返回。目标文件由 writeFileAtomic 替换，锁键只用于标识「同一份文件」，不要求
// 路径可读。
func resolveSymlinksBestEffort(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil && strings.TrimSpace(resolved) != "" {
		return filepath.Clean(resolved)
	}
	dir, file := filepath.Split(filepath.Clean(path))
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(file) == "" {
		return filepath.Clean(path)
	}
	resolvedDir, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil || strings.TrimSpace(resolvedDir) == "" {
		return filepath.Clean(path)
	}
	return filepath.Join(resolvedDir, file)
}
