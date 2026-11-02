package commands

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestExecuteStructuredCompactCommandShowsCompactingStatus 固化 /compact 的
// 同步执行反馈：压缩在跑时动态状态行必须是 running 的 "Compacting context…"，
// 完成后恢复 Ready，不能让用户面对一个无反馈的界面。
func TestExecuteStructuredCompactCommandShowsCompactingStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	original := runManualChatCompact
	t.Cleanup(func() { runManualChatCompact = original })

	started := make(chan struct{})
	release := make(chan struct{})
	runManualChatCompact = func(*ChatSession, string) (*chatCompactReport, error) {
		close(started)
		<-release
		return &chatCompactReport{
			RequestedMode: compactruntime.ModeAuto,
			Status: compactruntime.Status{
				Mode:   compactruntime.ModeAuto,
				Reason: "below_limit",
			},
		}, nil
	}

	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	done := make(chan CommandResult, 1)
	go func() {
		done <- executeStructuredCompactCommand(session, "/compact")
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("/compact did not reach the compact runner")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		coordinator.mu.Lock()
		kind := coordinator.surfaceStatus.kind
		coordinator.mu.Unlock()
		if kind == chatSurfaceStatusCompacting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("compact running status = %v, want compacting", kind)
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("/compact did not finish after release")
	}
	coordinator.mu.Lock()
	kind := coordinator.surfaceStatus.kind
	coordinator.mu.Unlock()
	if kind == chatSurfaceStatusCompacting {
		t.Fatal("compacting status was not cleared after /compact finished")
	}
}

func TestCompactRefreshesContextStatusAndDynamicActivity(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	original := runManualChatCompact
	t.Cleanup(func() { runManualChatCompact = original })

	started := make(chan struct{})
	release := make(chan struct{})
	runManualChatCompact = func(*ChatSession, string) (*chatCompactReport, error) {
		close(started)
		<-release
		return &chatCompactReport{
			RequestedMode: compactruntime.ModeAuto,
			Result: &compactruntime.Result{
				Mode:              compactruntime.ModeAuto,
				TokenBefore:       5000,
				TokenAfter:        120,
				CompactedMessages: 4,
			},
			Status: compactruntime.Status{
				Mode:             compactruntime.ModeAuto,
				MaxContextTokens: 10000,
			},
		}, nil
	}

	session := &ChatSession{
		ContextTokenCount:       5000,
		ContextWindowTokenCount: 10000,
	}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(96, 24)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.RefreshStatus("")
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	initial := statusModelPlainText(coordinator.uiActor.AppState().Bottom.StatusModel, 160)
	if !strings.Contains(initial, "50%") || !strings.Contains(initial, "Context") {
		t.Fatalf("initial context status = %q, want Context 50%% used", initial)
	}

	done := make(chan CommandResult, 1)
	go func() {
		done <- executeStructuredCompactCommand(session, "/compact")
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("/compact did not reach the compact runner")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		coordinator.waitUIActorIdle()
		state := coordinator.uiActor.AppState()
		row := statusModelPlainText(state.Bottom.DynamicStatusModel, 160)
		if strings.Contains(row, "Compacting context") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dynamic status during compact = %q, want Compacting context", row)
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("/compact did not finish after release")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	final := statusModelPlainText(state.Bottom.StatusModel, 160)
	if !strings.Contains(final, "1%") || !strings.Contains(final, "Context") {
		t.Fatalf("context status after compact = %q, want Context 1%% used", final)
	}
	if row := statusModelPlainText(state.Bottom.DynamicStatusModel, 160); strings.Contains(row, "Compacting context") {
		t.Fatalf("dynamic status still shows compacting after completion: %q", row)
	}
}

func TestFormatChatCompactReport_MissingModelCapabilityIncludesConfigHint(t *testing.T) {
	report := &chatCompactReport{
		RequestedMode: compactruntime.ModeLocal,
		Status: compactruntime.Status{
			Mode:             compactruntime.ModeLocal,
			Reason:           "missing_model_capability",
			ResolvedProvider: "codex_cli_gpt",
			ResolvedModel:    "gpt-5.5",
			TokenBefore:      3743238,
		},
	}

	output := formatChatCompactReport(report)
	if !strings.Contains(output, "reason=missing_model_capability") {
		t.Fatalf("expected missing_model_capability reason, got %q", output)
	}
	if !strings.Contains(output, "`providers.items.codex_cli_gpt.model_capabilities.gpt-5.5`") {
		t.Fatalf("expected concrete model capability path hint, got %q", output)
	}
	if !strings.Contains(output, "`max_context_tokens` / `auto_compact_token_limit`") {
		t.Fatalf("expected compact config fields hint, got %q", output)
	}
	if !strings.Contains(output, "token_source="+compactTokenSourceObservedUsage) {
		t.Fatalf("expected token source hint, got %q", output)
	}
}

func TestFormatChatCompactReport_SuccessIncludesObservedUsageTokenSource(t *testing.T) {
	report := &chatCompactReport{
		Result: &compactruntime.Result{
			Mode:               compactruntime.ModeLocal,
			TokenBefore:        900,
			TokenAfter:         120,
			CompactedMessages:  4,
			ReplacementHistory: []types.Message{},
		},
	}

	output := formatChatCompactReport(report)
	if !strings.Contains(output, "token_source="+compactTokenSourceObservedUsage) {
		t.Fatalf("expected token source hint, got %q", output)
	}
}

func TestFormatChatCompactReport_SuccessIncludesCompactLineage(t *testing.T) {
	report := &chatCompactReport{
		Result: &compactruntime.Result{
			Mode:               compactruntime.ModeLocal,
			TokenBefore:        900,
			TokenAfter:         120,
			CompactedMessages:  4,
			ReplacementHistory: []types.Message{},
		},
		Generation: 2,
		Title:      "检查登录流程为什么失败",
		RootTitle:  "检查登录流程为什么失败",
	}

	output := formatChatCompactReport(report)
	if !strings.Contains(output, "generation=2") {
		t.Fatalf("expected generation in compact report, got %q", output)
	}
	if !strings.Contains(output, "title=检查登录流程为什么失败") {
		t.Fatalf("expected title in compact report, got %q", output)
	}
	if strings.Contains(output, "root_title=") {
		t.Fatalf("did not expect root_title when title is present, got %q", output)
	}
}

func TestAttachChatCompactLineageReadsRuntimeSession(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.Metadata.Title = "检查登录流程为什么失败"
	runtimeSession.Metadata.Context = map[string]interface{}{
		runtimechat.ContextCompactGeneration: 1,
		runtimechat.ContextCompactRootTitle:  "检查登录流程为什么失败",
	}
	report := &chatCompactReport{}
	attachChatCompactLineage(report, &ChatSession{RuntimeSession: runtimeSession})
	if report.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", report.Generation)
	}
	if report.RootTitle != "检查登录流程为什么失败" {
		t.Fatalf("expected root title, got %q", report.RootTitle)
	}
	if report.Title != "检查登录流程为什么失败" {
		t.Fatalf("expected session title, got %q", report.Title)
	}
}
