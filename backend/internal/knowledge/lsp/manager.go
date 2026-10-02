// Package lsp 是知识层的进程外语义通道（04 §5 Phase 4 交付 2/3）。
//
// 职责边界：
//   - 进程生命周期：启动/崩溃检测/超时/内存上限/僵尸回收，全部复用既有基础设施：
//     进程树守卫走 internal/executor（ADR-0005），协议与位置编码走 internal/lsp
//     （ADR-0006 的 canonical 边界已在那里实现）；
//   - 重复防护：spawn 前按 ADR-0002 §4.4 获取 <workspace>/.aicli/knowledge/lsp/
//     <language>-<sha256(root)[0:12]>.lock；锁被活进程持有时**不 spawn**，
//     直接降级（宁可精度低，不可重复实例）；
//   - 降级可观测：任何失败都写 DegradeReason，调用方据此走 parser 通道，
//     绝不让 agent 循环失败（Degrade-Not-Fail）。
package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// ErrLockHeld 表示同一 (language, root) 已有活进程持锁：不 spawn，直接降级。
var ErrLockHeld = errors.New("knowledge/lsp: lock held by a live process")

// ErrProcessLimit 表示进程数已达上限。
var ErrProcessLimit = errors.New("knowledge/lsp: process limit reached")

// ErrMemoryLimit 表示进程内存超过配置上限。
var ErrMemoryLimit = errors.New("knowledge/lsp: memory limit exceeded")

// ErrSharedClientUnavailable 表示共享模式下宿主没能给出可用的语言服务器进程。
// 它与 ErrLockHeld 同级：调用方必须降级，不得改走自建路径——那正是本轮要消除
// 的第二个实例。
var ErrSharedClientUnavailable = errors.New("knowledge/lsp: shared server unavailable")

// LockRecord 是 ADR-0002 §4.4 定义的锁文件内容。
type LockRecord struct {
	PID       int    `json:"pid"`
	RootURI   string `json:"root_uri"`
	Command   string `json:"command"`
	StartedAt string `json:"started_at"`
}

// Options 配置一个 (language, root) 的语义通道。
type Options struct {
	// Root 是 workspace 根目录（锁与相对路径的锚点）。
	Root string
	// Spec 是语言服务器规格（internal/lsp 预设）。
	Spec baselsp.ServerSpec
	// MaxProcesses 是本通道允许的进程数上限（v1 每通道 1 个）。
	MaxProcesses int
	// MemoryLimitMB 是单进程内存软上限；超限即回收并降级。
	MemoryLimitMB int
	// StartupTimeout 是 initialize 握手超时。
	StartupTimeout time.Duration
	// RequestTimeout 是单次语义查询超时（由调用方使用）。
	RequestTimeout time.Duration
	// Dial 是启动注入缝；nil 时使用 baselsp.SpawnProcess（已接进程树守卫）。
	Dial baselsp.DialFunc
	// SharedClient 让本通道复用宿主已持有的语言服务器进程（诊断池），nil = 自建。
	//
	// 单实例约束（ADR-0002 §4.4 的延伸）：同一个语言在一个进程里只允许一个
	// server 进程。两条链路（诊断池 / 语义通道）都需要 gopls，各自 spawn 会得到
	// 两个进程——实测 gopls 常驻数百 MB，双实例会把内存压力直接转成崩溃（历史
	// 崩溃根因是整机内存耗尽）。因此宿主持有进程时本通道**只借用**：
	//   - 不取锁、不 spawn、不关停（进程归宿主）；
	//   - 借不到就降级，绝不自建（自建就是第二个实例）；
	//   - 宿主重启（/lsp restart）会让借来的 client 失效，每次 Ensure 重新借，
	//     缓存的旧 client 不复用。
	SharedClient func(ctx context.Context, serverName string) *baselsp.Client
	// Logger 透传给 internal/lsp。
	Logger baselsp.Logger
	// Now 是时钟注入缝（测试用）。
	Now func() time.Time
	// MemoryProbe 是内存探测注入缝；nil 时用平台实现（Windows 读工作集）。
	MemoryProbe func(pid int) (int64, error)
	// SkipLock 关闭锁检查（仅测试/诊断用；生产路径不得设置）。
	SkipLock bool
}

