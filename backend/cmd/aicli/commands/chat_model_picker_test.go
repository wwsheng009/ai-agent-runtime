package commands

import (
	"bufio"
	"fmt"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestNewRuntimeModelPickerStateStartsOnPreferredPage(t *testing.T) {
	options := make([]string, 0, 25)
	for i := 1; i <= 25; i++ {
		options = append(options, fmt.Sprintf("model_%02d", i))
	}

	state := newRuntimeModelPickerState(options, "model_23", 10)
	items, page, pageCount, total := state.pageWindow()
	if page != 2 || pageCount != 3 || total != 25 {
		t.Fatalf("unexpected page window: page=%d pageCount=%d total=%d", page, pageCount, total)
	}
	if len(items) != 5 || items[0] != "model_21" || items[2] != "model_23" {
		t.Fatalf("expected preferred model page, got %#v", items)
	}
}

func TestApplyRuntimeModelPickerInputPagesAndSelectsCurrentPageNumber(t *testing.T) {
	options := make([]string, 0, 23)
	for i := 1; i <= 23; i++ {
		options = append(options, fmt.Sprintf("model_%02d", i))
	}
	state := newRuntimeModelPickerState(options, "model_01", 10)

	state, result := applyRuntimeModelPickerInput(state, "n", "model_01")
	if result.Done || !result.Redraw || state.Page != 1 {
		t.Fatalf("expected next page redraw, state=%+v result=%+v", state, result)
	}

	_, result = applyRuntimeModelPickerInput(state, "2", "model_11")
	if !result.Done || result.Selected != "model_12" {
		t.Fatalf("expected current-page item 2 to select model_12, got %+v", result)
	}
}

func TestApplyRuntimeModelPickerInputSearchesAndClears(t *testing.T) {
	options := []string{"claude-sonnet", "gpt-4.1", "gpt-4.1-mini", "gpt-5", "qwen-max"}
	state := newRuntimeModelPickerState(options, "qwen-max", 2)

	state, result := applyRuntimeModelPickerInput(state, "/gpt", "qwen-max")
	if result.Done || !result.Redraw || state.Filter != "gpt" || state.Page != 0 {
		t.Fatalf("expected model search redraw, state=%+v result=%+v", state, result)
	}
	items, page, pageCount, total := state.pageWindow()
	if page != 0 || pageCount != 2 || total != 3 {
		t.Fatalf("unexpected filtered window: items=%#v page=%d pageCount=%d total=%d", items, page, pageCount, total)
	}
	if len(items) != 2 || items[0] != "gpt-5" || items[1] != "gpt-4.1" {
		t.Fatalf("expected ranked gpt matches on first page, got %#v", items)
	}

	state, result = applyRuntimeModelPickerInput(state, "c", "gpt-4.1")
	if result.Done || !result.Redraw || state.Filter != "" {
		t.Fatalf("expected search clear redraw, state=%+v result=%+v", state, result)
	}
	_, page, pageCount, total = state.pageWindow()
	if page != 2 || pageCount != 3 || total != len(options) {
		t.Fatalf("expected clear to restore preferred model page, page=%d pageCount=%d total=%d", page, pageCount, total)
	}
}

func TestApplyRuntimeModelPickerInputSupportsFuzzyCustomAndReservedNames(t *testing.T) {
	state := newRuntimeModelPickerState([]string{"deepseek-v3", "n", "qwen-max"}, "qwen-max", 10)

	_, result := applyRuntimeModelPickerInput(state, "deep", "qwen-max")
	if !result.Done || result.Selected != "deepseek-v3" {
		t.Fatalf("expected unique fuzzy search to select deepseek-v3, got %+v", result)
	}

	_, result = applyRuntimeModelPickerInput(state, "n", "qwen-max")
	if !result.Done || result.Selected != "n" {
		t.Fatalf("expected exact model named n to win over next-page command, got %+v", result)
	}

	_, result = applyRuntimeModelPickerInput(state, "custom-preview", "qwen-max")
	if !result.Done || result.Selected != "custom-preview" {
		t.Fatalf("expected unmatched text to remain a custom model, got %+v", result)
	}

	_, result = applyRuntimeModelPickerInput(state, "+next", "qwen-max")
	if !result.Done || result.Selected != "next" {
		t.Fatalf("expected +name to force a reserved custom model name, got %+v", result)
	}
}

func TestRenderRuntimeModelPickerPopupLinesShowsOnlyCurrentPage(t *testing.T) {
	options := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		options = append(options, fmt.Sprintf("model_%02d", i))
	}
	state := newRuntimeModelPickerState(options, "model_01", 5)
	lines := renderRuntimeModelPickerPopupLines(state, "model_01", "model_01", "", "", 0)
	rendered := strings.Join(lines, "\n")

	for _, expected := range []string{"共 12", "第 1/3 页", "model_01", "model_05", "n/p 翻页", "编号按当前页", "+模型名"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected popup to contain %q, got:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "model_06") || strings.Contains(rendered, "model_12") {
		t.Fatalf("expected popup to be limited to first page, got:\n%s", rendered)
	}
}

func TestPromptRuntimeModelSelectionLegacySearchesPagesAndSelectsCurrentPageNumber(t *testing.T) {
	models := make([]string, 0, 15)
	for i := 1; i <= 15; i++ {
		models = append(models, fmt.Sprintf("model_%02d", i))
	}
	session := &ChatSession{
		Provider: config.Provider{
			DefaultModel:    "model_01",
			SupportedModels: models,
		},
		Model:       "model_01",
		InputReader: bufio.NewReader(strings.NewReader("n\n2\n")),
	}

	oldShouldDiscard := shouldDiscardPendingInput
	shouldDiscardPendingInput = func() bool { return false }
	defer func() { shouldDiscardPendingInput = oldShouldDiscard }()

	var selected string
	output := captureStdout(t, func() {
		var err error
		var usedPopup bool
		selected, usedPopup, err = promptRuntimeModelSelection(session)
		if err != nil {
			t.Fatalf("promptRuntimeModelSelection: %v", err)
		}
		if usedPopup {
			t.Fatal("expected legacy model selection path without popup")
		}
	})

	if selected != "model_12" {
		t.Fatalf("expected second item on page 2, got %q", selected)
	}
	for _, expected := range []string{"第 1/2 页", "第 2/2 页", "model_11", "model_15", "n/p 翻页"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected legacy output to contain %q, got:\n%s", expected, output)
		}
	}
}
