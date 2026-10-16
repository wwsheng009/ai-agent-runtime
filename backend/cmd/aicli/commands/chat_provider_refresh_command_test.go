package commands

import (
	"strings"
	"testing"
)

func TestParseChatProviderRefreshCommand(t *testing.T) {
	cases := []struct {
		name           string
		line           string
		wantOK         bool
		wantNames      []string
		wantAll        bool
		wantDryRun     bool
		wantModelsPath string
		wantTimeout    int
		wantErr        bool
	}{
		{name: "裸命令不是 refresh", line: "/provider", wantOK: false},
		{name: "status 不是 refresh", line: "/provider status", wantOK: false},
		{name: "provider 名不是 refresh", line: "/provider openrouter", wantOK: false},
		{name: "前缀相同也不算 refresh", line: "/provider refresh-models-x", wantOK: false},
		{name: "flag 形式不是 refresh", line: "/provider --provider openrouter", wantOK: false},
		{name: "裸 refresh", line: "/provider refresh", wantOK: true},
		{name: "大小写不敏感", line: "/provider REFRESH", wantOK: true},
		{name: "指定多个 provider", line: "/provider refresh openrouter deepseek", wantOK: true,
			wantNames: []string{"openrouter", "deepseek"}},
		{name: "--all 与 --dry-run", line: "/provider refresh --all --dry-run", wantOK: true,
			wantAll: true, wantDryRun: true},
		{name: "等号形式取值", line: "/provider refresh --models-path=/api/v1/models --timeout=30", wantOK: true,
			wantModelsPath: "/api/v1/models", wantTimeout: 30},
		{name: "空格形式取值", line: "/provider refresh --models-path /api/models --timeout 5", wantOK: true,
			wantModelsPath: "/api/models", wantTimeout: 5},
		{name: "timeout 非数字", line: "/provider refresh --timeout abc", wantOK: true, wantErr: true},
		{name: "timeout 负数", line: "/provider refresh --timeout=-1", wantOK: true, wantErr: true},
		{name: "未知 flag", line: "/provider refresh --nope", wantOK: true, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request, ok, err := parseChatProviderRefreshCommand(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				if err != nil {
					t.Fatalf("未识别时报错必须为空: %v", err)
				}
				return
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望解析错误，实际 success: %+v", request)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if strings.Join(request.Names, ",") != strings.Join(tc.wantNames, ",") {
				t.Fatalf("names = %v, want %v", request.Names, tc.wantNames)
			}
			if request.All != tc.wantAll {
				t.Fatalf("All = %v, want %v", request.All, tc.wantAll)
			}
			if request.DryRun != tc.wantDryRun {
				t.Fatalf("DryRun = %v, want %v", request.DryRun, tc.wantDryRun)
			}
			if request.ModelsPath != tc.wantModelsPath {
				t.Fatalf("ModelsPath = %q, want %q", request.ModelsPath, tc.wantModelsPath)
			}
			if request.TimeoutSec != tc.wantTimeout {
				t.Fatalf("TimeoutSec = %d, want %d", request.TimeoutSec, tc.wantTimeout)
			}
		})
	}
}

// TestExecuteStructuredProviderRefreshInterceptsTokenBeforeProviderParsing 断言
// "refresh" 在任何 provider 解析之前被拦截：否则它会被 parseModelCommandRequest
// 当成 provider 名或模型名。同时断言该拦截只对 /provider 生效。
func TestExecuteStructuredProviderRefreshInterceptsTokenBeforeProviderParsing(t *testing.T) {
	result, handled := executeStructuredModelCommandVariant(nil, "/provider refresh", modelCommandVariantProvider)
	if !handled {
		t.Fatal("/provider refresh 必须由统一 provider 通道接管")
	}
	if text := resultText(result); !strings.Contains(text, "当前没有活动会话") {
		t.Fatalf("无会话时应报会话错误（而不是 provider 名解析错误）: %s", text)
	}

	result, handled = executeStructuredModelCommandVariant(&ChatSession{}, "/provider refresh --all", modelCommandVariantProvider)
	if !handled {
		t.Fatal("/provider refresh --all 必须由统一 provider 通道接管")
	}
	if text := resultText(result); !strings.Contains(text, "provider 配置不可用") {
		t.Fatalf("无配置时应报配置错误: %s", text)
	}

	result, handled = executeStructuredModelCommandVariant(&ChatSession{}, "/provider refresh --timeout abc", modelCommandVariantProvider)
	if !handled {
		t.Fatal("参数错误也必须由统一 provider 通道接管")
	}
	if text := resultText(result); !strings.Contains(text, "--timeout") {
		t.Fatalf("参数错误应回显 --timeout 提示: %s", text)
	}

	// /model 共享同一执行器，但 refresh 是 /provider 独有的二级子命令。
	modelResult, _ := executeStructuredModelCommandVariant(&ChatSession{}, "/model refresh", modelCommandVariantModel)
	if text := resultText(modelResult); strings.Contains(text, "provider 模型列表刷新") {
		t.Fatalf("/model refresh 不得进入 provider 刷新通道: %s", text)
	}
}