func (o Options) normalized() Options {
	// 0 = 未配置 → 默认 1；负值是显式非法配置，保留给 Ensure 拒绝（fail closed）。
	if o.MaxProcesses == 0 {
		o.MaxProcesses = 1
	}
	if o.StartupTimeout <= 0 {
		o.StartupTimeout = 15 * time.Second
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 5 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MemoryProbe == nil {
		o.MemoryProbe = probeProcessMemory
	}
	if o.Dial == nil {
		o.Dial = baselsp.SpawnProcess
	}
	return o
}

// Manager 管理一个 (language, root) 的语义通道。
//
// 并发安全；所有方法在失败路径上返回错误且把原因写入 DegradeReason，
// 调用方必须据此降级而不是重试到超时。
type Manager struct {
	mu     sync.Mutex
	opts   Options
	client *baselsp.Client
	reason string
	closed bool
	// shared 标记当前 client 是从宿主借来的：关停与解锁都不归本通道，
	// 且"进程已退出"是可恢复事件（宿主可能正在重启它），不能像自建进程
	// 那样把降级原因钉死。
	shared bool
}

// NewManager 构造通道管理器（不启动进程；Ensure 才启动）。
func NewManager(opts Options) *Manager {
	return &Manager{opts: opts.normalized()}
}

// RequestTimeout 返回单次查询超时（调用方用于 context 限时）。
func (m *Manager) RequestTimeout() time.Duration { return m.opts.RequestTimeout }

// LanguageServerName 返回语言服务器名（版本串与诊断用）。
func (m *Manager) LanguageServerName() string {
	name := strings.TrimSpace(m.opts.Spec.Name)
	if name == "" {
		name = "lsp"
	}
	return name
}

// Status 是语义通道的观测快照（供 /lsp 与 lsp_servers 渲染诊断信息）。
//
// 与 internal/lsp 的 ServerStatus 是两套东西：那套描述诊断池（编辑工具内联
// 诊断用），这套描述 knowledge.lsp 语义通道（code_* 工具的 definition/
// references/symbols 用）。两条链路各自 spawn 进程，因此必须分别可见——否则
// 用户看到"gopls ready"却拿到 source=index，完全无从判断降级发生在哪一侧。
type Status struct {
	// Server 是语言服务器名（gopls）。
	Server string
	// Root 是通道锚定的模块根（gopls 的类型信息与锁都以此为锚）。
	Root string
	// State 是生命周期状态，取值与 internal/lsp 的 ServerState 同名，便于
	// 两个状态面用同一套渲染口径。
	State string
	// PID 是已启动进程号；0 = 未启动（惰性启动，不预热）。
	PID int
	// Reason 是降级原因（空串 = 无降级）。
	Reason string
	// LockPath 是 ADR-0002 §4.4 的锁文件位置（可观测/诊断用）。
	LockPath string
	// Shared 标记该进程由宿主（诊断池）持有，本通道只借用。
	// 用户看到 pid 与诊断池一致时，这一项解释了"为什么语义通道没有自己的 pid"。
	Shared bool
	// StartedAt 是最近一次 Ensure 的时刻（零值 = 从未启动）。
	StartedAt time.Time
}

// Status 返回当前观测快照。它不启动进程、不加锁，只读既有状态。
func (m *Manager) Status() Status {
	if m == nil {
		return Status{State: "unavailable", Reason: "manager is nil"}
	}
	m.mu.Lock()
	client := m.client
	closed := m.closed
	reason := m.reason
	shared := m.shared
	m.mu.Unlock()

	st := Status{
		Server:   m.LanguageServerName(),
		Root:     m.opts.Root,
		Reason:   strings.TrimSpace(reason),
		LockPath: m.LockPath(),
		Shared:   shared,
	}
	switch {
	case closed:
		st.State = "stopped"
		if st.Reason == "" {
			st.Reason = "channel closed"
		}
	case client == nil:
		// 语义通道是惰性启动的：从未 Ensure 是正常状态，不是故障。
		st.State = "idle"
		if st.Reason == "" {
			if shared {
				st.Reason = "not borrowed yet (lazy; reuses the diagnostics pool process)"
			} else {
				st.Reason = "not started (lazy; starts on first semantic query)"
			}
		}
	default:
		select {
		case <-client.Done():
			if shared {
				// 宿主重启/关停了这个进程：不是本通道的崩溃，等下一次查询
				// 重新借即可，如实说成"共享进程已退出"。
				st.State = "degraded"
				st.Reason = "shared language server exited (host restarting?)"
			} else {
				st.State = "crashed"
				st.Reason = "language server exited"
			}
		default:
			st.State = "ready"
			st.PID = client.PID()
			if shared {
				// 借来的进程不持锁：显示锁路径会让用户去找一个我们从未创建的文件。
				st.LockPath = ""
			}
		}
	}
	return st
}

// DegradeReason 返回最近一次降级原因（空串表示无降级）。
func (m *Manager) DegradeReason() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reason
}

