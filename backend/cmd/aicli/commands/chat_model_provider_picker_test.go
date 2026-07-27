package commands

import (
	"fmt"
	"strings"
	"testing"
)

func TestNewModelCommandProviderPickerStateStartsOnPreferredPage(t *testing.T) {
	options := make([]string, 0, 25)
	for i := 1; i <= 25; i++ {
		options = append(options, fmt.Sprintf("provider_%02d", i))
	}

	state := newModelCommandProviderPickerState(options, "provider_23", "", 10)
	items, page, pageCount, total := state.pageWindow()
	if page != 2 || pageCount != 3 || total != 25 {
		t.Fatalf("unexpected page window: page=%d pageCount=%d total=%d", page, pageCount, total)
	}
	if len(items) != 5 || items[0] != "provider_21" || items[2] != "provider_23" {
		t.Fatalf("expected preferred provider page, got %#v", items)
	}
}

func TestApplyModelCommandProviderPickerInputPagesAndSelectsCurrentPageNumber(t *testing.T) {
	options := make([]string, 0, 23)
	for i := 1; i <= 23; i++ {
		options = append(options, fmt.Sprintf("provider_%02d", i))
	}
	state := newModelCommandProviderPickerState(options, "provider_01", "", 10)

	state, result := applyModelCommandProviderPickerInput(state, "n", "provider_01")
	if result.Done || !result.Redraw || state.Page != 1 {
		t.Fatalf("expected next page redraw, state=%+v result=%+v", state, result)
	}

	_, result = applyModelCommandProviderPickerInput(state, "2", "provider_11")
	if !result.Done || result.Selected != "provider_12" {
		t.Fatalf("expected current-page item 2 to select provider_12, got %+v", result)
	}
}

func TestApplyModelCommandProviderPickerInputSearchesAndClears(t *testing.T) {
	options := []string{"azure_openai", "deepseek", "openai", "openai_proxy", "vertex"}
	state := newModelCommandProviderPickerState(options, "vertex", "", 2)

	state, result := applyModelCommandProviderPickerInput(state, "/openai", "vertex")
	if result.Done || !result.Redraw || state.Filter != "openai" || state.Page != 0 {
		t.Fatalf("expected provider search redraw, state=%+v result=%+v", state, result)
	}
	items, page, pageCount, total := state.pageWindow()
	if page != 0 || pageCount != 2 || total != 3 {
		t.Fatalf("unexpected filtered window: items=%#v page=%d pageCount=%d total=%d", items, page, pageCount, total)
	}
	if len(items) != 2 || items[0] != "openai" || items[1] != "openai_proxy" {
		t.Fatalf("expected ranked openai matches on first page, got %#v", items)
	}

	state, result = applyModelCommandProviderPickerInput(state, "c", "openai")
	if result.Done || !result.Redraw || state.Filter != "" {
		t.Fatalf("expected search clear redraw, state=%+v result=%+v", state, result)
	}
	_, page, pageCount, total = state.pageWindow()
	if page != 2 || pageCount != 3 || total != len(options) {
		t.Fatalf("expected clear to restore preferred provider page, page=%d pageCount=%d total=%d", page, pageCount, total)
	}
}

func TestApplyModelCommandProviderPickerInputSupportsUniqueFuzzyAndReservedProviderNames(t *testing.T) {
	state := newModelCommandProviderPickerState([]string{"deepseek", "n", "openai"}, "openai", "", 10)

	_, result := applyModelCommandProviderPickerInput(state, "deep", "openai")
	if !result.Done || result.Selected != "deepseek" {
		t.Fatalf("expected unique fuzzy search to select deepseek, got %+v", result)
	}

	_, result = applyModelCommandProviderPickerInput(state, "n", "openai")
	if !result.Done || result.Selected != "n" {
		t.Fatalf("expected exact provider named n to win over next-page command, got %+v", result)
	}
}

func TestRenderModelCommandProviderPickerPopupLinesShowsOnlyCurrentPage(t *testing.T) {
	options := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		options = append(options, fmt.Sprintf("provider_%02d", i))
	}
	state := newModelCommandProviderPickerState(options, "provider_01", "", 5)
	lines := renderModelCommandProviderPickerPopupLines(state, "provider_01", "provider_01", "", "", "", 0)
	rendered := strings.Join(lines, "\n")

	for _, expected := range []string{"共 12", "第 1/3 页", "provider_01", "provider_05", "n/p 翻页", "编号按当前页"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected popup to contain %q, got:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "provider_06") || strings.Contains(rendered, "provider_12") {
		t.Fatalf("expected popup to be limited to first page, got:\n%s", rendered)
	}
}
