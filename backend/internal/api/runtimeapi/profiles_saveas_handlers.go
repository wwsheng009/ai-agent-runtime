package runtimeapi

import (
	"context"
	stderrors "errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// ── from_session：会话生效面固化（save-as 差分固化，Batch 13 slice 6 / G1 / D24 / D35） ──
//
// 与 CLI 侧 `chat_profile_lifecycle_saveas.go` 的同构关系：同一份差分口径（只写与
// 内置默认面不同的声明）、同一份渲染核心（`profilesys.RenderSaveAsProfile`）、同一组
// "不可声明项"提示（D35）。server 侧只有一处差异，且如实登记：
//
//   - CLI 的生效面来自会话内的投影字段（`session.ToolPolicy` 等，启动/切换时投影）；
//   - server 没有这些投影字段，生效面一律来自**会话绑定 profile 的解析结果**
//     （`resolveProfileSessionState`，与 `buildSessionActor` / `set_profile` 同一路径）。
//     因此 from_session 的产物是"解析后生效面"的差分：把 profile 继承与 agentdef
//     overlay 展平为一份自包含声明，而不是复制原 profile 文件的字面内容。
//
// 无绑定 / 无差分时显式 400 且不生成任何文件（A9）；不提供 force（半覆盖比拒绝更糟）。

// runtimeProfileSaveAsResult 是一次固化的产物与报告字段。
type runtimeProfileSaveAsResult struct {
	files    map[string][]byte
	agent    string
	baseline string
	surface  profilesys.SaveAsSurface
	omitted  []string
}

// runtimeProfileSaveAsValidationError 标记"请求不合法 / 会话生效面无法固化"的错误
// （400）。会话不存在按 404 处理（与 apply 同一口径），其余按 500。
type runtimeProfileSaveAsValidationError struct{ message string }

func (e *runtimeProfileSaveAsValidationError) Error() string { return e.message }

func newRuntimeProfileSaveAsValidationError(format string, args ...interface{}) error {
	return &runtimeProfileSaveAsValidationError{message: fmt.Sprintf(format, args...)}
}

func isRuntimeProfileSaveAsValidationError(err error) bool {
	var target *runtimeProfileSaveAsValidationError
	return stderrors.As(err, &target)
}

// renderRuntimeProfileSaveAs 读取会话绑定 profile 的解析结果，折算差分面并渲染
// profile.yaml（不落盘：落盘由调用方在层根内完成，便于复用创建端点的目标校验）。
func (h *Handler) renderRuntimeProfileSaveAs(r *http.Request, sessionID, name, agentOverride string) (*runtimeProfileSaveAsResult, error) {
	if h == nil || h.sessionManager == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	sessionID = chat.NormalizeSessionID(sessionID)
	if sessionID == "" {
		return nil, newRuntimeProfileSaveAsValidationError("from_session 必填：固化的是某个会话的生效面")
	}
	ctx := context.Background()
	if r != nil && r.Context() != nil {
		ctx = r.Context()
	}
	// 会话存储与并发 aicli 进程共享，读锁等待必须有截止时间（与 apply 同一纪律）。
	getCtx, getCancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
	session, err := h.sessionManager.Get(getCtx, sessionID)
	getCancel()
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, newRuntimeProfileSaveAsValidationError("会话 %q 不存在", sessionID)
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = map[string]interface{}{}
	}
	meta := session.Metadata.Context

	ref := strings.TrimSpace(sessionmeta.String(meta, sessionmeta.ProfileRef))
	if ref == "" {
		// A9：无绑定 ⇒ 生效面就是内置默认面 ⇒ 无差分可固化，且不生成任何文件。
		return nil, newRuntimeProfileSaveAsValidationError(
			"当前无差异，无需固化：会话未绑定 profile（生效面等于内置默认面）；未生成任何文件")
	}
	workspacePath := sessionmeta.String(meta, sessionmeta.WorkspacePath)
	if worktreePath := sessionmeta.String(meta, toolbroker.AgentSessionContextWorktreePath); worktreePath != "" {
		workspacePath = worktreePath
	}
	agentID := strings.TrimSpace(sessionmeta.String(meta, sessionmeta.ProfileAgent))
	state, err := h.resolveProfileSessionState(ref, agentID, workspacePath)
	if err != nil {
		return nil, newRuntimeProfileSaveAsValidationError("会话绑定 profile %q 解析失败: %s", ref, err)
	}
	if state == nil || state.Resolved == nil {
		return nil, newRuntimeProfileSaveAsValidationError("会话绑定 profile %q 未解析出有效生效面", ref)
	}
	surface, err := buildRuntimeProfileSaveAsSurface(state)
	if err != nil {
		return nil, err
	}
	if surface.Empty() {
		return nil, newRuntimeProfileSaveAsValidationError(
			"当前无差异，无需固化：会话生效面与内置默认面一致（无工具/skills/MCP 收窄，read_only 未开启）；未生成任何文件")
	}
	agent := strings.TrimSpace(agentOverride)
	if agent == "" {
		agent = agentID
	}
	if agent == "" {
		agent = profilesys.TemplateDefaultAgent
	}
	content, err := profilesys.RenderSaveAsProfile(name, runtimeProfileSaveAsDescription(sessionID, ref), agent, surface)
	if err != nil {
		return nil, newRuntimeProfileSaveAsValidationError("%s", err.Error())
	}
	return &runtimeProfileSaveAsResult{
		files:    map[string][]byte{"profile.yaml": content},
		agent:    agent,
		baseline: ref,
		surface:  surface,
		omitted:  runtimeProfileSaveAsOmittedNotes(meta, state, surface),
	}, nil
}

