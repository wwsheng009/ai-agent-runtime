package lsp

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// 进程内共享池（2026-10-01，收口轮十二）。
//
// 实测依据（docs/lsp/cross-session-sharing-analysis.md §1）：同一模块起第二个
// gopls 的边际成本是**一整个实例**（525MB / 541MB = 97%），索引与堆全在进程
// 私有内存里，没有任何工作被复用。因此同一进程内指向同一 root 的多个会话必须
// 共用一个池，否则每多一个会话就多付 ~500MB 内存加一次完整冷启。
//
// 键的构成是"什么决定了这份进程"：root + 启用成员的完整声明 + 影响 client
// 行为的诊断阈值。任一项不同就是两个池——宁可重复也不能让两个会话用不同的
// server 配置共享一个进程。

// sharedKey 标识一份可共享的池。取字符串而非结构体键是为了让 fingerprint 的
// 长度有界（root + 哈希）。
type sharedKey string

// sharedRegistryEntry 是共享表的一个桶：真正的池 + 引用计数。
type sharedRegistryEntry struct {
	registry *Registry
	hub      *observerHub
	refs     int
}

// sharedRegistries 是进程级共享表（无状态守护进程之外的唯一全局）。
var sharedRegistries = struct {
	mu     sync.Mutex
	byKey  map[sharedKey]*sharedRegistryEntry
	closed bool
}{byKey: map[sharedKey]*sharedRegistryEntry{}}

// AcquireSharedRegistry 返回 root 上可共享的池，并把 release 交给调用方在
// 自己不再使用时调用。最后一个 release 会关停进程并把桶移出表，下一个
// Acquire 于是拿到一个全新的池（而不是一个 stopped 的残骸）。
//
// (nil, nil, false) 表示"本次不参与共享"——调用方拿到的是一个私有池，自己
// 负责关停。三种不共享的情况：
//
//   - cfg.Enabled=false：没有进程可共享，进表只会让禁用配置占住键。
//   - opts.Dial != nil：注入传输（测试 / 自定义宿主）必须私有，否则两个互不
//     相干的假 server 会被并成一个，测试隔离当场失效。真实 spawn 路径
//     （Dial=nil）才共享。
//   - 键为空：root 解析不出来。
//
// 为什么按 (root, 声明) 而不是只按 root：两个会话可能配置了不同的成员集合
// 或不同的诊断阈值。共享进程会让"谁生效"变成注册顺序问题——那是最难查的一类
// bug，宁可多一个进程。
func AcquireSharedRegistry(cfg Config, root string, opts RegistryOptions) (*Registry, *observerHub, func(), bool) {
	if !cfg.Enabled || opts.Dial != nil {
		// 私有池的观测者链与共享前逐字节一致（composeObservers），不引入 hub：
		// 单会话路径没有任何东西需要扇出，多一层只增加回归面。
		return NewRegistry(cfg, root, opts), nil, nil, false
	}
	key, ok := sharedRegistryKey(cfg, root, opts)
	if !ok {
		return NewRegistry(cfg, root, opts), nil, nil, false
	}

	sharedRegistries.mu.Lock()
	defer sharedRegistries.mu.Unlock()

	if entry := sharedRegistries.byKey[key]; entry != nil {
		entry.refs++
		return entry.registry, entry.hub, sharedReleaser(key, entry), true
	}
	// hub 属于**桶**，不属于创建者：第二个借用方必须能挂到同一个 hub 上，
	// 早先的版本在 Bridge 侧建 hub，第二个借用方的事件会发到一个没人读的
	// hub 里——症状是"第二个会话收不到任何事件"。
	hub := newObserverHub(logObserver(LoggerOrNop(opts.Logger)))
	entry := &sharedRegistryEntry{refs: 1, hub: hub}
	entry.registry = NewRegistry(cfg, root, withHubObserver(opts, hub.emit))
	sharedRegistries.byKey[key] = entry
	return entry.registry, entry.hub, sharedReleaser(key, entry), true
}

// withHubObserver 复制 opts 并把观测者换成 hub 转发器。
func withHubObserver(opts RegistryOptions, emit Observer) RegistryOptions {
	opts.Observer = emit
	return opts
}

// sharedReleaser 构造一次性 release。once 保护是必需的：宿主可能在 Close 与
// 迟到清理两条路径上各调一次，重复 release 会把 refs 减到 0 以下，提前关停
// 别人还在用的池。
func sharedReleaser(key sharedKey, entry *sharedRegistryEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			sharedRegistries.mu.Lock()
			entry.refs--
			last := entry.refs <= 0
			if last && sharedRegistries.byKey[key] == entry {
				delete(sharedRegistries.byKey, key)
			}
			sharedRegistries.mu.Unlock()
			if last {
				// 只在锁外关停：关停要走 LSP shutdown 往返与进程树收尾，
				// 持锁做 I/O 会把所有并发 Acquire 卡住。
				entry.registry.Stop(nil)
			}
		})
	}
}

// SharedSessionCount 返回本进程内共享同一份池的会话数（未共享时为 0）。
// 状态面用它把"两个会话看到同一个 pid"讲清楚，而不是让用户以为是重复实例。
func SharedSessionCount(registry *Registry) int {
	if registry == nil {
		return 0
	}
	sharedRegistries.mu.Lock()
	defer sharedRegistries.mu.Unlock()
	for _, entry := range sharedRegistries.byKey {
		if entry.registry == registry {
			return entry.refs
		}
	}
	return 0
}

