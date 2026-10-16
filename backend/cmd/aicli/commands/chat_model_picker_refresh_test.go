package commands

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// The model stage leads with the refresh row so re-pulling the endpoint is the
// first action the user sees; /model ranks above add because it can change what
// every later row offers.
func TestBuildModelPickerItemsWithRefreshRowLeadsWithRefresh(t *testing.T) {
	models := []string{"gpt-5", "o3"}
	list := buildModelPickerItemsWithRefreshRow(models, "gpt-5")

	if list.refreshIndex != 0 || list.addIndex != 1 || list.optionIndexOff != 2 {
		t.Fatalf("refreshIndex/addIndex/optionIndexOff = %d/%d/%d, want 0/1/2",
			list.refreshIndex, list.addIndex, list.optionIndexOff)
	}
	if len(list.items) != len(models)+2 {
		t.Fatalf("items = %d, want %d action rows plus the catalog", len(list.items), len(models)+2)
	}
	if title := list.items[list.refreshIndex].Title; !strings.Contains(title, "刷新模型列表") {
		t.Fatalf("first row = %q, want the refresh action", title)
	}
	if title := list.items[list.addIndex].Title; !strings.Contains(title, "添加模型") {
		t.Fatalf("second row = %q, want the add action", title)
	}
	for i, model := range models {
		if row := list.optionIndexOff + i; !strings.Contains(list.items[row].Title, model) {
			t.Fatalf("row %d = %q, want model %q", row, list.items[row].Title, model)
		}
	}
	// Neither action row may alias a catalog entry, or picking "refresh" would
	// silently switch the model instead.
	for name, row := range map[string]int{"refresh": list.refreshIndex, "add": list.addIndex} {
		if mapped := row - list.optionIndexOff; mapped >= 0 && mapped < len(models) {
			t.Fatalf("%s row maps into the catalog at index %d", name, mapped)
		}
	}
}

// The startup picker (selectModelFullScreen) runs without a ChatSession and
// maps item indexes straight back into the option list, so its builder must not
// gain a leading action row.
func TestBuildModelPickerItemsWithAddRowHasNoRefreshRow(t *testing.T) {
	list := buildModelPickerItemsWithAddRow([]string{"gpt-5"}, "")
	if list.refreshIndex != -1 {
		t.Fatalf("refreshIndex = %d, want -1", list.refreshIndex)
	}
	if list.addIndex != 0 || list.optionIndexOff != 1 {
		t.Fatalf("addIndex/optionIndexOff = %d/%d, want 0/1", list.addIndex, list.optionIndexOff)
	}
}

func TestChatProviderRefreshInlineSummary(t *testing.T) {
	tests := []struct {
		name     string
		run      chatProviderRefreshRun
		contains []string
		absent   []string
	}{
		{
			name: "updated",
			run: chatProviderRefreshRun{Outcomes: []chatProviderRefreshProviderOutcome{{
				Name:          "alpha",
				ModelCount:    4,
				Added:         []string{"m-c"},
				Removed:       []string{"m-d"},
				ChangedModels: 2,
				Reason:        "updated",
			}}},
			contains: []string{"已刷新 provider alpha", "共 4 个模型", "新增 m-c", "移除 m-d", "能力更新 2 个", "已保存到配置文件"},
		},
		{
			name: "already up to date",
			run: chatProviderRefreshRun{Outcomes: []chatProviderRefreshProviderOutcome{{
				Name:       "alpha",
				ModelCount: 3,
				Reason:     "already_up_to_date",
			}}},
			contains: []string{"alpha", "已是最新", "3 个模型"},
			absent:   []string{"已保存到配置文件"},
		},
		{
			name: "fetch failed",
			run: chatProviderRefreshRun{Outcomes: []chatProviderRefreshProviderOutcome{{
				Name:   "alpha",
				Reason: "fetch_failed: dial tcp 127.0.0.1:1: connect: connection refused",
			}}},
			contains: []string{"刷新 provider alpha 失败", "connection refused"},
			absent:   []string{"已刷新", "已保存到配置文件"},
		},
		{
			name: "persist failed",
			run: chatProviderRefreshRun{Outcomes: []chatProviderRefreshProviderOutcome{{
				Name:   "alpha",
				Reason: "persist_failed: read-only file system",
			}}},
			contains: []string{"刷新 provider alpha 失败", "写入配置", "read-only file system"},
			absent:   []string{"已刷新", "已保存到配置文件"},
		},
		{
			name:     "no provider selected",
			run:      chatProviderRefreshRun{},
			contains: []string{"alpha", "没有可刷新的模型列表"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chatProviderRefreshInlineSummary("alpha", tc.run)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Fatalf("summary = %q, want it to contain %q", got, want)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(got, unwanted) {
					t.Fatalf("summary = %q, must not contain %q", got, unwanted)
				}
			}
		})
	}
}

