package runtimeapi

// 会话工作区锚定的 MCP 配置（runtime-server）
//
// 背景：进程级 MCP 解析锚定在 runtime-server 的启动目录，无法跟随会话的
// workspace 目录；而 aicli TUI 在用户于工作区目录启动时天然享受
// `./.aicli/mcp.yaml` 优先级。本文件把「按会话工作区锚定」的能力注入
// runtime-server，并与 TUI 共用同一内核（aiclipaths/mcp config 的 From 入口）。
//
// 语义与边界：
//   - 会话工作区链解析结果与进程级相同时**不建新实例**（避免同时存在多份配置）；
//   - 显式覆盖（aicli.mcp.config_file 的真实覆盖）对任何工作区都生效，
//     解析结果因此与进程级一致，同样不会分裂出第二份配置；
//   - 未绑定工作区的会话继续使用进程级 manager；
//   - 会话级 enable/disable 覆盖仍在最外层过滤（sessionScopedMCPSurface），
//     与本层的 workspace manager 组合而不是互斥。
//
// 生命周期：按解析出的生效文件缓存（LRU，默认 8 个），淘汰与关闭时停止
// manager；服务关闭调用 CloseWorkspaceMCPSupport。

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// WorkspaceMCPSupportConfig 由 runtime-server 装配：把「按工作区锚定的分层解析」
// 注入 handler。零值（未注入）时所有会话保持既有进程级行为。
type WorkspaceMCPSupportConfig struct {
	// ExplicitOverride 是 aicli.mcp.config_file 的原始值（可为空）。
	ExplicitOverride string
	// GlobalPath 是进程级解析出的生效路径（可为空）；与之相同的解析结果复用全局 manager。
	GlobalPath string
	// Resolve 以 workspace 目录为锚点解析发现链；缺省用 aiclipaths 共享实现。
	Resolve func(baseDir string) aiclipaths.MCPConfigResolution
	// NewManager 构造会话级 manager；缺省用 manager.NewManager()。
	NewManager func() manager.Manager
	// Wrap 把底层 manager 包装为工具面适配器（与全局 mcpAdapter 同路径）。
	// 缺省用 tools.NewAgentAdapter（nil runtimeConfig，仅测试/兜底场景）。
	Wrap func(manager.Manager) skill.MCPManager
	// MaxEntries 限制缓存的工作区 manager 数量；<=0 时用 8。
	MaxEntries int
}

type workspaceMCPEntry struct {
	manager manager.Manager  // 底层 manager（停止连接用）
	adapter skill.MCPManager // 工具面适配器（返回给调用方）
	closer  io.Closer        // 适配器附带资源（LSP 池等），可为 nil
	lastUse time.Time
}

// workspaceMCPManagers 按「解析出的生效文件」缓存会话级 manager。
type workspaceMCPManagers struct {
	mu      sync.Mutex
	ctx     context.Context
	cfg     WorkspaceMCPSupportConfig
	entries map[string]*workspaceMCPEntry
	closed  bool
	// prepare 在适配器首次构建后调用一次（例如 h.wireLSPObservation）。
	prepare func(skill.MCPManager)
}

func newWorkspaceMCPManagers(cfg WorkspaceMCPSupportConfig) *workspaceMCPManagers {
	if cfg.Resolve == nil {
		explicit := cfg.ExplicitOverride
		cfg.Resolve = func(baseDir string) aiclipaths.MCPConfigResolution {
			return aiclipaths.ResolveMCPConfigPathDetailedFrom(baseDir, explicit)
		}
	}
	if cfg.NewManager == nil {
		cfg.NewManager = func() manager.Manager { return manager.NewManager() }
	}
	if cfg.Wrap == nil {
		cfg.Wrap = func(m manager.Manager) skill.MCPManager {
			return runtimetools.NewAgentAdapter(runtimetools.NewDefaultManagerWithRuntimeConfig(m, nil))
		}
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 8
	}
	return &workspaceMCPManagers{
		ctx:     context.Background(),
		cfg:     cfg,
		entries: make(map[string]*workspaceMCPEntry),
	}
}

// workspaceMCPKey 生成缓存键：Windows 下大小写不敏感（文件系统语义）。
func workspaceMCPKey(path string) string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

