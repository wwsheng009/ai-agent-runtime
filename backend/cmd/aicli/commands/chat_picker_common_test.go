package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// Tests for the shared full-screen searchable picker components extracted from
// /model and reused by /login (chat_picker_common.go).

func TestNormalizeChatPickerOptions(t *testing.T) {
	got := normalizeChatPickerOptions([]string{
		"  Beta  ", "alpha", "gamma", "beta", "ALPHA", "", "   ", "alpha",
	})
	want := []string{"alpha", "Beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("expected %d options, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("option %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestNormalizeChatPickerOptionsStableTieBreak(t *testing.T) {
	// Case-variants collapse to the first occurrence (dedupe is
	// case-insensitive) and the rest sorts case-insensitively, so the same
	// provider/model appears once and in a deterministic order.
	got := normalizeChatPickerOptions([]string{"b", "A", "a"})
	want := []string{"A", "b"}
	if len(got) != len(want) {
		t.Fatalf("expected %d options, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("option %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestBuildChatPickerItems(t *testing.T) {
	items := buildChatPickerItems([]string{"alpha", "beta"}, "beta", "provider", "provider")
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if !strings.Contains(items[1].Title, "(当前)") {
		t.Fatalf("current item must be marked, got title %q", items[1].Title)
	}
	if strings.Contains(items[0].Title, "(当前)") {
		t.Fatalf("non-current item must not be marked, got title %q", items[0].Title)
	}
	if items[0].Detail != "provider" || items[0].SearchText != "provider alpha" {
		t.Fatalf("unexpected item projection: %+v", items[0])
	}
	if items[1].SearchText != "provider beta" {
		t.Fatalf("unexpected current item projection: %+v", items[1])
	}
}

func TestBuildChatPickerItemsNoSearchPrefix(t *testing.T) {
	items := buildChatPickerItems([]string{"alpha"}, "", "", "")
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].SearchText != "alpha" || items[0].Detail != "" {
		t.Fatalf("prefix-less projection must keep raw search text, got %+v", items[0])
	}
}

func TestChatPickerStageEmptyItemsIsError(t *testing.T) {
	// The stage must fail closed on an empty item list (before touching the
	// session/lease) so callers decide whether to fall back to text input.
	_, _, err := chatPickerStage(context.Background(), nil, nil, ui.FullScreenListOptions{})
	if err == nil || !strings.Contains(err.Error(), "没有可选项") {
		t.Fatalf("expected empty-options error, got %v", err)
	}
}
