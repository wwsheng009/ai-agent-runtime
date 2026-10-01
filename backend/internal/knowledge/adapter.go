package knowledge

import (
	"context"
	"errors"
	"strings"
)

// AdapterCapabilities 声明一个 adapter 实际可提供的能力面（04 §5 Phase 4 交付 1）。
//
// 语义：false 表示该能力必须走降级路径，而不是"暂时失败"。核心流程不得因为
// 某个能力为 false 而失败（04 §4.1 原则 7 Degrade-Not-Fail；交付 5 离线模式）。
type AdapterCapabilities struct {
	// Definition 表示能给出符号定义位置（L2 语义级）。
	Definition bool `json:"definition"`
	// References 表示能给出符号引用位置。
	References bool `json:"references"`
	// Callers 表示能区分调用点（References 的子集能力）。
	Callers bool `json:"callers"`
	// Types 表示能给出类型关系（v1 无实现，占位为 false）。
	Types bool `json:"types"`
	// Tests 表示能识别测试关联（v1 无实现，占位为 false）。
	Tests bool `json:"tests"`
}

// Any 报告是否至少有一项能力可用。
func (c AdapterCapabilities) Any() bool {
	return c.Definition || c.References || c.Callers || c.Types || c.Tests
}

// LanguageAdapter 把一份文件内容翻译成符号与引用候选。
//
// 这是知识层唯一的"语言相关"接缝：tree-sitter、LSP、runtime evidence 各自实现
// 本接口后接入，索引流程（indexer.go）不需要任何改动（ADR-0007）。
type LanguageAdapter interface {
	// Name 是写入 refs.source 的生产者标识。
	Name() RefSource
	// Version 参与 signature_hash / stable_key 与工作区知识版本（04 §4.4）。
	// 版本变化必须能触发 full_rebuild_on_adapter_change。
	Version() string
	// Detect 判断该 adapter 是否处理此文件；head 是文件头部字节（可为空）。
	// 返回的语言 token 与 FileRecord.Language 同域（小写，如 "go"）。
	Detect(path string, head []byte) (string, bool)
	// Extract 解析文件；实现不得写入 store，也不得依赖 workspace 之外的输入。
	Extract(ctx context.Context, file FileRecord, content []byte) (Extraction, error)
	// Capabilities 声明能力面；索引路径只依赖 Extract。
	Capabilities() AdapterCapabilities
}

// Extraction 是一次解析的产物。
type Extraction struct {
	Symbols []Symbol
	Refs    []pendingRef
}

// pendingRef 是尚未解析目标的引用候选（目标解析见 indexer.resolveRefs）。
type pendingRef struct {
	Name         string
	Kind         RefKind
	Line         int
	Col          int
	Snippet      string
	FromSymbolID string
}

// AdapterKind 是配置可选的索引适配器（knowledge.adapter）。
type AdapterKind string

const (
	// AdapterBuiltin 是 v1 默认的零依赖 regex 解析器。
	AdapterBuiltin AdapterKind = "builtin"
	// AdapterTreeSitter 是可选的真解析器通道（v1 未接入语法，选择后降级）。
	AdapterTreeSitter AdapterKind = "treesitter"
	// AdapterLSP 是进程外语义通道（不承担索引抽取；语义查询见 SemanticAdapter）。
	AdapterLSP AdapterKind = "lsp"
)

// ErrInvalidAdapter 表示 knowledge.adapter 取值非法（加载期拒绝，fail closed）。
var ErrInvalidAdapter = errors.New("knowledge: invalid adapter")

// ErrAdapterUnavailable 表示选中的 adapter 在当前环境不可用（可选通道缺席）。
// 调用方必须降级，而不是把错误抛给 agent 循环。
var ErrAdapterUnavailable = errors.New("knowledge: adapter unavailable")

// ParseAdapterKind 解析 adapter 取值；空值等价 builtin（默认）。
func ParseAdapterKind(s string) (AdapterKind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(AdapterBuiltin), "default", "regex":
		return AdapterBuiltin, nil
	case string(AdapterTreeSitter), "tree-sitter":
		return AdapterTreeSitter, nil
	case string(AdapterLSP), "lsp-adapter":
		return AdapterLSP, nil
	default:
		return "", errors.New("knowledge: invalid adapter " + strconvQuote(s))
	}
}

// AdapterSelection 是一次 adapter 选择的结果。
//
// Degraded=true 时 Reason 必须非空：降级不得静默（ADR-0005 §4.3 的可观测原则）。
type AdapterSelection struct {
	Adapter   LanguageAdapter
	Requested AdapterKind
	Degraded  bool
	Reason    string
}

// SelectIndexAdapter 按配置选择索引 adapter。
//
// 选择规则（v1）：
//   - builtin（默认）：总是可用，零依赖；
//   - treesitter：可选实现，未接入语法时降级 builtin 并给出 Reason；
//   - lsp：进程外语义层，不承担索引抽取，降级 builtin 并给出 Reason。
//
// 任何情况下都返回可用的 adapter（Degrade-Not-Fail）。
func SelectIndexAdapter(cfg Config) AdapterSelection {
	kind, err := ParseAdapterKind(cfg.Adapter)
	if err != nil {
		kind = AdapterBuiltin
	}
	switch kind {
	case AdapterTreeSitter:
		if adapter := newTreeSitterAdapter(); adapter.Available() {
			return AdapterSelection{Adapter: adapter, Requested: kind}
		}
		return AdapterSelection{
			Adapter:   builtinAdapter{},
			Requested: kind,
			Degraded:  true,
			Reason:    "tree-sitter 语法未接入（可选实现），回退 builtin",
		}
	case AdapterLSP:
		return AdapterSelection{
			Adapter:   builtinAdapter{},
			Requested: kind,
			Degraded:  true,
			Reason:    "lsp 适配器不承担索引抽取（进程外语义层），回退 builtin",
		}
	default:
		return AdapterSelection{Adapter: builtinAdapter{}, Requested: AdapterBuiltin}
	}
}

// ---- 语义查询面（02 §44 SemanticAdapter） ----

// SemanticLocation 是一次语义查询（definition / references）的结果位置。
//
// 位置一律是 canonical 表示（ADR-0006 §4.1）：0-based 行号 + 行内 UTF-8 字节列。
type SemanticLocation struct {
	Path       string
	Line       int
	Col        int
	Kind       RefKind
	Snippet    string
	Confidence Confidence
	Source     RefSource
}

// SemanticAdapter 是"跨文件语义"查询的接缝（L2/L3 精度层）。
//
// 实现（v1 只有 lsp adapter）必须自行完成 ADR-0006 的边界转换：入参/出参都是
// canonical UTF-8 位置，LSP 协商编码只存在于实现内部。
type SemanticAdapter interface {
	// Name 是写入结果 Source 的生产者标识。
	Name() RefSource
	// Version 参与缓存键与 confidence 的 staleness 判定（ADR-0006 §4.7）。
	Version() string
	// Available 报告当前是否可用；false 时调用方必须降级，不得阻塞。
	Available() bool
	// Definition 返回符号定义位置。
	Definition(ctx context.Context, file string, line, col int) ([]SemanticLocation, error)
	// References 返回符号引用位置（含调用点）。
	References(ctx context.Context, file string, line, col int) ([]SemanticLocation, error)
}

// strconvQuote 避免为一处错误信息引入 strconv 依赖。
func strconvQuote(s string) string { return "\"" + s + "\"" }
