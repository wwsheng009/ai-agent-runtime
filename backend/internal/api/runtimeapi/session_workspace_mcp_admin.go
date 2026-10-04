package runtimeapi

// 会话级（工作区锚定）MCP 管理：把「会话实际加载的配置文件」暴露为可读写目标。
//
// 背景：会话工具面已经按工作区锚定加载（session_workspace_mcp.go），但会话 API
// 只返回会话级覆盖清单（disabled），面板看不到、也改不了工作区的 mcp.yaml。
// 本文件补齐「读目标 + 写目标」：
//   - 读目标（列表/状态）：工作区链命中且不同于进程级 → 工作区文件；
//     未绑定工作区或工作区没有配置文件 → 回退进程级（会话真实生效语义）。
//   - 写目标（新增/编辑/启停/删除）：工作区锚定注入后优先写入工作区文件；
//     工作区还没有配置文件时写 <workspace>/.aicli/mcp.yaml（首个 server 落盘后，
//     下一次解析即命中该文件，工具面自动切换到工作区 manager）。
//
// 复用 internal/mcp/admin 服务：写路径与热重载语义与全局管理完全一致，
// 区别只是 manager 来自工作区缓存（避免为同一份配置起第二套连接）。

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
)

// sessionMCPAdminScope 会话 MCP 管理目标（读/写分离，只读解析，不建连）。
type sessionMCPAdminScope struct {
	SessionID string
	Workspace string
	// 读目标：会话实际生效的文件（列表/诊断都锚定它）。
	ReadPath   string
	ReadSource string
	ReadExists bool
	// 写目标：新增/编辑/启停/删除落盘的文件。
	WritePath   string
	WriteSource string
	WriteExists bool
	// WorkspaceScoped 表示写目标是会话工作区锚定的配置文件。
	WorkspaceScoped bool
	// WorkspaceFallback 表示当前读目标回退到了进程级（工作区还没有配置文件）。
	WorkspaceFallback bool
	// Resolution 是工作区锚点的完整解析（候选/来源），供面板展示。
	Resolution aiclipaths.MCPConfigResolution
}

// fileExists 报告路径是否为存在的普通文件。
func fileExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// sameAbsPath 比较两个路径是否指向同一文件（相对路径按进程 cwd 归一化）。
func sameAbsPath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	if absolute, err := filepath.Abs(left); err == nil {
		left = absolute
	}
	if absolute, err := filepath.Abs(right); err == nil {
		right = absolute
	}
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

// sessionMCPAdminScopeFor 计算会话的 MCP 管理目标（nil 安全，降级为进程级）。
func (h *Handler) sessionMCPAdminScopeFor(session *chat.Session) *sessionMCPAdminScope {
	scope := &sessionMCPAdminScope{}
	if h == nil || session == nil {
		return scope
	}
	scope.SessionID = strings.TrimSpace(session.ID)
	scope.Workspace = sessionWorkspaceDir(session)

	globalPath := strings.TrimSpace(h.profileGlobalMCPPath)
	if h.workspaceMCP != nil && strings.TrimSpace(h.workspaceMCP.cfg.GlobalPath) != "" {
		globalPath = strings.TrimSpace(h.workspaceMCP.cfg.GlobalPath)
	}
	scope.ReadPath, scope.ReadSource, scope.ReadExists = globalPath, "process", fileExists(globalPath)

	if h.workspaceMCP == nil || scope.Workspace == "" {
		scope.WritePath, scope.WriteSource, scope.WriteExists = scope.ReadPath, scope.ReadSource, scope.ReadExists
		return scope
	}

	resolution := h.workspaceMCP.resolve(scope.Workspace)
	scope.Resolution = resolution
	winner := strings.TrimSpace(resolution.Path)
	projectTarget := filepath.Join(scope.Workspace, ".aicli", aiclipaths.DefaultMCPConfigFileName)

	switch {
	case winner != "" && !sameAbsPath(winner, globalPath):
		// 工作区链命中独立文件：读写都锚定它（工作区 manager 缓存的同一实例）。
		scope.ReadPath, scope.ReadSource, scope.ReadExists = winner, resolution.Source, fileExists(winner)
		scope.WritePath, scope.WriteSource, scope.WriteExists = scope.ReadPath, scope.ReadSource, scope.ReadExists
		scope.WorkspaceScoped = true
	case winner != "":
		// 解析结果与进程级是同一文件：直接复用全局管理面（不产生第二份配置）。
		scope.WritePath, scope.WriteSource, scope.WriteExists = scope.ReadPath, scope.ReadSource, scope.ReadExists
	default:
		// 工作区没有配置文件：读回退进程级（会话当前实际生效），写落到工作区 project 层，
		// 首个 server 落盘后解析即命中该文件。
		scope.WritePath, scope.WriteSource, scope.WriteExists = projectTarget, "project", false
		scope.WorkspaceScoped = true
		scope.WorkspaceFallback = true
	}
	return scope
}