func TestRefreshChatPickerProviderModelsRequiresProvider(t *testing.T) {
	result := refreshChatPickerProviderModels(nil, "   ")
	text := chatAccountCommandText(t, result)
	if !strings.Contains(text, "未指定 provider") {
		t.Fatalf("result = %q, want the missing-provider guard", text)
	}
}

// End to end: the picker's refresh row re-pulls /v1/models for the provider the
// list was opened on and writes the fetched catalog back to the config file.
func TestRefreshChatPickerProviderModelsWritesFetchedCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m-a"},{"id":"m-b"},{"id":"m-c"}]}`))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := chatModelAddTestConfig(t, path, "alpha", "m-a", []string{"m-a", "m-b"})
	// chatModelAddTestConfig seeds the on-disk node; the in-memory entry it
	// mirrors omits the endpoint fields, so point them at the fake server and
	// hand the fetcher an inline key (the seeded api_key_ref resolves nowhere).
	item := cfg.Providers.Items["alpha"]
	item.Protocol = "openai"
	item.BaseURL = server.URL
	item.APIKey = "test-key"
	item.Enabled = true
	cfg.Providers.Items["alpha"] = item

	result := refreshChatPickerProviderModels(&ChatSession{Config: cfg}, "alpha")
	text := chatAccountCommandText(t, result)
	if !strings.Contains(text, "已刷新 provider alpha") {
		t.Fatalf("result = %q, want a success summary", text)
	}
	if !strings.Contains(text, "新增 m-c") {
		t.Fatalf("result = %q, want the fetched model reported as added", text)
	}

	got := readProviderModelsInConfig(t, path, "alpha")
	for _, want := range []string{"m-a", "m-b", "m-c"} {
		if !containsFold(got, want) {
			t.Fatalf("persisted supported_models = %v, want %q present", got, want)
		}
	}
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

// 刷新回执必须同时落到 header 与刷新行 preview 上：副屏（alternate screen）期间
// transcript 不可见，只写 transcript 等于"按了刷新没有任何反馈"。
func TestBuildModelPickerItemsWithRefreshRowDetailAttachesPreview(t *testing.T) {
	detail := "刷新 provider alpha 失败：dial tcp 127.0.0.1:1: connect: connection refused"
	list := buildModelPickerItemsWithRefreshRowDetail([]string{"gpt-5"}, "gpt-5", detail)

	if list.refreshIndex != 0 || list.addIndex != 1 || list.optionIndexOff != 2 {
		t.Fatalf("detail variant must keep the row contract, got refresh/add/off = %d/%d/%d",
			list.refreshIndex, list.addIndex, list.optionIndexOff)
	}
	if len(list.items) != 3 {
		t.Fatalf("items = %d, want 2 action rows plus the catalog", len(list.items))
	}
	if preview := list.items[list.refreshIndex].Preview; !strings.Contains(preview, "connection refused") {
		t.Fatalf("refresh row preview = %q, want the receipt detail", preview)
	}
	if preview := list.items[list.addIndex].Preview; preview != "" {
		t.Fatalf("add row preview = %q, want it untouched", preview)
	}

	// 空白 detail 必须退化成与 buildModelPickerItemsWithRefreshRow 逐字一致，
	// 避免没有回执时给刷新行凭空加一行 preview。
	plain := buildModelPickerItemsWithRefreshRowDetail([]string{"gpt-5"}, "gpt-5", "   ")
	if preview := plain.items[plain.refreshIndex].Preview; preview != "" {
		t.Fatalf("blank detail must not set a preview, got %q", preview)
	}
}

