// kinds.go 是通知语义登记表（docs/bots/schema-architecture 06 §1 D9：契约优先）：
// 新增通知 kind 必须先在此登记（kind ↔ 优先级 ↔ 默认受众），宿主与通道不得各自
// 发明词表。当前词表与 connectors 注意力通知（AttentionKind*）及 supervision 投影
// 对齐；"completed" 是 gateway 投影旧词表，一并登记以兼容。
package notify

import "strings"

// KindSpec 是单个 kind 的语义登记项。
type KindSpec struct {
	Kind            string
	Priority        Priority
	DefaultAudience string // "web" | "im" | "all"：默认受众提示（通道实现可再细分）
}

var kindSpecs = []KindSpec{
	{Kind: "approval", Priority: PriorityHigh, DefaultAudience: "all"},
	{Kind: "question", Priority: PriorityHigh, DefaultAudience: "all"},
	{Kind: "error", Priority: PriorityHigh, DefaultAudience: "all"},
	{Kind: "completion", Priority: PriorityNormal, DefaultAudience: "all"},
	{Kind: "completed", Priority: PriorityNormal, DefaultAudience: "all"}, // gateway 投影旧词表（同义）
	{Kind: "waiting", Priority: PriorityNormal, DefaultAudience: "all"},
}

// KnownKinds 返回登记表副本（顺序稳定）。
func KnownKinds() []KindSpec {
	return append([]KindSpec(nil), kindSpecs...)
}

// SpecOf 查询 kind 的登记项；未知 kind 返回 ok=false，调用方按 PriorityOf 保守处理。
func SpecOf(kind string) (KindSpec, bool) {
	kind = strings.TrimSpace(kind)
	for _, spec := range kindSpecs {
		if spec.Kind == kind {
			return spec, true
		}
	}
	return KindSpec{}, false
}
