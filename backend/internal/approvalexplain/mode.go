package approvalexplain

import "strings"

// Mode 是审批解释的生成策略（§4.13）。
//
//   - ModeOff：只用规则摘要，永不调用模型；
//   - ModeOnDemand（默认）：用户点「解释」才调用一次模型；
//   - ModePreGenerate：审批一在 Web 端露面就后台预生成（仅 runtime-server 有
//     该读路径；本地模式无预热通路，宿主按 on_demand 处理）。
type Mode string

const (
	ModeOff         Mode = "off"
	ModeOnDemand    Mode = "on_demand"
	ModePreGenerate Mode = "pre_generate"
)

// ParseMode 解析模式取值；空串表示默认（on_demand），大小写与常见别名均可。
// 未知取值返回 (ModeOnDemand, false)，由调用方决定是报错还是保持默认。
//
// 这是 runtimeapi.ParseApprovalExplainMode 与 aicli 本地门控共用的唯一解析实现，
// 保证两端对 AICLI_APPROVAL_EXPLAIN_MODE 的理解完全一致。
func ParseMode(raw string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on_demand", "on-demand", "ondemand", "on":
		return ModeOnDemand, true
	case "off", "none", "disabled", "disable":
		return ModeOff, true
	case "pre_generate", "pre-generate", "pregenerate", "pre":
		return ModePreGenerate, true
	}
	return ModeOnDemand, false
}
