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

// DegradeReason 返回最近一次降级原因（空串表示无降级）。
func (m *Manager) DegradeReason() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reason
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
	m.mu.Unlock()
	if closed || client == nil {
		return false
	}
	select {
	case <-client.Done():
		// 进程已退出（崩溃或被杀）：标记降级，等待下次 Ensure 重建。
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

// Close 关闭通道：优雅关停 → 进程树守卫收尾 → 释放锁。
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	client := m.client
	m.client = nil
	m.mu.Unlock()

	if client != nil {
		client.Shutdown(ctx)
	}
	m.releaseLock()
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
