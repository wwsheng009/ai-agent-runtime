package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
)

// TestDebugRenderOutputSection：/debug Render Output 节在 gateway-backed
// session 上渲染 primary/mirror 指标；未 attach 时显示 not attached。
func TestDebugRenderOutputSection(t *testing.T) {
	// 构造带 gateway-backed TerminalSession 的 session。
	sink := outputpkg.NewMemorySink(outputpkg.TargetDescriptor{
		SinkID:             "dbg-primary",
		Class:              outputpkg.TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	gw, err := outputpkg.NewRenderOutputGateway("dbg-"+randSuffix(), outputpkg.RenderGatewayOptions{
		Clock:                 outputpkg.SystemClock{},
		CloseTimeout:          3 * time.Second,
		ReconfigureTimeout:    3 * time.Second,
		MaxIntentBytes:        1 << 20,
		MirrorQueueCapacity:   8,
		DeliveryJournalLimit:  outputpkg.JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		EventJournalLimit:     outputpkg.JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		MaxSubscriptions:      8,
		MaxSubscriptionBuffer: 32,
	}, outputpkg.RenderRouteConfig{
		Primary:            sink,
		PrimaryOwnership:   outputpkg.SinkOwned,
		ProjectionTargetID: "pt-primary",
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	session := &ChatSession{TerminalSession: ui.NewTerminalSessionWithOutput(gw)}
	// 产生一两笔 primary。
	_ = gw.Submit(context.Background(), outputpkg.RenderIntent{
		IntentID: "dbg-1",
		Kind:     outputpkg.TransactionFrame,
		Bytes:    []byte("hello"),
	})

	doc := buildDebugRenderOutputDocument(session)
	text := renderDocumentText(doc)
	if !strings.Contains(text, "Render Output:") {
		t.Fatalf("missing Render Output heading:\n%s", text)
	}
	if !strings.Contains(text, "committed=1") {
		t.Fatalf("missing primary counters:\n%s", text)
	}
	if !strings.Contains(text, "Last Sequence:") {
		t.Fatalf("missing sequence:\n%s", text)
	}

	// 未 attach：无 gateway 的 session。
	plain := &ChatSession{}
	doc2 := buildDebugRenderOutputDocument(plain)
	text2 := renderDocumentText(doc2)
	if !strings.Contains(text2, "not attached") && !strings.Contains(text2, "<none>") {
		t.Fatalf("unattached session must say not attached:\n%s", text2)
	}
}

// buildDebugRenderOutputDocument 包装 appendChatDebugRenderOutputLines。
func buildDebugRenderOutputDocument(session *ChatSession) render.Document {
	var builder chatDebugDocumentBuilder
	appendChatDebugRenderOutputLines(&builder, session)
	return builder.document()
}

// renderDocumentText 用 plain backend 渲染文档为文本。
func renderDocumentText(doc render.Document) string {
	return (render.PlainBackend{}).Render(doc)
}

func randSuffix() string {
	return string(rune('a'+time.Now().UnixNano()%26)) + "s"
}
