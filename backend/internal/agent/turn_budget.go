package agent

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// PR-4 单轮预算（docs/plan/ui-event-bridge-drop-hardening.md §6.4）。
//
// 契约（单一事实源）：
//   - 三项预算都可独立缺省：0 / 负数表示该维度不设限；文本进度行只包含已配置维度，
//     避免出现 "step 12/-" 这类无法行动的噪声。
//   - Level 是**咨询性判决**：
//   - ok   ：全部已配置维度 < 80%；
//   - soft ：任一已配置维度 >= 80%（收尾水位，触发一次软着陆提示）；
//   - hard ：任一已配置维度 >= 100%（预算耗尽）。
//   - 硬边界的**执行归属**不变：
//   - steps 由循环条件 stepExceedsLimit 负责停止；
//   - wall_clock 由 MaxRunDuration 的 ctx 超时负责停止；
//   - tokens 由 remainingBudget 检查负责停止。
//     本类型的职责是让三者共享同一份用量/水位判定，供软着陆、结果字段与事件使用，
//     因此调用方必须用与执行路径相同的口径填充 TurnBudgetUsage，避免"提示 80%、
//     实际 95%"这类双口径漂移。
const (
	// TurnBudgetLevelOK 未达到任何水位。
	TurnBudgetLevelOK = "ok"
	// TurnBudgetLevelSoft 已达到收尾水位（>= TurnBudgetSoftRatio）。
	TurnBudgetLevelSoft = "soft"
	// TurnBudgetLevelHard 已耗尽预算（>= 100%）。
	TurnBudgetLevelHard = "hard"

	// 预算维度名（稳定上报字段值）。
	TurnBudgetDimensionSteps     = "steps"
	TurnBudgetDimensionWallClock = "wall_clock"
	TurnBudgetDimensionTokens    = "tokens"

	// TurnBudgetSoftRatio 收尾水位：达到即注入一次收尾指令，避免在硬边界处
	// 静默截断正在进行的工作。
	TurnBudgetSoftRatio = 0.8
)

// TurnBudgetSpec 单轮预算上限（0 / 负数 = 不设限）。
type TurnBudgetSpec struct {
	MaxSteps     int
	MaxWallClock time.Duration
	MaxTokens    int
}

// Empty 报告是否未配置任何预算维度。
func (s TurnBudgetSpec) Empty() bool {
	return NormalizeMaxSteps(s.MaxSteps) == 0 && s.MaxWallClock <= 0 && s.MaxTokens <= 0
}

// TurnBudgetUsage 单轮已用量（口径必须与执行路径一致）。
type TurnBudgetUsage struct {
	// CompletedSteps 已完成步数（不含正在执行的这一步）。
	CompletedSteps int
	// Elapsed 本轮已耗时。
	Elapsed time.Duration
	// TokensSpent 本轮已消耗 token（含压缩等旁路消耗）。
	TokensSpent int
}

// TurnBudgetState 是判定结果 + 可读进度行。
type TurnBudgetState struct {
	Level   string
	Reasons []string
	// HardReasons / SoftReasons 按维度拆分，便于调用方只针对 tokens 等特定维度行动。
	HardReasons []string
	SoftReasons []string
	// Ratio 是已配置维度中的最大水位（0-1+；未配置任何维度时为 0）。
	Ratio float64
	// Line 形如 "turn budget: step 240/300 · 32m/40m · tokens 62%"。
	Line       string
	StepsLine  string
	WallLine   string
	TokensLine string
}

// ReachedSoftLimit 报告是否已达到收尾水位。
func (s TurnBudgetState) ReachedSoftLimit() bool {
	return s.Level == TurnBudgetLevelSoft || s.Level == TurnBudgetLevelHard
}

// HardReason 报告某维度是否已耗尽预算。
func (s TurnBudgetState) HardReason(dimension string) bool {
	for _, reason := range s.HardReasons {
		if reason == dimension {
			return true
		}
	}
	return false
}

