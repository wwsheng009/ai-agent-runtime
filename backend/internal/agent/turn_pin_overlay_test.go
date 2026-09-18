package agent

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P3：回合级 pin（system 消息 / 工具叠加）的 context 契约。
// 覆盖：叠加结果不修改基础工具面；context 往返保持副本语义。

func TestOverlayTurnPinnedToolsReplacesAndAppends(t *testing.T) {
	base := []types.ToolDefinition{
		{Name: "a", Description: "base-a"},
		{Name: "b", Description: "base-b"},
	}
	pinned := []types.ToolDefinition{
		{Name: "B", Description: "pin-b"},
		{Name: "c", Description: "pin-c"},
	}

	merged := overlayTurnPinnedTools(base, pinned)
	if len(merged) != 3 {
		t.Fatalf("merged len=%d, want 3: %#v", len(merged), merged)
	}
	if merged[0].Name != "a" || merged[0].Description != "base-a" {
		t.Fatalf("base order/definition lost: %#v", merged[0])
	}
	if merged[1].Name != "B" || merged[1].Description != "pin-b" {
		t.Fatalf("same-name pin must replace the base definition: %#v", merged[1])
	}
	if merged[2].Name != "c" || merged[2].Description != "pin-c" {
		t.Fatalf("new pin must append: %#v", merged[2])
	}
	if base[1].Name != "b" || base[1].Description != "base-b" {
		t.Fatalf("base slice was mutated: %#v", base)
	}

	if got := overlayTurnPinnedTools(base, nil); len(got) != len(base) {
		t.Fatalf("empty pin must return base unchanged, got %#v", got)
	}
}

func TestTurnContextCarriesSystemMessagesAndPinnedTools(t *testing.T) {
	ctx := WithTurnSystemMessages(context.Background(), []types.Message{
		{Role: "system", Content: "Skill program guide: demo"},
	})
	ctx = WithTurnPinnedTools(ctx, []types.ToolDefinition{
		{Name: "skill__demo", Description: "pinned"},
	})

	messages := turnSystemMessagesFromContext(ctx)
	if len(messages) != 1 || messages[0].Role != "system" || messages[0].Content != "Skill program guide: demo" {
		t.Fatalf("turn system messages round-trip failed: %#v", messages)
	}
	tools := turnPinnedToolsFromContext(ctx)
	if len(tools) != 1 || tools[0].Name != "skill__demo" {
		t.Fatalf("turn pinned tools round-trip failed: %#v", tools)
	}

	if messages2 := turnSystemMessagesFromContext(context.Background()); len(messages2) != 0 {
		t.Fatalf("plain context must not carry turn system messages: %#v", messages2)
	}
	if tools2 := turnPinnedToolsFromContext(context.Background()); len(tools2) != 0 {
		t.Fatalf("plain context must not carry pinned tools: %#v", tools2)
	}
}
