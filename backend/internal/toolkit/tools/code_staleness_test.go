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
//   - 分级函数边界（60s / 900s，== 视为通过）；
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
		{"reader S=60 视为新鲜（边界通过）", false, 100, 60, true, CodeIndexTierAll},
		{"reader S=61 → 仅定义类", false, 100, 61, true, CodeIndexTierDefinitions},
		{"reader S=900 视为中等上限（边界通过）", false, 100, 900, true, CodeIndexTierDefinitions},
		{"reader S=901 → 不注册", false, 100, 901, true, CodeIndexTierNone},
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
	fixture.handle.SnapshotTS = time.Now().Add(-300 * time.Second).Unix()
	fixture.handle.StalenessSeconds = 300
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
			if env.StalenessSeconds != 300 {
				t.Fatalf("staleness_seconds = %d, want 300（D3 实际值）", env.StalenessSeconds)
			}
			if env.Completeness != codeCompletenessFallback {
				t.Fatalf("completeness = %q, want fallback", env.Completeness)
			}
			if !strings.Contains(env.Explanation, "落后约 300 秒") {
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
	if env.Source != codeSourceIndex || env.StalenessSeconds != 300 {
		t.Fatalf("中等陈旧定义类 = %q/%d, want index/300", env.Source, env.StalenessSeconds)
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
