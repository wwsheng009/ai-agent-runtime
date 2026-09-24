package commands

import (
	"fmt"
	"os"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// chatProfileConfigOverlay 是 profile runtime.overrides 的会话级配置覆盖视图。
//
// 语义（设计 D13/D14）：
//   - 覆盖只在会话内生效：不写回配置文件、不改动全局配置单例、不影响其它会话；
//   - 合并基线是会话当前生效的 cfg（分层加载 + 系统预设已完成），复用
//     agentconfig.MergeConfigYAML 的键级合并语义；
//   - 未声明覆盖（nil/空）时 Active()=false，调用方必须原样保留基线，保证
//     零行为差异（NFR-1）；
//   - 合并结果会重新走一次 ValidateConfig，非法覆盖在会话生效面被拒绝，
//     而不是把坏配置带进运行时。
type chatProfileConfigOverlay struct {
	// Overrides 是解析期已校验（D14 白名单/拒绝域）的稀疏覆盖键树。
	Overrides map[string]interface{}
	// Keys 是覆盖叶子路径（点分隔、已排序），用于 TUI/doctor 展示与缓存失效判断。
	Keys []string
	// Origins 把每个覆盖叶子标记为 profile 来源（复用 origins 结构）。
	Origins map[string]string
}

// newChatProfileConfigOverlay 从解析结果构造覆盖视图；无覆盖时返回 nil，
// 让"未写 runtime.overrides"的 profile 完全不进入覆盖路径。
func newChatProfileConfigOverlay(resolved *profilesys.ResolvedAgent) *chatProfileConfigOverlay {
	if resolved == nil || len(resolved.Overrides) == 0 {
		return nil
	}
	overrides := profilesys.CloneOverrides(resolved.Overrides)
	if len(overrides) == 0 {
		return nil
	}
	return &chatProfileConfigOverlay{
		Overrides: overrides,
		Keys:      profilesys.OverrideKeyList(overrides),
		Origins:   profilesys.OverrideOrigins(overrides),
	}
}

// Active 报告该覆盖视图是否含有实际生效的覆盖键。
func (o *chatProfileConfigOverlay) Active() bool {
	return o != nil && len(o.Overrides) > 0
}

// Apply 返回叠加覆盖后的会话配置视图。
//
// 零变化（空映射 `{}` 覆盖、或覆盖值与基线完全一致）时返回 base 本身，
// 调用方据此保持与原行为逐字节一致；base 永不被就地修改。
func (o *chatProfileConfigOverlay) Apply(base *config.Config) (*config.Config, error) {
	if !o.Active() || base == nil {
		return base, nil
	}
	overlayYAML, err := profilesys.MergeOverridesIntoYAML(nil, o.Overrides)
	if err != nil {
		return nil, fmt.Errorf("profile runtime.overrides: %w", err)
	}
	merged, changed, err := config.ApplyConfigOverlayYAML(base, overlayYAML)
	if err != nil {
		return nil, fmt.Errorf("profile runtime.overrides: %w", err)
	}
	if !changed || merged == nil {
		return base, nil
	}
	return merged, nil
}

// applyProfileConfigOverlay 把覆盖视图投影到会话配置（D13 的单一生效点）。
//
// 基线捕获规则：会话配置未被叠加过时，当前 session.Config 就是基线；一旦叠加过，
// 后续热切换/解除都从同一基线重算，因此覆盖不会层层叠加。若叠加视图被外部路径
// （例如 /config reload 整体替换了会话配置）替换掉，则丢弃过期基线并以最新配置
// 重新作为基线，避免把旧配置装回去。
func applyProfileConfigOverlay(session *ChatSession, overlay *chatProfileConfigOverlay) error {
	if session == nil {
		return nil
	}
	if session.ProfileConfigOverlayApplied != nil && session.Config != session.ProfileConfigOverlayApplied {
		// 会话配置已被外部整体替换：旧基线作废。
		session.ProfileConfigBase = nil
		session.ProfileConfigOverlayApplied = nil
	}
	base := session.ProfileConfigBase
	if base == nil {
		base = session.Config
		session.ProfileConfigBase = base
	}
	if !overlay.Active() {
		restoreProfileConfigBase(session)
		return nil
	}
	if base == nil {
		return fmt.Errorf("profile runtime.overrides: 会话配置基线为空")
	}
	merged, err := overlay.Apply(base)
	if err != nil {
		return err
	}
	session.Config = merged
	session.ProfileConfigOverlayApplied = merged
	session.ProfileConfigOverlayKeys = append([]string(nil), overlay.Keys...)
	session.ProfileConfigOverlayOrigins = cloneStringMap(overlay.Origins)
	return nil
}

// detachProfileConfigOverlay 解除会话上的配置覆盖并还原基线（profile 解除绑定 /
// 切换到无覆盖的 profile 时调用）。零覆盖会话是空操作。
func detachProfileConfigOverlay(session *ChatSession) {
	if session == nil || session.ProfileConfigOverlayApplied == nil {
		return
	}
	restoreProfileConfigBase(session)
}

func restoreProfileConfigBase(session *ChatSession) {
	if session == nil {
		return
	}
	if session.ProfileConfigBase != nil {
		session.Config = session.ProfileConfigBase
	}
	session.ProfileConfigOverlayApplied = nil
	session.ProfileConfigOverlayKeys = nil
	session.ProfileConfigOverlayOrigins = nil
}

// profileResolutionConfig 返回解析 profile 时应使用的配置基线。覆盖视图只描述
// 当前 profile 的生效面，不应成为下一个 profile 的解析输入：否则
// aicli.mcp.config_file / profiles.* 之类的白名单覆盖会自我引用，热切换的结果
// 取决于上一个 profile（D19 要求切换结果只由新 profile 决定）。
func profileResolutionConfig(session *ChatSession) *config.Config {
	if session == nil {
		return nil
	}
	if session.ProfileConfigBase != nil {
		return session.ProfileConfigBase
	}
	return session.Config
}

// emitProfileConfigOverlayWarning 显式告警：覆盖未生效，会话继续按基线运行。
func emitProfileConfigOverlayWarning(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(newChatSystemOutputWriter(os.Stderr), "Warning: profile 配置覆盖未生效: %v\n", err)
}