// RetryableDegrade 报告当前降级是否值得下次查询重试。
//
// 自建模式返回 false：进程崩溃/启动失败后逐查询重试会造成重启风暴（ADR-0002
// 的既定取舍），重建交给显式 Ensure。
//
// 共享模式返回 true：进程归宿主所有，"借不到"往往只是宿主还在启动或刚重启，
// 宿主随时会恢复。把这种瞬时失败钉死会让语义通道在整个会话里永久不可用——
// 这正是上一版借用实现的缺陷。自建模式（含"尚未启动"）一律 false：没启动时
// 根本没有降级原因，调用方本来就会走 Ensure；启动失败后则按既定取舍不重试。
func (m *Manager) RetryableDegrade() bool {
	if m == nil {
		return false
	}
	// 判据是"是否配置了共享模式"，不是"此刻是否借到"：借用失败的那一刻
	// shared 还是 false，而那恰恰是最需要允许重试的时刻。
	return m.opts.SharedClient != nil
}

// Client 返回已启动的客户端；未启动或已关闭时返回 nil。
func (m *Manager) Client() *baselsp.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client
}

// Available 报告语义通道当前是否可用（进程活着且未降级）。
func (m *Manager) Available() bool {
	m.mu.Lock()
	client := m.client
	closed := m.closed
	shared := m.shared
	m.mu.Unlock()
	if closed || client == nil {
		return false
	}
	select {
	case <-client.Done():
		// 自建进程退出 = 崩溃/被杀，钉住降级原因等显式重建（逐查询重启会
		// 造成重启风暴）；共享进程退出通常只是宿主重启了它，下一次 Ensure
		// 会重新借到——此时钉死原因会让整条语义通道永久不可用。
		if shared {
			m.mu.Lock()
			if m.shared {
				m.client = nil
				m.shared = false
				m.reason = ""
			}
			m.mu.Unlock()
			return false
		}
		m.setReason("language server exited")
		return false
	default:
		return true
	}
}

// LockPath 返回 ADR-0002 §4.4 的锁文件路径（可观测/测试用）。
func (m *Manager) LockPath() string {
	sum := sha256.Sum256([]byte(normalizeRoot(m.opts.Root)))
	token := hex.EncodeToString(sum[:])[:12]
	lang := strings.ToLower(strings.TrimSpace(m.opts.Spec.Name))
	if lang == "" {
		lang = "lsp"
	}
	return filepath.Join(m.opts.Root, ".aicli", "knowledge", "lsp", lang+"-"+token+".lock")
}

// Ensure 确保语义通道可用；失败时返回错误并把原因写入 DegradeReason。
//
// 顺序（与 ADR-0002/0005 对齐）：锁 → 进程数上限 → spawn（守卫）→ 内存上限。
// 任一步失败都不得 panic、不得阻塞调用方超过 StartupTimeout。
func (m *Manager) Ensure(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("knowledge/lsp: manager closed")
	}
	if m.shared {
		// 借来的 client：宿主可能已经重启过它（/lsp restart），每次 Ensure
		// 重新借一次，绝不复用缓存的旧 client。
		m.client = nil
		m.shared = false
	}
	if m.client != nil {
		client := m.client
		m.mu.Unlock()
		select {
		case <-client.Done():
			m.mu.Lock()
			m.client = nil
			m.setReasonLocked("language server exited")
			m.mu.Unlock()
		default:
			return nil
		}
	} else {
		m.mu.Unlock()
	}

	// 单实例优先：宿主持有该语言的进程时只借用，不 spawn、不取锁。
	if m.opts.SharedClient != nil {
		return m.ensureShared(ctx)
	}

	// ADR-0002 §4.4：锁新鲜（持有者存活）→ 不 spawn，降级。
	if !m.opts.SkipLock {
		if err := m.acquireLock(); err != nil {
			m.setReason(err.Error())
			return err
		}
	}

	// 进程数上限：v1 每通道 1 个；超限降级（不得悄悄多起）。
	if m.opts.MaxProcesses < 1 {
		err := fmt.Errorf("%w: max_processes=%d", ErrProcessLimit, m.opts.MaxProcesses)
		m.setReason(err.Error())
		return err
	}

	startCtx, cancel := context.WithTimeout(ctx, m.opts.StartupTimeout)
	defer cancel()
	client, err := baselsp.NewClient(baselsp.ClientOptions{
		Spec:            m.opts.Spec,
		Root:            m.opts.Root,
		Dial:            m.opts.Dial,
		Log:             m.opts.Logger,
		StartupTimeout:  m.opts.StartupTimeout,
		ShutdownTimeout: m.opts.StartupTimeout,
	})
	if err != nil {
		m.releaseLock()
		m.setReason("client init failed: " + err.Error())
		return err
	}
	if err := client.Start(startCtx); err != nil {
		m.releaseLock()
		m.setReason("start failed: " + err.Error())
		return err
	}

	// 内存上限：超限立即回收（僵尸/泄漏进程不得存活）。
	if m.opts.MemoryLimitMB > 0 && client.PID() > 0 {
		if bytes, err := m.opts.MemoryProbe(client.PID()); err == nil && bytes > 0 {
			limit := int64(m.opts.MemoryLimitMB) * 1024 * 1024
			if bytes > limit {
				client.Shutdown(context.Background())
				m.releaseLock()
				err := fmt.Errorf("%w: %d MiB > %d MiB", ErrMemoryLimit, bytes/(1024*1024), m.opts.MemoryLimitMB)
				m.setReason(err.Error())
				return err
			}
		}
	}

	m.mu.Lock()
	m.client = client
	m.reason = ""
	m.mu.Unlock()
	return nil
}