// resolve 以 base 为锚点解析发现链（供 profile 回退路径复用同一份解析结果）。
func (s *workspaceMCPManagers) resolve(base string) aiclipaths.MCPConfigResolution {
	if s == nil {
		return aiclipaths.MCPConfigResolution{}
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return aiclipaths.MCPConfigResolution{}
	}
	return s.cfg.Resolve(base)
}

// acquire 返回该工作区应使用的 manager；空工作区 / 解析为空 / 与进程级解析相同
// 时返回 nil（调用方回退全局 manager，避免配置分裂）。
func (s *workspaceMCPManagers) acquire(workspacePath string) skill.MCPManager {
	entry := s.acquireEntry(workspacePath)
	if entry == nil {
		return nil
	}
	return entry.adapter
}

// acquireEntry 语义与 acquire 相同，额外返回缓存条目本身（含底层 manager），
// 供「会话级管理工作区配置文件」复用同一实例（写后重载即作用于会话工具面）。
func (s *workspaceMCPManagers) acquireEntry(workspacePath string) *workspaceMCPEntry {
	if s == nil {
		return nil
	}
	base := strings.TrimSpace(workspacePath)
	if base == "" {
		return nil
	}
	resolution := s.resolve(base)
	resolvedPath := strings.TrimSpace(resolution.Path)
	if resolvedPath == "" {
		return nil
	}
	if samePath(resolvedPath, s.cfg.GlobalPath) {
		return nil
	}
	key := workspaceMCPKey(resolvedPath)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if entry, ok := s.entries[key]; ok {
		entry.lastUse = time.Now()
		s.mu.Unlock()
		return entry
	}
	s.mu.Unlock()

	// 加载/建连在锁外进行，避免单个工作区的慢建连阻塞其他会话；
	// 竞态下可能重复构造，锁内二次检查后停掉多余的实例（不会泄漏连接）。
	created, err := s.build(base)
	if err != nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		closeWorkspaceMCPEntry(created)
		return nil
	}
	if entry, ok := s.entries[key]; ok {
		entry.lastUse = time.Now()
		s.mu.Unlock()
		closeWorkspaceMCPEntry(created)
		return entry
	}
	s.entries[key] = created
	evicted := s.evictLocked()
	s.mu.Unlock()

	for _, entry := range evicted {
		closeWorkspaceMCPEntry(entry)
	}
	return created
}

func (s *workspaceMCPManagers) build(base string) (*workspaceMCPEntry, error) {
	created := s.cfg.NewManager()
	if created == nil {
		return nil, fmt.Errorf("workspace MCP manager factory returned nil")
	}
	loader, ok := created.(manager.LayeredConfigLoaderFrom)
	if !ok || loader == nil {
		_ = created.Stop()
		return nil, fmt.Errorf("MCP manager does not support anchored layered loading")
	}
	if err := loader.LoadConfigEffectiveFrom(base, s.cfg.ExplicitOverride); err != nil {
		_ = created.Stop()
		return nil, err
	}
	if async, ok := created.(manager.AsyncManager); ok && async != nil {
		if err := async.StartAsync(s.ctx); err != nil {
			_ = created.Stop()
			return nil, err
		}
	} else if err := created.Start(s.ctx); err != nil {
		_ = created.Stop()
		return nil, err
	}
	adapter := s.cfg.Wrap(created)
	if adapter == nil {
		_ = created.Stop()
		return nil, fmt.Errorf("workspace MCP wrapper returned nil")
	}
	if s.prepare != nil {
		s.prepare(adapter)
	}
	entry := &workspaceMCPEntry{manager: created, adapter: adapter, lastUse: time.Now()}
	if closer, ok := adapter.(io.Closer); ok {
		entry.closer = closer
	}
	return entry, nil
}

// closeWorkspaceMCPEntry 先释放适配器附带资源（LSP 池），再停止 MCP 连接。
func closeWorkspaceMCPEntry(entry *workspaceMCPEntry) {
	if entry == nil {
		return
	}
	if entry.closer != nil {
		_ = entry.closer.Close()
	}
	if entry.manager != nil {
		_ = entry.manager.Stop()
	}
}

// evictLocked 按 LRU 淘汰超限条目；调用方需持有写锁。
func (s *workspaceMCPManagers) evictLocked() []*workspaceMCPEntry {
	var evicted []*workspaceMCPEntry
	for len(s.entries) > s.cfg.MaxEntries {
		var oldestKey string
		var oldest time.Time
		first := true
		for key, entry := range s.entries {
			if first || entry.lastUse.Before(oldest) {
				oldestKey = key
				oldest = entry.lastUse
				first = false
			}
		}
		if oldestKey == "" {
			break
		}
		evicted = append(evicted, s.entries[oldestKey])
		delete(s.entries, oldestKey)
	}
	return evicted
}