func TestChatProviderRefreshModelListKeepsDefaultAndDedupes(t *testing.T) {
	got := chatProviderRefreshModelList([]string{"b", "A", "a", " ", "b"}, "A")
	want := []string{"b", "A"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("list = %v, want %v", got, want)
	}

	// 端点清单里没有的 default_model 必须保留（与 web「获取模型列表」一致）。
	got = chatProviderRefreshModelList([]string{"only-new"}, "pinned-model")
	if strings.Join(got, ",") != "only-new,pinned-model" {
		t.Fatalf("default_model 必须保留: %v", got)
	}
}

func TestDiffProviderModelListsIsCaseInsensitiveAndSorted(t *testing.T) {
	added, removed := diffProviderModelLists(
		[]string{"gpt-4o", "Old-Model"},
		[]string{"GPT-4O", "new-b", "new-a"},
	)
	if strings.Join(added, ",") != "new-a,new-b" {
		t.Fatalf("added = %v", added)
	}
	if strings.Join(removed, ",") != "Old-Model" {
		t.Fatalf("removed = %v", removed)
	}

	added, removed = diffProviderModelLists(nil, nil)
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("空清单不应有差异: added=%v removed=%v", added, removed)
	}
}

func TestRenderChatProviderRefreshOutcomeLabels(t *testing.T) {
	outcomes := []chatProviderRefreshProviderOutcome{
		{Name: "a", Reason: "updated", ModelCount: 3, Added: []string{"x"}, ChangedModels: 1, VerifiedAt: "2026-01-01T00:00:00Z"},
		{Name: "b", Reason: "already_up_to_date"},
		{Name: "c", Reason: "fetch_failed: boom"},
	}
	text := strings.Join(renderChatProviderRefreshOutcome(outcomes, chatProviderRefreshRequest{DryRun: true}, "a", 1, 0, 1), "\n")

	for _, want := range []string{"预览", "would_update", "a [would_update]", "already_up_to_date", "c [fetch_failed: boom]", "verified_at: 2026-01-01T00:00:00Z"} {
		if !strings.Contains(text, want) {
			t.Fatalf("渲染缺少 %q:\n%s", want, text)
		}
	}
	// 拉取失败的 provider 不打印模型计数行（没有可信的模型清单）。
	if got := strings.Count(text, "    模型:"); got != 2 {
		t.Fatalf("模型计数行应为 2 行（updated/already_up_to_date），实际 %d:\n%s", got, text)
	}
}

func TestProviderRefreshRuntimeCommandSpec(t *testing.T) {
	spec, ok := resolveRuntimeCommandSpec("/provider refresh")
	if !ok {
		t.Fatal("/provider refresh 必须能解析出注册表条目")
	}
	if spec.Mode != runtimeModePrompt {
		t.Fatalf("Mode = %v, want runtimeModePrompt（inline+写操作被 chat_busy_screen_exec_test.go 禁止）", spec.Mode)
	}
	if !spec.Confirm {
		t.Fatal("写配置的二级子命令必须确认")
	}
	if spec.Effect != runtimeEffectNextTurn {
		t.Fatalf("Effect = %v, want runtimeEffectNextTurn", spec.Effect)
	}
	if spec.Category != categoryModelRouting {
		t.Fatalf("Category = %v, want categoryModelRouting", spec.Category)
	}
	if category := chatCommandOutputCategoryFor(spec); category != chatOutputSideEffect {
		t.Fatalf("输出类别 = %v, want chatOutputSideEffect", category)
	}

	// 未知二级 token 仍走 /provider 通配档（保持既有 provider 名语义）。
	wildcard, ok := resolveRuntimeCommandSpec("/provider openrouter")
	if !ok {
		t.Fatal("/provider <name> 必须解析通配档")
	}
	if wildcard.Mode != runtimeModeScreen {
		t.Fatalf("通配档 Mode = %v, want runtimeModeScreen", wildcard.Mode)
	}
}

func TestProviderRefreshCompletionIsProviderOnly(t *testing.T) {
	providerCandidates := completionCommandNames(completeProviderSlashArgs(nil, "", 0))
	found := false
	for _, name := range providerCandidates {
		if name == chatProviderRefreshToken {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("/provider 补全必须包含 refresh: %v", providerCandidates)
	}

	for _, name := range completionCommandNames(completeModelSlashArgs(nil, "", 0)) {
		if name == chatProviderRefreshToken {
			t.Fatalf("/model 补全不得混入 refresh（refresh 不是 /model 的子命令）: %v", name)
		}
	}

	// 取值域（如 --provider <name>）不应混入子命令名。
	for _, name := range completionCommandNames(completeProviderSlashArgs(nil, "--provider ", 11)) {
		if name == chatProviderRefreshToken {
			t.Fatalf("provider 取值域不得出现 refresh 候选: %v", name)
		}
	}
}