// EvaluateTurnBudget 计算预算水位。同一份 spec/usage 在任何调用点都应得到同一判决。
func EvaluateTurnBudget(spec TurnBudgetSpec, usage TurnBudgetUsage) TurnBudgetState {
	state := TurnBudgetState{Level: TurnBudgetLevelOK}
	segments := make([]string, 0, 3)
	reasons := make([]string, 0, 3)

	add := func(dimension string, ratio float64) {
		if ratio > state.Ratio {
			state.Ratio = ratio
		}
		switch {
		case ratio >= 1:
			state.HardReasons = append(state.HardReasons, dimension)
		case ratio >= TurnBudgetSoftRatio:
			state.SoftReasons = append(state.SoftReasons, dimension)
		}
	}

	if maxSteps := NormalizeMaxSteps(spec.MaxSteps); maxSteps > 0 {
		ratio := float64(usage.CompletedSteps) / float64(maxSteps)
		state.StepsLine = fmt.Sprintf("step %d/%d", usage.CompletedSteps, maxSteps)
		segments = append(segments, state.StepsLine)
		add(TurnBudgetDimensionSteps, ratio)
	}
	if spec.MaxWallClock > 0 {
		ratio := float64(usage.Elapsed) / float64(spec.MaxWallClock)
		state.WallLine = fmt.Sprintf("%s/%s", FormatTurnBudgetDuration(usage.Elapsed), FormatTurnBudgetDuration(spec.MaxWallClock))
		segments = append(segments, state.WallLine)
		add(TurnBudgetDimensionWallClock, ratio)
	}
	if spec.MaxTokens > 0 {
		ratio := float64(usage.TokensSpent) / float64(spec.MaxTokens)
		state.TokensLine = fmt.Sprintf("tokens %d%%", int(math.Round(ratio*100)))
		segments = append(segments, state.TokensLine)
		add(TurnBudgetDimensionTokens, ratio)
	}

	reasons = append(reasons, state.HardReasons...)
	reasons = append(reasons, state.SoftReasons...)
	state.Reasons = reasons
	switch {
	case len(state.HardReasons) > 0:
		state.Level = TurnBudgetLevelHard
	case len(state.SoftReasons) > 0:
		state.Level = TurnBudgetLevelSoft
	default:
		state.Level = TurnBudgetLevelOK
	}
	if len(segments) > 0 {
		state.Line = "turn budget: " + strings.Join(segments, " · ")
	}
	return state
}

// TokensSpentFromBudget 把 remaining-budget 计数换算成已消耗 token（clamp 到 >= 0）。
// 这是 token 维度与执行路径保持同口径的唯一入口：软着陆提示与实际停止条件都读它。
func TokensSpentFromBudget(maxTokens, remaining int) int {
	if maxTokens <= 0 {
		return 0
	}
	spent := maxTokens - remaining
	if spent < 0 {
		return 0
	}
	return spent
}

// FormatTurnBudgetDuration 把耗时渲染成紧凑形态（便于单行进度显示）：
// 45s / 32m / 1h20m。
func FormatTurnBudgetDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(math.Round(d.Seconds())))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if minutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, minutes)
}

// TurnBudgetSoftLandingMessage 是软着陆（收尾水位）注入给模型的指令。
// 目标：让模型主动收尾并留下可续跑的交接信息，而不是在硬边界被静默截断。
func TurnBudgetSoftLandingMessage(state TurnBudgetState) string {
	line := strings.TrimSpace(state.Line)
	if line == "" {
		line = "turn budget: unspecified"
	}
	return strings.Join([]string{
		fmt.Sprintf("Turn budget notice: this turn is at %d%% of its configured budget (%s).", int(math.Round(state.Ratio*100)), line),
		"Wrap up now instead of starting new exploration:",
		"1) finish or explicitly abandon the tool call currently in flight;",
		"2) persist any workspace/session findings that must survive this turn;",
		"3) reply with a short handoff: what is done, what is not, and the exact next action to resume.",
		"New long-running work will not fit in this turn.",
	}, "\n")
}

// TurnBudgetHardStopMessage 是 token 预算耗尽时的用户可见收尾文案。
func TurnBudgetHardStopMessage(state TurnBudgetState) string {
	line := strings.TrimSpace(state.Line)
	if line == "" {
		line = "turn budget: unspecified"
	}
	return fmt.Sprintf(
		"已达到本轮 token 预算上限（%s），当前轮次已优雅停止；已完成的工具结果与会话记录均已保留，可继续对话从现有会话续跑。",
		line,
	)
}