// Close 停止全部会话级 manager（服务关闭时调用；幂等）。
func (s *workspaceMCPManagers) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	entries := make([]*workspaceMCPEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry != nil {
			entries = append(entries, entry)
		}
	}
	s.entries = make(map[string]*workspaceMCPEntry)
	s.mu.Unlock()

	for _, entry := range entries {
		closeWorkspaceMCPEntry(entry)
	}
}

// SetWorkspaceMCPSupport 注入「按会话工作区锚定」的 MCP 能力（runtime-server 装配）。
// 未注入时所有会话继续使用进程级 manager（测试/CLI 内嵌场景零变化）。
func (h *Handler) SetWorkspaceMCPSupport(cfg WorkspaceMCPSupportConfig) {
	if h == nil {
		return
	}
	h.workspaceMCP = newWorkspaceMCPManagers(cfg)
	wire := h.wireLSPObservation
	h.workspaceMCP.prepare = func(surface skill.MCPManager) {
		_ = wire(surface)
	}
}

// CloseWorkspaceMCPSupport 停止全部会话级 workspace manager。
func (h *Handler) CloseWorkspaceMCPSupport() {
	if h == nil || h.workspaceMCP == nil {
		return
	}
	h.workspaceMCP.Close()
}

// sessionWorkspaceDir 返回会话绑定的工作区目录（worktree 优先）；空串表示未绑定。
func sessionWorkspaceDir(session *chat.Session) string {
	if session == nil {
		return ""
	}
	workspace := sessionmeta.String(session.Metadata.Context, sessionmeta.WorkspacePath)
	if worktreePath := sessionmeta.String(session.Metadata.Context, toolbroker.AgentSessionContextWorktreePath); strings.TrimSpace(worktreePath) != "" {
		workspace = worktreePath
	}
	return strings.TrimSpace(workspace)
}

// sessionBaseMCPManager 解析会话应使用的「基础 manager」：工作区链命中且不同于
// 进程级解析时用会话级 manager，否则回退全局。会话级启停覆盖由调用方再叠加。
func (h *Handler) sessionBaseMCPManager(ctx context.Context, session *chat.Session) skill.MCPManager {
	if h == nil {
		return nil
	}
	if h.workspaceMCP == nil {
		return h.mcpManager
	}
	if base := sessionWorkspaceDir(session); base != "" {
		if manager := h.workspaceMCP.acquire(base); manager != nil {
			return manager
		}
	}
	return h.mcpManager
}

// sessionBaseMCPManagerByID 同 sessionBaseMCPManager，按会话 id 读取工作区。
func (h *Handler) sessionBaseMCPManagerByID(ctx context.Context, sessionID string) skill.MCPManager {
	if h == nil {
		return nil
	}
	if h.workspaceMCP == nil {
		return h.mcpManager
	}
	session := h.sessionByID(ctx, sessionID)
	if session == nil {
		return h.mcpManager
	}
	return h.sessionBaseMCPManager(ctx, session)
}

// sessionMCPFallback 返回该会话的「全局回退」（profile 未自带 mcp.yaml 时的兜底）：
// 工作区锚定解析命中且不同于进程级时返回（路径, 会话级 manager），否则进程级。
func (h *Handler) sessionMCPFallback(workspacePath string) (string, skill.MCPManager) {
	if h == nil {
		return "", nil
	}
	fallbackPath := strings.TrimSpace(h.profileGlobalMCPPath)
	fallbackManager := h.mcpManager
	if h.workspaceMCP == nil {
		return fallbackPath, fallbackManager
	}
	base := strings.TrimSpace(workspacePath)
	if base == "" {
		return fallbackPath, fallbackManager
	}
	resolution := h.workspaceMCP.resolve(base)
	resolvedPath := strings.TrimSpace(resolution.Path)
	if resolvedPath == "" || samePath(resolvedPath, h.profileGlobalMCPPath) {
		return fallbackPath, fallbackManager
	}
	if manager := h.workspaceMCP.acquire(base); manager != nil {
		return resolvedPath, manager
	}
	return fallbackPath, fallbackManager
}