// ensureShared 走借用路径。借到即用；借不到就降级并给出稳定原因，绝不自建
// （自建就是本轮要消除的第二个实例）。
func (m *Manager) ensureShared(ctx context.Context) error {
	client := m.opts.SharedClient(ctx, m.LanguageServerName())
	if client == nil {
	err := fmt.Errorf("%w: host pool has no live %s process", ErrSharedClientUnavailable, m.LanguageServerName())
	m.setReason(err.Error())
	return err
	}
	select {
	case <-client.Done():
		err := fmt.Errorf("%w: shared %s process already exited", ErrSharedClientUnavailable, m.LanguageServerName())
		m.setReason(err.Error())
		return err
	default:
	}
	m.mu.Lock()
	m.client = client
	m.shared = true
	m.reason = ""
	m.mu.Unlock()
	return nil
}

// Close 关闭通道：优雅关停 → 进程树守卫收尾 → 释放锁。
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	client := m.client
	shared := m.shared
	m.client = nil
	m.shared = false
	m.mu.Unlock()

	// 借来的进程归宿主所有：关停它会连带打断诊断池，且解锁会删掉我们从未持有的锁。
	if client != nil && !shared {
		client.Shutdown(ctx)
	}
	if !shared {
		m.releaseLock()
	}
	return nil
}

// ---- 锁（ADR-0002 §4.4） ----

func (m *Manager) acquireLock() error {
	path := m.LockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("knowledge/lsp: create lock dir: %w", err)
	}
	if raw, err := os.ReadFile(path); err == nil {
		var record LockRecord
		if json.Unmarshal(raw, &record) == nil && record.PID > 0 && record.PID != os.Getpid() && pidAlive(record.PID) {
			return fmt.Errorf("%w: pid=%d command=%s", ErrLockHeld, record.PID, record.Command)
		}
	}
	record := LockRecord{
		PID:       os.Getpid(),
		RootURI:   filepath.ToSlash(m.opts.Root),
		Command:   strings.TrimSpace(m.opts.Spec.Command),
		StartedAt: m.opts.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}

func (m *Manager) releaseLock() {
	path := m.LockPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var record LockRecord
	if json.Unmarshal(raw, &record) != nil || record.PID != os.Getpid() {
		return // 不是我们持有的锁：不得删除（可能已被接管）。
	}
	_ = os.Remove(path)
}

// ---- helpers ----

func (m *Manager) setReason(reason string) {
	m.mu.Lock()
	m.reason = reason
	m.mu.Unlock()
}

func (m *Manager) setReasonLocked(reason string) { m.reason = reason }

func normalizeRoot(root string) string {
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		abs = strings.TrimSpace(root)
	}
	return strings.ReplaceAll(abs, `\`, "/")
}

// pidAlive 报告进程是否存活（锁新鲜度判据；与 owner 仲裁同口径：先验 PID）。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return processAlive(proc)
}

// FormatLock 渲染锁记录（状态面/诊断用）。
func FormatLock(record LockRecord) string {
	return "pid=" + strconv.Itoa(record.PID) + " root=" + record.RootURI + " cmd=" + record.Command + " started=" + record.StartedAt
}