// buildRuntimeProfileSaveAsSurface 把解析出的生效面折算为可声明差分。形态保持
// （allowlist 形态仍写 allowlist、deny 形态仍写 denylist）是可复现的前提：重开后
// 按同一形态解析，得到的允许集与固化时一致。
func buildRuntimeProfileSaveAsSurface(state *profileRuntimeState) (profilesys.SaveAsSurface, error) {
	if state == nil || state.Resolved == nil {
		return profilesys.SaveAsSurface{}, newRuntimeProfileSaveAsValidationError("会话绑定 profile 未解析出有效生效面")
	}
	surface := profilesys.SaveAsSurface{
		SkillAllowlist:    append([]string(nil), state.Resolved.Skills.Allowlist...),
		SkillDenylist:     append([]string(nil), state.Resolved.Skills.Denylist...),
		MCPUseServers:     append([]string(nil), state.Resolved.MCPSelection.UseServers...),
		MCPExcludeServers: append([]string(nil), state.Resolved.MCPSelection.ExcludeServers...),
	}
	if policy := state.ToolPolicy; policy != nil {
		surface.ReadOnly = policy.ReadOnly
		if policy.AllowlistEnabled {
			names := policy.AllowedToolNames()
			if len(names) == 0 {
				// 空 allowlist 会被 ValidateProfileSpec 判为非法（"不得变成隐式全禁"），
				// 与其写出一个解析不了的 profile，不如显式报错。
				return profilesys.SaveAsSurface{}, newRuntimeProfileSaveAsValidationError(
					"当前工具面是空 allowlist（全部禁用）：profile.yaml 无法表达该形态（空 allowlist 会被校验拒绝）；未生成 profile")
			}
			surface.ToolAllowlist = names
		} else {
			surface.ToolDenylist = runtimeProfileSaveAsToolNames(policy.DeniedTools)
		}
	}
	return surface, nil
}

// runtimeProfileSaveAsOmittedNotes 逐项明示"会话生效面里有、但 profile.yaml 无字段"的
// 内容（D35）：prompt（D24 明确不固化）、非默认权限模式、skills 目录。
func runtimeProfileSaveAsOmittedNotes(meta map[string]interface{}, state *profileRuntimeState, surface profilesys.SaveAsSurface) []string {
	notes := []string{
		"prompt（会话 prompt 可能来自临时上下文，D24 明确不固化）；需要时在 profile 里另加 prompt 文件",
	}
	mode := strings.TrimSpace(sessionmeta.String(meta, sessionmeta.EffectivePermissionMode))
	if mode == "" {
		mode = strings.TrimSpace(sessionmeta.String(meta, sessionmeta.PermissionMode))
	}
	if mode != "" && mode != string(runtimepolicy.ModeDefault) {
		notes = append(notes, fmt.Sprintf(
			"权限模式 %q（profile.yaml 无 permission_mode 字段，见 D35）；重开时用 --permission-mode 或会话控件指定", mode))
	}
	if len(surface.SkillAllowlist) > 0 || len(surface.SkillDenylist) > 0 {
		if state != nil && state.Resolved != nil && len(state.Resolved.SkillDirs) > 0 {
			notes = append(notes, "skills 目录（profile.yaml 无 dirs 字段；重开时按配置解析技能目录）")
		}
	}
	return notes
}

// runtimeProfileSaveAsDescription 生成 profile.description：说明来源与基线，便于
// `profile list/show` 一眼看出这不是模板生成物。
func runtimeProfileSaveAsDescription(sessionID, ref string) string {
	return fmt.Sprintf("从会话固化（save-as；会话 %s；基线 profile: %s；仅含差分声明）", sessionID, ref)
}

// runtimeProfileSaveAsSurfaceSummary 报告"写了哪些字段、各几项"（与 CLI 报告的
// 差分声明口径同构；read_only 是布尔声明，单独列出）。
func runtimeProfileSaveAsSurfaceSummary(surface profilesys.SaveAsSurface) map[string]interface{} {
	summary := map[string]interface{}{}
	add := func(path string, count int) {
		if count > 0 {
			summary[path] = count
		}
	}
	add("tools.allowlist", len(surface.ToolAllowlist))
	add("tools.denylist", len(surface.ToolDenylist))
	add("skills.allowlist", len(surface.SkillAllowlist))
	add("skills.denylist", len(surface.SkillDenylist))
	add("mcp.use_servers", len(surface.MCPUseServers))
	add("mcp.exclude_servers", len(surface.MCPExcludeServers))
	summary["tools.read_only"] = surface.ReadOnly
	return summary
}

// writeRuntimeProfileSaveAsFiles 物化固化产物：失败即清理本次新建的目录（失败路径
// 不留半成品；预存目录不动，与 CLI 的"失败即回滚"同一纪律）。
func writeRuntimeProfileSaveAsFiles(absRoot string, files map[string][]byte) ([]string, error) {
	preExisted := true
	if _, err := os.Stat(absRoot); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat profile root %s: %w", absRoot, err)
		}
		preExisted = false
	}
	relPaths := make([]string, 0, len(files))
	for rel := range files {
		relPaths = append(relPaths, rel)
	}
	sort.Strings(relPaths)
	for _, rel := range relPaths {
		target := filepath.Join(absRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			if !preExisted {
				_ = os.RemoveAll(absRoot)
			}
			return nil, err
		}
		if err := os.WriteFile(target, files[rel], 0o644); err != nil {
			if !preExisted {
				_ = os.RemoveAll(absRoot)
			}
			return nil, err
		}
	}
	return relPaths, nil
}

// runtimeProfileSaveAsToolNames 把策略里的工具名集合（map[string]bool）折算为升序列表。
func runtimeProfileSaveAsToolNames(flags map[string]bool) []string {
	names := make([]string, 0, len(flags))
	for name, enabled := range flags {
		if enabled && strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
