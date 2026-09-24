package commands

import (
	"path/filepath"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// TestBuiltinTemplateToolSurfaceQuantifiedBound 钉住设计文档 §5.1 第 1 条 /
// 实施方案 §1.3 第 1 条的量化界（FR-2）：
//
//	minimal / review 模板声明的生效工具面 ≤ 全量工具清单的 40%。
//
// 全量清单取 runtimepolicy.KnownToolTaxonomyNames()（与 `profile validate`
// 的登记清单同一事实源），不重复维护第二份名单。
//
// 实测证据（2026-09-24，`aicli exec --enable-tools --debug-http` 的请求 artifact
// `*_request_provider_wrapper.json`）：
//   - 全量基线请求 tools[] = 45：其中 21 个为 profile 可控的目录工具，24 个为
//     runtime-owned 控制面工具；后者按 `policy/tool_policy.go`
//     IsRuntimeOwnedEssentialTool 设计**不受 allowlist 收窄**，仅显式 denylist
//     可裁剪（设计 §9.2 T2 / Batch 9），故量化界按"profile 可控工具面"计量；
//   - minimal 生效 2（view/grep）、review 生效 6（fetch/glob/grep/ls/view/web_search），
//     相对 21 个可控工具分别 9.5% / 28.6%，均 ≤ 40%；
//   - 两个模板被排除的目录工具均未出现在 tools[]（extra=0，无"假裁剪"）。
func TestBuiltinTemplateToolSurfaceQuantifiedBound(t *testing.T) {
	baseline := runtimepolicy.KnownToolTaxonomyNames()
	if len(baseline) == 0 {
		t.Fatal("工具清单为空：无法计算量化界")
	}
	known := make(map[string]struct{}, len(baseline))
	for _, name := range baseline {
		known[name] = struct{}{}
	}

	cfg := &config.Config{SkillsRuntime: &config.SkillsRuntimeConfig{}}
	for _, tc := range []struct {
		template string
		name     string
	}{
		{template: "minimal", name: "minimal-quant"},
		{template: "review", name: "review-quant"},
	} {
		root := filepath.Join(t.TempDir(), tc.name)
		writeRenderedProfile(t, root, tc.template, tc.name)
		result, err := runProfileShowCommand(cfg, root, "")
		if err != nil {
			t.Fatalf("show %s: %v", tc.template, err)
		}

		count := result.ToolPolicy.EffectiveAllowCount
		if count < 0 {
			t.Fatalf("%s 模板必须声明 allowlist（否则没有裁剪面）", tc.template)
		}
		if ratio := float64(count) / float64(len(baseline)); ratio > 0.4 {
			t.Fatalf("%s 生效工具面 %d/%d = %.1f%% 超过 40%% 量化界（§1.3 第 1 条）",
				tc.template, count, len(baseline), ratio*100)
		}
		if want := len(result.ToolPolicy.Allowlist) - len(result.ToolPolicy.ExcludedByDeny); count != want {
			t.Fatalf("%s 生效数量口径不一致：effective=%d want=%d（allowlist=%d, excluded=%d）",
				tc.template, count, want, len(result.ToolPolicy.Allowlist), len(result.ToolPolicy.ExcludedByDeny))
		}
		for _, name := range result.ToolPolicy.Allowlist {
			if _, ok := known[name]; !ok {
				t.Fatalf("%s allowlist 含未登记工具名：%q（清单漂移）", tc.template, name)
			}
		}
	}
}
