package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestInitialRuntimeSelectionIndex_PrefersCurrentThenDefault(t *testing.T) {
	options := []string{"alpha", "beta", "gamma"}

	if got := initialRuntimeSelectionIndex(options, "beta", "alpha"); got != 1 {
		t.Fatalf("expected current match index 1, got %d", got)
	}
	if got := initialRuntimeSelectionIndex(options, "", "gamma"); got != 2 {
		t.Fatalf("expected default option index 2, got %d", got)
	}
	if got := initialRuntimeSelectionIndex(options, "missing", "also-missing"); got != 0 {
		t.Fatalf("expected fallback to first option, got %d", got)
	}
	if got := initialRuntimeSelectionIndex(nil, "beta", "alpha"); got != -1 {
		t.Fatalf("expected empty options to return -1, got %d", got)
	}
}

func TestRuntimeSelectionControllerNavigate_WrapsAndClamps(t *testing.T) {
	var rendered []int
	controller := newRuntimeSelectionController(nil, ui.PopupHandle{}, "prompt", []string{"a", "b", "c"}, 1, func(selected int, warning string) []string {
		rendered = append(rendered, selected)
		return []string{warning}
	})

	controller.Navigate(1)
	if controller.Selected() != 2 {
		t.Fatalf("expected navigate +1 to select index 2, got %d", controller.Selected())
	}
	controller.Navigate(1)
	if controller.Selected() != 0 {
		t.Fatalf("expected navigate +1 to wrap to 0, got %d", controller.Selected())
	}
	controller.Navigate(-1)
	if controller.Selected() != 2 {
		t.Fatalf("expected navigate -1 to wrap to 2, got %d", controller.Selected())
	}
	if len(rendered) != 3 {
		t.Fatalf("expected 3 re-renders, got %d", len(rendered))
	}

	option, ok := controller.SelectedOption()
	if !ok || option != "c" {
		t.Fatalf("expected selected option c, got %q ok=%v", option, ok)
	}
}

func TestResolveRuntimeSelectionInputWithCursor_BlankUsesHighlighted(t *testing.T) {
	options := []string{"gpt-4.1", "gpt-4.1-mini", "custom-model"}

	if got, ok := resolveRuntimeSelectionInputWithCursor("", "gpt-4.1", "", options, 1, true, false); !ok || got != "gpt-4.1-mini" {
		t.Fatalf("expected blank input to use highlighted option, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeSelectionInputWithCursor("3", "gpt-4.1", "", options, 0, true, false); !ok || got != "custom-model" {
		t.Fatalf("expected typed number to still resolve, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeSelectionInputWithCursor("custom-model", "gpt-4.1", "", options, 0, true, false); !ok || got != "custom-model" {
		t.Fatalf("expected typed name to still resolve, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeSelectionInputWithCursor("", "gpt-4.1", "", options, -1, true, false); !ok || got != "gpt-4.1" {
		t.Fatalf("expected invalid cursor to fall back to current, got %q ok=%v", got, ok)
	}
}

func TestResolveRuntimeReasoningEffortInputWithCursor_BlankUsesHighlighted(t *testing.T) {
	options := []string{"high", "max"}

	if got, ok := resolveRuntimeReasoningEffortInputWithCursor("", "high", true, "", options, 1); !ok || got != "max" {
		t.Fatalf("expected blank input to use highlighted effort, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeReasoningEffortInputWithCursor("0", "high", true, "", options, 1); !ok || got != "" {
		t.Fatalf("expected clear token to empty effort, got %q ok=%v", got, ok)
	}
}

func TestRenderSelectionPopupLines_HighlightsSelectedRow(t *testing.T) {
	lines := renderSelectionPopupLines(
		"选择 Provider",
		"provider",
		"openai",
		[]string{"openai", "deepseek"},
		"openai",
		"",
		"  提示: ↑↓ 选择，回车确认高亮项；也可输入编号或名称",
		"",
		"",
		1,
	)

	rendered := strings.Join(lines, "\n")
	if !strings.Contains(rendered, " >[2] deepseek") && !strings.Contains(rendered, ">[2] deepseek") {
		t.Fatalf("expected selected marker on second option, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "  [1] openai") && !strings.Contains(rendered, " [1] openai") {
		t.Fatalf("expected non-selected first option without >, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "↑↓ 选择") {
		t.Fatalf("expected navigation hint, got:\n%s", rendered)
	}
}