// sharedRegistryKey 计算共享键。返回 ok=false 表示不该共享。
func sharedRegistryKey(cfg Config, root string, opts RegistryOptions) (sharedKey, bool) {
	root = normalizeRoot(root)
	if root == "" {
		return "", false
	}
	cfg = cfg.Normalize()
	var parts []string
	for _, spec := range cfg.Servers {
		if !spec.IsEnabled() {
			continue
		}
		parts = append(parts, "server:"+serverSpecFingerprint(spec))
	}
	if len(parts) == 0 {
		// 没有成员的池不共享：它是惰性空壳，共不共享都省不下任何进程，
		// 却让"共享"这个词在状态面变得可疑。
		return "", false
	}
	sort.Strings(parts)

	diagnostics := cfg.Diagnostics.Normalize()
	// 只纳入真正改变 client 行为的诊断阈值：WaitMS/MaxItems/MaxChars 等是
	// 每次请求现算的，属于 Bridge 层，不进进程身份；EmptyEarlyAccept 与
	// EmptyConfirmMS 改变 client 的等待行为，必须进。
	parts = append(parts,
		"empty_early_accept:"+boolKey(diagnostics.EmptyEarlyAcceptValue()),
		"empty_confirm_ms:"+strconv.Itoa(diagnostics.EmptyConfirmMS),
		"max_tracked_docs:"+strconv.Itoa(cfg.MaxTrackedDocsValue()),
		// 崩溃恢复预算必须进键：重启计数与窗口都存在**共享**的 entry 上
		// （registryEntry.restarts / lastRestartAt），而预算是从 entry.parent.cfg
		// 读的——也就是第一个注册者的声明。不进键的后果是双向静默：声明
		// restartLimit:0（禁用恢复）的会话会拿到别人的 1 而在崩溃后被悄悄拉起，
		// 反过来声明要恢复的会话可能撞上别人的 0 而永久停在 degraded。
		"restart_limit:"+strconv.Itoa(cfg.RestartLimitValue()),
		"restart_window:"+cfg.RestartWindowValue().String(),
		"client:"+strings.TrimSpace(opts.ClientName)+"/"+strings.TrimSpace(opts.ClientVersion),
	)

	return sharedKey(strings.ToLower(root) + "|" + shortHash(strings.Join(parts, "\x00"))), true
}

// serverSpecFingerprint 把一个成员声明折叠成稳定串。Env 与
// InitializationOptions 是 map，必须按键排序，否则同一份配置会因迭代顺序
// 产生不同的键——那会让本该共享的池分裂成两个（正是要消除的浪费）。
func serverSpecFingerprint(spec ServerSpec) string {
	var parts []string
	parts = append(parts,
		"name:"+strings.TrimSpace(spec.Name),
		"command:"+strings.TrimSpace(spec.Command),
		"args:"+strings.Join(spec.Args, "\x01"),
		"languages:"+strings.Join(spec.Languages, "\x01"),
		"extensions:"+strings.Join(spec.Extensions, "\x01"),
		"filenames:"+strings.Join(spec.Filenames, "\x01"),
		"startup:"+spec.StartupTimeout.String(),
		"shutdown:"+spec.ShutdownTimeout.String(),
		"empty_conclusive:"+boolKey(spec.EmptyPublishConclusiveValue()),
	)
	parts = append(parts, "env:"+sortedMapFingerprint(spec.Env))
	parts = append(parts, "init:"+stableJSONFingerprint(spec.InitializationOptions))
	return strings.Join(parts, "\x02")
}

// observerHub 是共享池的观测者集合。共享之后 client 的事件要送到**每一个**
// 借用方，而 client 只在构造时拿到一个 Observer 函数——所以真正被注册进
// client 的必须是这一个转发器，宿主订阅通过 Subscribe 进出。
//
// 不这样做的话，第二个会话收不到任何事件；或者更糟，第一个会话关闭后仍然
// 收到它的 observer 调用（向一个已死的会话推事件）。
type observerHub struct {
	mu   sync.RWMutex
	base Observer
	subs map[uint64]Observer
	next uint64
}

func newObserverHub(base Observer) *observerHub {
	return &observerHub{base: base, subs: map[uint64]Observer{}}
}

// Subscribe 注册一个借用方的观测者，返回退订函数。退订必须在宿主停止使用池
// 时调用，否则事件会继续流向已关闭的会话。
func (h *observerHub) Subscribe(observer Observer) func() {
	if h == nil || observer == nil {
		return func() {}
	}
	h.mu.Lock()
	h.next++
	id := h.next
	h.subs[id] = observer
	h.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			h.mu.Unlock()
		})
	}
}

// emit 先取快照再回调：观测者可能反过来调池的方法（状态查询、退订），持锁
// 回调会自锁。
func (h *observerHub) emit(event Event) {
	if h == nil {
		return
	}
	h.mu.RLock()
	base := h.base
	targets := make([]Observer, 0, len(h.subs))
	for _, observer := range h.subs {
		targets = append(targets, observer)
	}
	h.mu.RUnlock()

	if base != nil {
		base(event)
	}
	for _, observer := range targets {
		observer(event)
	}
}

func boolKey(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func sortedMapFingerprint(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, "\x01")
}

// stableJSONFingerprint 折叠任意初始化选项。encoding/json 对 map 键排序，
// 所以同一份语义配置永远得到同一个串——这正是 map 声明能被安全放进共享键的
// 前提。
func stableJSONFingerprint(value map[string]interface{}) string {
	if len(value) == 0 {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		// 编码不了的值（channel、func、循环结构）说明这份配置本来就无法
		// 作为 initialize 参数发送。保守地让共享失败：宁可多一个进程，
		// 也不要两个会话共享一个进程身份不明的池。
		return "\x00unsupported"
	}
	return shortHash(string(encoded))
}