func TestChatModelPickerModelStageSubtitleCarriesNotice(t *testing.T) {
	const legacy = "provider: free_model · Enter 确认，Delete 删除选中模型，Esc 取消"
	if got := chatModelPickerModelStageSubtitle("free_model", ""); got != legacy {
		t.Fatalf("notice-free subtitle = %q, want the legacy header", got)
	}
	if got := chatModelPickerModelStageSubtitle("free_model", "   "); got != legacy {
		t.Fatalf("blank notice subtitle = %q, want the legacy header", got)
	}
	withNotice := chatModelPickerModelStageSubtitle("free_model", " 已是最新（3 个模型） ")
	for _, want := range []string{"provider: free_model", "已是最新（3 个模型）", "Enter 确认，Delete 删除选中模型，Esc 取消"} {
		if !strings.Contains(withNotice, want) {
			t.Fatalf("subtitle = %q, want it to contain %q", withNotice, want)
		}
	}
}

// header 摘要必须短（单行、被 fit 截断），且失败时不得出现"已刷新"这类成功措辞。
func TestChatModelPickerRefreshPickerSummary(t *testing.T) {
	tests := []struct {
		name     string
		run      chatProviderRefreshRun
		contains []string
		absent   []string
	}{
		{
			name: "updated",
			run: chatProviderRefreshRun{Outcomes: []chatProviderRefreshProviderOutcome{{
				Name:          "alpha",
				ModelCount:    4,
				Added:         []string{"m-c"},
				Removed:       []string{"m-d"},
				ChangedModels: 2,
				Reason:        "updated",
			}}},
			contains: []string{"已刷新", "共 4 个模型", "新增 m-c", "移除 m-d", "能力更新 2 个"},
		},
		{
			name: "already up to date",
			run: chatProviderRefreshRun{Outcomes: []chatProviderRefreshProviderOutcome{{
				Name:       "alpha",
				ModelCount: 3,
				Reason:     "already_up_to_date",
			}}},
			contains: []string{"已是最新", "3 个模型"},
			absent:   []string{"已刷新", "已保存"},
		},
		{
			name: "fetch failed",
			run: chatProviderRefreshRun{Outcomes: []chatProviderRefreshProviderOutcome{{
				Name:   "alpha",
				Reason: "fetch_failed: dial tcp 127.0.0.1:1: connect: connection refused",
			}}},
			contains: []string{"刷新失败", "connection refused"},
			absent:   []string{"已刷新", "已保存"},
		},
		{
			name: "persist failed",
			run: chatProviderRefreshRun{Outcomes: []chatProviderRefreshProviderOutcome{{
				Name:   "alpha",
				Reason: "persist_failed: read-only file system",
			}}},
			contains: []string{"刷新失败", "写入配置", "read-only file system"},
			absent:   []string{"已刷新", "已保存"},
		},
		{
			name:     "no outcome",
			run:      chatProviderRefreshRun{},
			contains: []string{"alpha", "没有可刷新的模型列表"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chatModelPickerRefreshPickerSummary("alpha", tc.run)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Fatalf("summary = %q, want it to contain %q", got, want)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(got, unwanted) {
					t.Fatalf("summary = %q, must not contain %q", got, unwanted)
				}
			}
			if compact := chatModelPickerRefreshCompactSummary(got); compact == "" {
				t.Fatalf("summary = %q compacted to empty", got)
			}
		})
	}
}

