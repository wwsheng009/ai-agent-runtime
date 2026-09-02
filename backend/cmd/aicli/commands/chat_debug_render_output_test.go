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

// TestDebugRenderOutputRecentDeliveries：/debug Render Output 节渲染最近 N 笔
// DeliveryRecord 摘要；payload 以 hash 呈现，明文 bytes 绝不出现。
func TestDebugRenderOutputRecentDeliveries(t *testing.T) {
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

	// 三笔不同 payload 的 primary（含敏感明文，必须在 /debug 中不可见）。
	const secret = "super-secret-token-abc123"
	for i := 0; i < 3; i++ {
		payload := []byte("dbg-payload-" + string(rune('a'+i)))
		if i == 1 {
			payload = []byte(secret)
		}
		_ = gw.Submit(context.Background(), outputpkg.RenderIntent{
			IntentID: "dbg-r" + string(rune('0'+i)),
			Kind:     outputpkg.TransactionFrame,
			Bytes:    payload,
		})
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := gw.Drain(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	text := renderDocumentText(buildDebugRenderOutputDocument(session))
	if !strings.Contains(text, "Recent Deliveries:") {
		t.Fatalf("missing Recent Deliveries heading:\n%s", text)
	}
	// 摘要行：seq/batch/kind/primary/payload mode。
	if !strings.Contains(text, "payload=hash_only") {
		t.Fatalf("delivery summary must show hash_only payload mode:\n%s", text)
	}
	if !strings.Contains(text, "primary=committed") {
		t.Fatalf("delivery summary must show primary status:\n%s", text)
	}
	if !strings.Contains(text, "kind=frame") {
		t.Fatalf("delivery summary must show transaction kind:\n%s", text)
	}
	// 明文 payload 不得出现在 /debug 输出中。
	if strings.Contains(text, secret) {
		t.Fatalf("plaintext payload leaked into /debug:\n%s", text)
	}
	if strings.Contains(text, "dbg-payload-") {
		t.Fatalf("plaintext payload leaked into /debug:\n%s", text)
	}
}

// TestDebugRenderOutputRecentDeliveriesCap：摘要条数受 cap 限制，且每笔
// payload 只以 hash 呈现（无明文、无截断原文）。
func TestDebugRenderOutputRecentDeliveriesCap(t *testing.T) {
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

	for i := 0; i < 12; i++ {
		_ = gw.Submit(context.Background(), outputpkg.RenderIntent{
			IntentID: "dbg-c" + string(rune('0'+i)),
			Kind:     outputpkg.TransactionFrame,
			Bytes:    []byte("cap-payload-" + string(rune('0'+i))),
		})
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := gw.Drain(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	text := renderDocumentText(buildDebugRenderOutputDocument(session))
	count := strings.Count(text, "record=rd-")
	if count == 0 {
		t.Fatalf("missing delivery summaries:\n%s", text)
	}
	if count > chatDebugRecentDeliveryCap {
		t.Fatalf("delivery summary cap exceeded: %d > %d\n%s", count, chatDebugRecentDeliveryCap, text)
	}
	// payload 明文与部分原文都不得出现。
	if strings.Contains(text, "cap-payload-") {
		t.Fatalf("plaintext payload leaked into /debug:\n%s", text)
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
