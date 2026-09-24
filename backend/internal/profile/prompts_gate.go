package profile

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
)

// D29（Batch 14）：项目级 profile 在**未信任工作区**的 prompts 分级门控。
//
// 门控是分级的（设计文档 §G6 D29 表）：tools / skills / mcp 裁剪声明、
// permission_mode 默认值、runtime.overrides 只会收窄生效面或本就受白名单约束
// → 正常生效；只有 **prompts**（自由文本，可整体替换 system prompt）在未信任时
// 必须扣留——否则 clone 一个仓库即可静默注入指令（V20/V21 核实：今日零门控）。
//
// 约定：
//   - 只门控**项目层** profile（profile root 位于工作区下，与 LayerRoot("project")
//     同源）；user 层（~/.aicli/profiles）永不受影响；
//   - 工作区信任结论由调用方提供（CLI = 进程级 currentFolderTrust()；
//     server = 按 workspacePath 现算），本包不做 I/O、不弹提示；
//   - 特性关闭（AICLI_FOLDER_TRUST 未开启）时 foldertrust 既有语义给出
//     Trusted=true，因此本门控在关闭态自然放行，不需要额外分支。

// ProjectPromptGate 是一次 D29 判定的结果。
type ProjectPromptGate struct {
	// Suppressed 为 true 表示该 profile 的 prompts 必须被扣留。
	Suppressed bool
	// Reason 是给用户看的一句话原因（Suppressed=false 时为空）。
	Reason string
}

// EvaluateProjectPromptGate 计算 D29 判定，不修改 resolved。
func EvaluateProjectPromptGate(resolved *ResolvedAgent, workspaceRoot string, workspaceTrusted bool) ProjectPromptGate {
	if resolved == nil || workspaceTrusted {
		return ProjectPromptGate{}
	}
	if !hasPromptContent(resolved.Prompts) {
		// 没有可扣留的内容：不制造"假警告"。
		return ProjectPromptGate{}
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		// 未信任 + 工作区根未知：无法证明是 user 层 → 失败关闭（安全门控的默认方向）。
		return ProjectPromptGate{Suppressed: true, Reason: promptSuppressionReason}
	}
	profileRoot := strings.TrimSpace(resolved.Paths.ProfileRoot)
	if profileRoot == "" {
		profileRoot = strings.TrimSpace(resolved.ProfileRoot)
	}
	if !foldertrust.IsProjectScopedPath(profileRoot, root) {
		return ProjectPromptGate{}
	}
	return ProjectPromptGate{Suppressed: true, Reason: promptSuppressionReason}
}

// ApplyProjectPromptGate 评估并落地 D29 门控：命中时清空 prompts 内容载体并在
// resolved 上记录扣留标记，返回是否扣留。
//
// 只清 `Prompts`（System/Role/Tools = 内容载体），**保留 `PromptMode` 声明**：
// mode 只表达 replace/append 语义，内容为空时两者均无副作用（消费侧
// LoadPromptText 返回空串，chat_session 的 profilePrompt != "" 守卫使空文本
// 不会替换基线提示）。
func ApplyProjectPromptGate(resolved *ResolvedAgent, workspaceRoot string, workspaceTrusted bool) bool {
	gate := EvaluateProjectPromptGate(resolved, workspaceRoot, workspaceTrusted)
	if !gate.Suppressed {
		return false
	}
	resolved.Prompts = ResolvedPromptFiles{}
	resolved.PromptSuppressed = true
	resolved.PromptSuppressionReason = gate.Reason
	return true
}

const promptSuppressionReason = "工作区未信任：项目级 profile 的 prompts 未应用（tools/skills/MCP 等收窄声明仍生效）"

// hasPromptContent 判断 resolved 是否携带可注入的 prompt 内容载体。
func hasPromptContent(p ResolvedPromptFiles) bool {
	return strings.TrimSpace(p.System) != "" ||
		strings.TrimSpace(p.Role) != "" ||
		strings.TrimSpace(p.Tools) != ""
}