func TestChatModelPickerRefreshCompactSummary(t *testing.T) {
	if got := chatModelPickerRefreshCompactSummary("  已刷新： 共 3 个模型\n"); got != "已刷新： 共 3 个模型" {
		t.Fatalf("compacted summary = %q, want folded whitespace", got)
	}
	long := strings.Repeat("失", chatModelPickerRefreshSummaryMaxRunes+10)
	got := chatModelPickerRefreshCompactSummary(long)
	if runes := []rune(got); len(runes) != chatModelPickerRefreshSummaryMaxRunes {
		t.Fatalf("compacted length = %d runes, want %d", len(runes), chatModelPickerRefreshSummaryMaxRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("compacted summary = %q, want a trailing ellipsis", got)
	}
}

// seedChatRefreshableProvider 把 chatModelAddTestConfig 里的 provider 指向一个
// 可拉取的 /v1/models 端点：种子只写节点，内联字段留空，api_key_ref 也解析不到
// 值（会先撞上 api key 校验），所以这里补 protocol/base_url/inline key。
func seedChatRefreshableProvider(cfg *config.Config, providerName, baseURL string) {
	item := cfg.Providers.Items[providerName]
	item.Protocol = "openai"
	item.BaseURL = baseURL
	item.APIKey = "test-key"
	item.Enabled = true
	cfg.Providers.Items[providerName] = item
}

// 端到端（回执侧）：端点返回的清单与配置一致时，刷新仍必须带回可见回执——
// 这正是用户报告"按了刷新没有反馈"的场景（清单没变，界面看不出任何变化）。
func TestRefreshChatPickerProviderModelsWithReceiptReportsUpToDateWithoutChanges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m-a"},{"id":"m-b"}]}`))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := chatModelAddTestConfig(t, path, "alpha", "m-a", []string{"m-a", "m-b"})
	seedChatRefreshableProvider(cfg, "alpha", server.URL)

	result, receipt := refreshChatPickerProviderModelsWithReceipt(&ChatSession{Config: cfg}, "alpha")
	if strings.TrimSpace(receipt.Summary) == "" || strings.TrimSpace(receipt.Detail) == "" {
		t.Fatalf("receipt = %+v, want both header and preview text", receipt)
	}
	if !strings.Contains(receipt.Summary, "alpha") && !strings.Contains(receipt.Detail, "alpha") {
		t.Fatalf("receipt = %+v, want it to name the provider", receipt)
	}
	if text := chatAccountCommandText(t, result); strings.TrimSpace(text) == "" {
		t.Fatal("transcript record must not be empty")
	}
	if got := readProviderModelsInConfig(t, path, "alpha"); !containsFold(got, "m-a") || !containsFold(got, "m-b") {
		t.Fatalf("persisted supported_models = %v, want the catalog intact", got)
	}
}

// 端到端（失败侧）：端点不可达时，回执必须带上失败原因并通过 transcript 记录，
// 不能静默——用户按下刷新后至少能看到"刷新失败：..."。
func TestRefreshChatPickerProviderModelsWithReceiptReportsFetchFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := chatModelAddTestConfig(t, path, "alpha", "m-a", []string{"m-a"})
	seedChatRefreshableProvider(cfg, "alpha", "http://127.0.0.1:1")

	result, receipt := refreshChatPickerProviderModelsWithReceipt(&ChatSession{Config: cfg}, "alpha")
	for name, text := range map[string]string{"summary": receipt.Summary, "detail": receipt.Detail} {
		if strings.TrimSpace(text) == "" {
			t.Fatalf("%s = %q, want the failure reason surfaced in the picker", name, text)
		}
		// header 走「刷新失败：…」，preview 复用 inline summary 的「刷新 provider
		// alpha 失败：…」，两者都必须点明失败。
		if !strings.Contains(text, "失败") {
			t.Fatalf("%s = %q, want a failure notice", name, text)
		}
		if strings.Contains(text, "已刷新") || strings.Contains(text, "已保存") {
			t.Fatalf("%s = %q, must not claim success", name, text)
		}
	}
	if text := chatAccountCommandText(t, result); !strings.Contains(text, "刷新 provider alpha 失败") {
		t.Fatalf("result = %q, want the transcript record to carry the failure", text)
	}
	if got := readProviderModelsInConfig(t, path, "alpha"); len(got) != 1 || !containsFold(got, "m-a") {
		t.Fatalf("persisted supported_models = %v, want the failed refresh to leave it untouched", got)
	}
}

