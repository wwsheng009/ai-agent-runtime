package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// chat_profile_resume.go 实现 A4 的 resume 半程（Batch 11a）：把持久化的 profile
// 身份读回会话，并按该引用重新解析/投影生效面。
//
// 读方向与写方向对称：`syncChatRuntimeContext`（chat_team_binding.go:318-322）把
// `session.ProfileReference` 写进 sessionmeta 的 profile_ref；resume 路径必须读回来，
// 否则 ① `/profile status` 显示"无 profile 基线"（展示缺失）；② 下一次 sync 会因为
// 内存字段为空而**删除** sessionmeta 里的 profile_ref（绑定静默丢失）；③ 会话以基线
// （更宽）工具面运行，profile 声明的收窄失效（FR-9/NFR-3）。
//
// 优先级口径（与 D30/A5 同一精神）：本次启动显式 `--profile` > 配置 default_profile >
// 会话内持久化绑定。启动路径的解析结果在 restore 之后投影（chat_setup.go:248-254），
// 因此这里只处理"本次没有任何显式/默认选择"的恢复场景。

// hydrateChatProfileIdentityFromResumedSession 从运行时会话元数据回填 profile 身份
// 字段（引用/名称/agent/根目录），返回回填的引用；无持久化绑定或会话已有绑定时返回 ""。
func hydrateChatProfileIdentityFromResumedSession(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return ""
	}
	// 已有绑定（本次启动解析出的显式/默认 profile）不覆盖。
	if strings.TrimSpace(session.ProfileReference) != "" || strings.TrimSpace(session.ProfileName) != "" {
		return ""
	}
	ctx := session.RuntimeSession.Metadata.Context
	if ctx == nil {
		return ""
	}
	ref := strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.ProfileRef))
	if ref == "" {
		return ""
	}
	session.ProfileReference = ref
	session.ProfileName = strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.ProfileName))
	session.ProfileAgent = strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.ProfileAgent))
	session.ProfileRoot = strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.ProfileRoot))
	return ref
}

// chatReapplyResumedProfileState 按持久化引用重新解析并投影生效面，保证 prompt/tools/
// skills/mcp 与身份一致（不谎报：不会出现"状态栏说 profile X、实际按基线跑"）。
//
// 解析失败（profile 被删除/磁盘编辑出错）时保留身份字段并提示 `/profile reload`：
// R18 口径——失败不改动会话状态，绑定不丢、可恢复；同时保证下一次 sync 不会误删
// sessionmeta 中的绑定。
func chatReapplyResumedProfileState(session *ChatSession, ref string) {
	ref = strings.TrimSpace(ref)
	if session == nil || ref == "" {
		return
	}
	state, err := resolveChatProfileState(session.Config, &chatCommandOptions{ProfileFlag: ref})
	if err != nil || state == nil || !state.Active() {
		reason := "profile 未解析出可用 agent"
		if err != nil {
			reason = err.Error()
		}
		fmt.Fprintf(newChatSystemOutputWriter(os.Stderr),
			"Warning: 恢复会话的 profile %q 解析失败: %s；本会话暂按基线运行，修复后用 /profile reload\n", ref, reason)
		return
	}
	applyProfileStateToChatSession(session, state)
}