// payload 生成 API 响应中的 scope 块。
func (s *sessionMCPAdminScope) payload() map[string]interface{} {
	if s == nil {
		return map[string]interface{}{}
	}
	read := map[string]interface{}{
		"path":   s.ReadPath,
		"source": s.ReadSource,
		"exists": s.ReadExists,
	}
	write := map[string]interface{}{
		"path":   s.WritePath,
		"source": s.WriteSource,
		"exists": s.WriteExists,
	}
	out := map[string]interface{}{
		"workspace":          s.Workspace,
		"workspace_scoped":   s.WorkspaceScoped,
		"workspace_fallback": s.WorkspaceFallback,
		"read":               read,
		"write":              write,
	}
	if len(s.Resolution.Candidates) > 0 {
		out["candidates"] = s.Resolution.Candidates
	}
	return out
}

// diagnostics 生成管理面诊断（生效文件 + 候选），无解析信息时用读目标兜底。
func (s *sessionMCPAdminScope) diagnostics() *mcpadmin.ConfigDiagnostics {
	if s == nil {
		return nil
	}
	resolution := s.Resolution
	if strings.TrimSpace(resolution.Path) == "" {
		if strings.TrimSpace(s.ReadPath) == "" {
			return nil
		}
		resolution = aiclipaths.MCPConfigResolution{Path: s.ReadPath, Source: s.ReadSource}
	}
	diagnostics := mcpadmin.ConfigDiagnosticsFromResolution(resolution)
	return &diagnostics
}

// sessionMCPAdminService 构造 scope 对应的管理服务。
//
// write=false 时锚定读目标（列表/状态），write=true 时锚定写目标（变更）。
// 工作区目标复用会话级 manager（写后 ReloadConfig 直接作用于会话工具面）；
// 工作区还没有 manager（首个 server 尚未落盘）时用纯写模式，由调用方失效
// 工具面缓存，下一个 turn 边界再按新文件懒加载。
func (h *Handler) sessionMCPAdminService(scope *sessionMCPAdminScope, write bool) mcpadmin.AdminService {
	if h == nil || scope == nil {
		return nil
	}
	path := scope.ReadPath
	workspaceTarget := scope.WorkspaceScoped && !scope.WorkspaceFallback
	if write {
		path = scope.WritePath
		workspaceTarget = scope.WorkspaceScoped
	}
	if !workspaceTarget {
		return h.runtimeMCPAdminService()
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}

	options := make([]mcpadmin.Option, 0, 3)
	reused := false
	if h.workspaceMCP != nil {
		if entry := h.workspaceMCP.acquireEntry(scope.Workspace); entry != nil && entry.manager != nil {
			options = append(options, mcpadmin.WithManager(entry.manager))
			reused = true
		}
	}
	if !reused {
		// 尚无工作区 manager：只落盘，不尝试重载（工具面在下一 turn 边界重建）。
		options = append(options, mcpadmin.WithApplyOnMutate(false))
	}
	if diagnostics := scope.diagnostics(); diagnostics != nil {
		options = append(options, mcpadmin.WithConfigDiagnostics(*diagnostics))
	}
	return mcpadmin.NewService(path, options...)
}

// sessionEffectiveMCPServerState 报告 server 在「会话生效配置」中的存在性与启用
// 状态：先查工作区文件，找不到再回退全局状态（检查清单，不建连）。
func (h *Handler) sessionEffectiveMCPServerState(ctx context.Context, session *chat.Session, name string) (bool, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, false
	}
	if scope := h.sessionMCPAdminScopeFor(session); scope != nil && scope.WorkspaceScoped && !scope.WorkspaceFallback {
		if service := h.sessionMCPAdminService(scope, false); service != nil {
			if ctx == nil {
				ctx = context.Background()
			}
			queryCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
			defer cancel()
			if cfg, err := service.Get(queryCtx, name); err == nil && cfg != nil {
				enabled := cfg.Enabled
				if cfg.Disabled {
					enabled = false
				}
				return true, enabled
			}
		}
	}
	return h.runtimeMCPServerState(ctx, name)
}
