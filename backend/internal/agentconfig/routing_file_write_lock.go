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
// 锁按**目标文件路径**注册，进程内共享。跨进程并发不在本锁范围内，仍由落盘的原子写
// （`writeFileAtomic`）保证单次写入不撕裂。
type routingFileWriteLockEntry struct {
	mu   sync.Mutex
	refs int
}

var (
	routingFileWriteLocksMu sync.Mutex
	routingFileWriteLocks   = map[string]*routingFileWriteLockEntry{}
)

// LockRoutingFileWrite 获取目标文件的写锁，返回释放函数；释放函数可安全重复调用。
// 路径为空时返回 no-op 释放函数（调用方无需为此单独分支）。
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

	var once sync.Once
	return func() {
		once.Do(func() {
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