// provider 名与配置对不上时也必须给副屏回执（配置读/解析失败同样走这条路）。
func TestRefreshChatPickerProviderModelsWithReceiptReportsErrors(t *testing.T) {
	t.Run("missing provider name", func(t *testing.T) {
		_, receipt := refreshChatPickerProviderModelsWithReceipt(nil, "  ")
		if !strings.Contains(receipt.Summary, "未指定 provider") || !strings.Contains(receipt.Detail, "未指定 provider") {
			t.Fatalf("receipt = %+v, want the missing-provider guard", receipt)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		cfg := chatModelAddTestConfig(t, path, "alpha", "m-a", []string{"m-a"})
		result, receipt := refreshChatPickerProviderModelsWithReceipt(&ChatSession{Config: cfg}, "nope")
		if strings.TrimSpace(receipt.Summary) == "" || strings.TrimSpace(receipt.Detail) == "" {
			t.Fatalf("receipt = %+v, want both fields populated", receipt)
		}
		if !strings.Contains(receipt.Summary, "刷新失败") {
			t.Fatalf("summary = %q, want it to lead with the failure", receipt.Summary)
		}
		if text := chatAccountCommandText(t, result); strings.TrimSpace(text) == "" {
			t.Fatal("error result must not be empty")
		}
	})
}

// 刷新/添加是动作行：副屏里不给它们序号，也不让它们占号，所以模型行仍从 [1] 起。
// 这条契约把「第 1、2 行不编号」钉在两个 builder 上（启动路径只有添加行）。
func TestModelPickerActionRowsCarryNoIndex(t *testing.T) {
	list := buildModelPickerItemsWithRefreshRow([]string{"m-a", "m-b"}, "m-a")
	if !list.items[list.refreshIndex].NoIndex {
		t.Fatalf("refresh row must opt out of the rank column: %#v", list.items[list.refreshIndex])
	}
	if !list.items[list.addIndex].NoIndex {
		t.Fatalf("add row must opt out of the rank column: %#v", list.items[list.addIndex])
	}
	for index := list.optionIndexOff; index < len(list.items); index++ {
		if list.items[index].NoIndex {
			t.Fatalf("model row %d must keep its rank label: %#v", index, list.items[index])
		}
	}

	// 启动路径没有刷新行，第一条仍是动作行。
	addOnly := buildModelPickerItemsWithAddRow([]string{"m-a"}, "m-a")
	if !addOnly.items[addOnly.addIndex].NoIndex {
		t.Fatalf("startup add row must opt out of the rank column: %#v", addOnly.items[addOnly.addIndex])
	}
	if addOnly.items[addOnly.optionIndexOff].NoIndex {
		t.Fatal("the first model row must keep its rank label")
	}

	// 挂回执（preview）不能顺手把开关改掉：列表重开后动作行依旧无序号。
	withDetail := buildModelPickerItemsWithRefreshRowDetail([]string{"m-a"}, "m-a", "已是最新（1 个模型）")
	if !withDetail.items[withDetail.refreshIndex].NoIndex || withDetail.items[withDetail.refreshIndex].Preview == "" {
		t.Fatalf("refresh row = %#v, want NoIndex plus the receipt preview", withDetail.items[withDetail.refreshIndex])
	}
}
