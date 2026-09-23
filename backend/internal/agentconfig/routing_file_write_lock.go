package agentconfig

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// 路由写入文件锁（方案 §3.4 M11 / §12 R4）。
//
// workspace 层（`chat-prefs.yaml`）与 config 层（`.aicli/config.yaml`）的写入都是
// 「读 → 改 → 写」：先读整份文件，字段级合并补丁，再整份写回。若两个写入端并发
// 执行，后写者会带着同一份旧快照落盘，静默覆盖前者的字段（丢更新）。可达性成立：
// aicli TUI（`chat_routing_layers.go`）与内嵌 runtime-server 的 API handler
// （PATCH /api/runtime/sessions/{id}/routing 的 workspace/config 分支）调用的是
// **同一个** `UpdateWorkspaceRoutingSection` / `UpdateAICLIRoutingSection`；两个不同
// 会话绑定同一工作区时也会落到同一份 `chat-prefs.yaml`。
//
// 锁按**目标文件路径**注册，进程内共享；进程内互斥之外再取一把**跨进程 OS 级锁**，
// 覆盖 aicli TUI 与内嵌 runtime-server 分属不同进程的场景（见 LockRoutingFileWrite
// 的跨进程说明与降级策略）。落盘的原子写（`writeFileAtomic`）仍负责单次写入不撕裂。
type routingFileWriteLockEntry struct {
	mu   sync.Mutex
	refs int
}

// routingFileOSLockSuffix 是跨进程旁路锁文件的固定后缀：锁文件 = 归一化目标路径 +
// 该后缀（如 `chat-prefs.yaml.lock`）。绝不锁目标文件本身，理由见 LockRoutingFileWrite。
const routingFileOSLockSuffix = ".lock"

var (
	routingFileWriteLocksMu sync.Mutex
	routingFileWriteLocks   = map[string]*routingFileWriteLockEntry{}
)

// LockRoutingFileWrite 获取目标文件的写锁，返回释放函数；释放函数可安全重复调用。
// 路径为空时返回 no-op 释放函数（调用方无需为此单独分支）。
//
// 加锁分两层：
//  1. **进程内**：按归一化路径共享的互斥锁（本文件内的 map，覆盖 goroutine 并发）；
//  2. **跨进程**：在旁路锁文件 `<归一化目标路径>.lock` 上取 OS 级锁（Windows
//     LockFileEx / Unix flock，均阻塞式），由平台文件实现（acquireRoutingFileOSLock）。
//
// 为什么锁旁路文件而不是目标文件：目标文件由 writeFileAtomic「写临时文件 + rename」
// 替换，rename 后是**新的 inode / 新的文件对象**，锁目标文件会在替换那一刻失去互斥
// 意义（后续进程可在新对象上立刻加锁）。旁路锁文件只创建、不重命名，身份稳定。
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
//	unlock := LockRoutingFileWrite(path)
//	defer unlock()
//	// 读-改-写
func LockRoutingFileWrite(path string) func() {
	key := normalizeRoutingWriteLockKey(path)
	if key == "" {
		return func() {}
	}

	routingFileWriteLocksMu.Lock()
	entry := routingFileWriteLocks[key]
	if entry == nil {
		entry = &routingFileWriteLockEntry{}
		routingFileWriteLocks[key] = entry
	}
	// 先加引用再等锁：等待者持有引用时条目不会被回收，避免「释放者删表 → 等待者
	// 拿到孤儿锁」。
	entry.refs++
	routingFileWriteLocksMu.Unlock()

	entry.mu.Lock()

	// 进程内互斥已持有，再取跨进程 OS 锁：同一进程内的 goroutine 不会各自打开
	// 锁文件互相阻塞（同一进程对同一文件重复加锁会自锁），也避免无谓的系统调用。
	osUnlock, ok := acquireRoutingFileOSLock(key)
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
			routingFileWriteLocksMu.Lock()
			entry.refs--
			if entry.refs <= 0 {
				delete(routingFileWriteLocks, key)
			}
			routingFileWriteLocksMu.Unlock()
		})
	}
}

// normalizeRoutingWriteLockKey 归一化锁键：清理路径分隔符，Windows 下大小写不敏感
// （同一文件的不同大小写写法必须命中同一把锁）。
func normalizeRoutingWriteLockKey(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	key := filepath.Clean(trimmed)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}
