package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// ADR-0004 工具层验收（§4.1 分级 + §4.2 信封三字段 + §5 硬约束）：
//   - 分级函数边界（校准后的 900s / 7200s，== 视为通过）；
//   - 信封三字段在 index / partial / fallback 三种形态下都必须显式出现；
//   - 运行时陈旧守卫：关系类非全开档、定义类过旧档一律不返回索引结果；
//   - 逃生舱 stale_reader=off 的 tier 语义（resolver 侧计算，工具侧放行）。

func TestCodeTierForSnapshotBoundaries(t *testing.T) {
	cases := []struct {
		name           string
		writer         bool
		snapshotTS     int64
		staleness      int64
		gradingEnabled bool
		want           string
	}{
		{"writer 全开（即使快照很旧）", true, 100, 100000, true, CodeIndexTierAll},
		{"逃生舱关闭分级 → 按 writer 策略全开", false, 100, 100000, false, CodeIndexTierAll},
		{"reader S=7200 视为新鲜（边界通过）", false, 100, 7200, true, CodeIndexTierAll},
		{"reader S=7201 → 仅定义类", false, 100, 7201, true, CodeIndexTierDefinitions},
		{"reader S=28800 视为中等上限（边界通过）", false, 100, 28800, true, CodeIndexTierDefinitions},
		{"reader S=28801 → 不注册", false, 100, 28801, true, CodeIndexTierNone},
		{"从未成功索引（snapshot=0）→ 不注册", false, 0, 0, true, CodeIndexTierNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CodeTierForSnapshot(tc.writer, tc.snapshotTS, tc.staleness, tc.gradingEnabled); got != tc.want {
				t.Fatalf("CodeTierForSnapshot = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCodeStaleBoundariesCalibrated 锁住 2026-10-01 的校准结论不被静默改回。
//
// 为什么需要它：原值 60s / 900s 是 ADR-0004 标注的"初始值…Phase2-start 校准"
// 占位数，从未校准。60s 的直接后果是——空闲工作区（无文件变更 → 无全量对账 →
// staleness 单调增长）的索引明明可证明正确，却在 60s 后把关系类查询永久降级为
// grep、15min 后让 code.* 工具整体不再注册。这条测试让任何把它改回 60s 量级的
// 改动必须先推翻这条校准，而不是无声地退化。
//
// 同时锁住 S_fresh < S_max 这个结构不变量：否则三档分级退化成两档。
func TestCodeStaleBoundariesCalibrated(t *testing.T) {
	if CodeStaleFreshSeconds >= CodeStaleMaxSeconds {
		t.Fatalf("S_fresh(%d) 必须小于 S_max(%d)，否则 definitions 档消失、三档退化",
			CodeStaleFreshSeconds, CodeStaleMaxSeconds)
	}
	// definitions 档要真的有宽度（当前是 15min~2h，即 105 分钟）。
	if CodeStaleMaxSeconds-CodeStaleFreshSeconds < 3600 {
		t.Fatalf("definitions 档宽度 %ds 过窄（< 1h），陈旧度分级形同虚设",
			CodeStaleMaxSeconds-CodeStaleFreshSeconds)
	}
	// 反向护栏：这两个值是"源码索引的陈旧度容忍度"，不是"实时代理的存活时间"。
	// 若有人按日志 tail 的量级（秒/分钟级）重新收紧，这里会失败并要求先读上面的校准依据。
	if CodeStaleFreshSeconds < 3600 {
		t.Fatalf("S_fresh = %ds 回到分钟级量级；实测全量对账的中位间隔是 ~90 分钟，"+
			"照此设阈值等于把 relations 闸门焊死在关闭位。改前请先读本常量上方的校准说明",
			CodeStaleFreshSeconds)
	}
	// 空闲可观测（2026-10-01 校准的核心回归点）：空闲工作区没有文件变更，
	// 全量对账不会重跑，staleness 单调增长。校准前 S_fresh=60s，于是这类
	// reader 在 60s 后就把关系类查询永久降级为 grep。现在覆盖实测的
	// ~90 分钟对账节奏，relations 在整个节奏内都走索引。
	if got := CodeTierForSnapshot(false, 100, 5400, true); got != CodeIndexTierAll {
		t.Fatalf("空闲 90 分钟（≈实测对账中位间隔）后的 reader tier = %q, want all"+
			"（对账节奏内不该丢掉关系类）", got)
	}
	// 超出 S_fresh 后应是"降级为 definitions"而不是 none——工具整体消失
	// 比"答案变旧"更糟，definitions 档把控制权交回 grep 而不是撤走工具。
	if got := CodeTierForSnapshot(false, 100, 14400, true); got != CodeIndexTierDefinitions {
		t.Fatalf("空闲 4 小时后的 reader tier = %q, want definitions（应是降级而非撤走工具）", got)
	}
}

func TestCodeEnvelopeCarriesStalenessTriple(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	fixture.handle.SnapshotTS = 1758345600
	fixture.handle.StalenessSeconds = 12
	fixture.handle.Tier = CodeIndexTierAll

	tool := NewCodeSearchTool()
	tool.SetCodeIndexResolver(fixture.resolver)
	ctx := context.Background()

	res, err := tool.Execute(ctx, map[string]interface{}{"query": "Alpha"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex {
		t.Fatalf("source = %q, want index", env.Source)
	}
	if env.SnapshotTS != 1758345600 || env.StalenessSeconds != 12 {
		t.Fatalf("snapshot/staleness = %d/%d, want 1758345600/12", env.SnapshotTS, env.StalenessSeconds)
	}
	if env.Completeness != codeCompletenessFull {
		t.Fatalf("completeness = %q, want full", env.Completeness)
	}

	// 截断 → partial。
	res, err = tool.Execute(ctx, map[string]interface{}{"query": "Alpha", "limit": 1})
	if err != nil {
		t.Fatalf("Execute(limit=1): %v", err)
	}
	env = decodeCodeEnvelope(t, res)
	if !env.Truncated || env.Completeness != codeCompletenessPartial {
		t.Fatalf("truncated/completeness = %v/%q, want true/partial", env.Truncated, env.Completeness)
	}

	// 三字段必须显式出现（无 omitempty）：用原始 map 检查 key 存在。
	assertEnvelopeHasStalenessKeys(t, res.Content)
}

func TestCodeEnvelopeFallbackKeepsStalenessTriple(t *testing.T) {
	tool := NewCodeSearchTool()
	tool.SetCodeIndexResolver(func(context.Context) (*CodeIndexHandle, bool) { return nil, false })

	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "Alpha"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceFallback {
		t.Fatalf("source = %q, want fallback", env.Source)
	}
	if env.Completeness != codeCompletenessFallback {
		t.Fatalf("completeness = %q, want fallback", env.Completeness)
	}
	if env.SnapshotTS != 0 || env.StalenessSeconds != 0 {
		t.Fatalf("fallback snapshot/staleness = %d/%d, want 0/0", env.SnapshotTS, env.StalenessSeconds)
	}
	// 零值也必须出现（ADR-0004 §4.2：不得省略）。
	assertEnvelopeHasStalenessKeys(t, res.Content)
}

func TestCodeRelationGuardUnderStaleness(t *testing.T) {
	fixture := newCodeToolsFixture(t)
	fixture.handle.SnapshotTS = time.Now().Add(-14400 * time.Second).Unix()
	fixture.handle.StalenessSeconds = 14400
	fixture.handle.Tier = CodeIndexTierDefinitions
	ctx := context.Background()

	// 关系类（references / callers / navigate-refs）在中等陈旧下必须实时兜底。
	relationRuns := map[string]func() *toolkit.ToolResult{
		"code_references": func() *toolkit.ToolResult {
			tool := NewCodeReferencesTool()
			tool.SetCodeIndexResolver(fixture.resolver)
			res, _ := tool.Execute(ctx, map[string]interface{}{"symbol": "Alpha"})
			return res
		},
		"code_callers": func() *toolkit.ToolResult {
			tool := NewCodeCallersTool()
			tool.SetCodeIndexResolver(fixture.resolver)
			res, _ := tool.Execute(ctx, map[string]interface{}{"symbol": "Alpha"})
			return res
		},
		"code_navigate(refs)": func() *toolkit.ToolResult {
			tool := NewCodeNavigateTool()
			tool.SetCodeIndexResolver(fixture.resolver)
			res, _ := tool.Execute(ctx, map[string]interface{}{"symbol": "Alpha", "direction": "refs"})
			return res
		},
	}
	for name, run := range relationRuns {
		t.Run(name, func(t *testing.T) {
			env := decodeCodeEnvelope(t, run())
			if env.Source != codeSourceFallback {
				t.Fatalf("source = %q, want fallback（陈旧关系类不得返回索引结果）", env.Source)
			}
			if env.Fallback == nil || env.Fallback.Reason != codeFallbackStaleIndex {
				t.Fatalf("fallback = %+v, want reason=%s", env.Fallback, codeFallbackStaleIndex)
			}
			if env.StalenessSeconds != 14400 {
				t.Fatalf("staleness_seconds = %d, want 14400（D3 实际值）", env.StalenessSeconds)
			}
			if env.Completeness != codeCompletenessFallback {
				t.Fatalf("completeness = %q, want fallback", env.Completeness)
			}
			if !strings.Contains(env.Explanation, "落后约 14400 秒") {
				t.Fatalf("explanation 缺少陈旧度说明: %q", env.Explanation)
			}
		})
	}

	// 定义类在中等陈旧下仍可用（D2）：返回索引结果 + 实际陈旧度。
	search := NewCodeSearchTool()
	search.SetCodeIndexResolver(fixture.resolver)
	res, err := search.Execute(ctx, map[string]interface{}{"query": "Alpha"})
	if err != nil {
		t.Fatalf("code_search: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex || env.StalenessSeconds != 14400 {
		t.Fatalf("中等陈旧定义类 = %q/%d, want index/14400", env.Source, env.StalenessSeconds)
	}

	// 过旧档：定义类也必须兜底（等价 mode=off 工具面）。
	fixture.handle.Tier = CodeIndexTierNone
	res, err = search.Execute(ctx, map[string]interface{}{"query": "Alpha"})
	if err != nil {
		t.Fatalf("code_search(stale): %v", err)
	}
	env = decodeCodeEnvelope(t, res)
	if env.Source != codeSourceFallback || env.Fallback == nil || env.Fallback.Reason != codeFallbackStaleIndex {
		t.Fatalf("过旧档定义类必须兜底，got source=%q fallback=%+v", env.Source, env.Fallback)
	}
	if !strings.Contains(env.Explanation, "过旧档") {
		t.Fatalf("explanation 缺少档位说明: %q", env.Explanation)
	}
}

func TestCodeStaleReaderEscapeHatchAllowsRelation(t *testing.T) {
	// 逃生舱的 tier 语义：grading=false 时 reader 也按 writer 策略（全开）。
	tier := CodeTierForSnapshot(false, 100, 100000, false)
	if tier != CodeIndexTierAll {
		t.Fatalf("tier = %q, want all（stale_reader=off 逃生舱）", tier)
	}

	fixture := newCodeToolsFixture(t)
	fixture.handle.Tier = tier
	fixture.handle.StalenessSeconds = 100000
	tool := NewCodeReferencesTool()
	tool.SetCodeIndexResolver(fixture.resolver)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"symbol": "Alpha"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeCodeEnvelope(t, res)
	if env.Source != codeSourceIndex {
		t.Fatalf("source = %q, want index（逃生舱下用户自担风险）", env.Source)
	}
	// 逃生舱只影响注册/放行，不影响陈旧度上报（D3）。
	if env.StalenessSeconds != 100000 {
		t.Fatalf("staleness_seconds = %d, want 100000（实际值不得省略）", env.StalenessSeconds)
	}
}

// assertEnvelopeHasStalenessKeys 检查原始 JSON 是否显式携带三字段（无 omitempty）。
func assertEnvelopeHasStalenessKeys(t *testing.T, content string) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	for _, key := range []string{"snapshot_ts", "staleness_seconds", "completeness"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("信封缺少字段 %q: %s", key, content)
		}
	}
}
