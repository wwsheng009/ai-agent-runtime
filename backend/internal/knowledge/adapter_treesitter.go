package knowledge

import "context"

// treeSitterAdapter 是可选的真解析器通道（04 §5 Phase 4 交付 2）。
//
// v1 未接入语法（不引入 cgo/语法依赖），因此 Available()=false：
// SelectIndexAdapter 会降级 builtin 并给出可观测的 Reason。保留类型与能力
// 声明，使能力矩阵测试能钉住"可选通道缺席时索引仍可用"，也为后续接入留出
// 唯一的扩展点（ADR-0007：接入时只替换 LanguageAdapter）。
type treeSitterAdapter struct{}

// newTreeSitterAdapter 返回 tree-sitter 通道实例。
func newTreeSitterAdapter() treeSitterAdapter { return treeSitterAdapter{} }

// Available 报告语法是否已接入；v1 恒 false。
func (treeSitterAdapter) Available() bool { return false }

// Name 实现 LanguageAdapter：tree-sitter 产出 confidence=syntax 的行。
func (treeSitterAdapter) Name() RefSource { return SourceTreeSitter }

// Version 实现 LanguageAdapter；未接入时版本不参与任何身份计算
// （选择阶段已降级，Version 不会被用于 signature_hash）。
func (treeSitterAdapter) Version() string { return "treesitter/unavailable" }

// Detect 实现 LanguageAdapter；未接入时不做任何声明。
func (treeSitterAdapter) Detect(string, []byte) (string, bool) { return "", false }

// Extract 实现 LanguageAdapter；未接入时显式失败，绝不返回半成品。
func (treeSitterAdapter) Extract(context.Context, FileRecord, []byte) (Extraction, error) {
	return Extraction{}, ErrAdapterUnavailable
}

// Capabilities 实现 LanguageAdapter；未接入 → 全 false（不得假装可用）。
func (treeSitterAdapter) Capabilities() AdapterCapabilities { return AdapterCapabilities{} }
