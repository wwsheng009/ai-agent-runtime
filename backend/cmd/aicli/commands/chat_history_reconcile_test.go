package commands

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// A runtime event log can be persisted after only a prefix of a turn while
// the canonical session store already contains the full transcript. A nonempty
// Scene must therefore not suppress persisted-history import, and repeated
// resume/history presentation must not append a second copy.
func TestPrintVisibleChatHistory_UnifiedReconcilesPartialEventLogIdempotently(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}

	// Simulate an event log that restored only the first of two identical user
	// inputs. Content alone is insufficient as an identity: reconcile must
	// retain the restored occurrence and import the second occurrence exactly
	// once, together with the missing assistant rows.
	bridge.submitUserInput("repeat")
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("repeat"),
		*runtimetypes.NewAssistantMessage("first response"),
		*runtimetypes.NewUserMessage("repeat"),
		*runtimetypes.NewAssistantMessage("second response"),
	})

	if got := printVisibleChatHistory(session, "已加载历史会话"); got != 4 {
		t.Fatalf("visible history count=%d want 4", got)
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	assertPersistedHistoryScene(t, bridge.sceneSnapshot())

	if got := printVisibleChatHistory(session, "已加载历史会话"); got != 4 {
		t.Fatalf("second visible history count=%d want 4", got)
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	assertPersistedHistoryScene(t, bridge.sceneSnapshot())

	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified history reconcile populated legacy historyWindow: %#v", got)
	}
	if terminal.Len() == 0 {
		t.Fatal("TerminalSession did not render reconciled history")
	}
}

func TestReplaceCanonicalHistoryProjectionDiscardsTruncatedTurnsAcrossEventLogReplay(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.eventLogPathOverride = logPath
	bridge.submitUserInput("removed user turn")
	bridge.submitAssistant("removed assistant answer")

	canonical := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("surviving user turn"),
		*runtimetypes.NewAssistantMessage("surviving assistant answer"),
	}
	if !bridge.replaceCanonicalHistoryProjection(canonical, "已回退到 user turn 0：上方旧消息已失效") {
		t.Fatal("canonical history replacement was rejected without an active run")
	}
	bridge.submitCommand("backtrack apply: turn=0")
	assertCanonicalBacktrackProjection(t, bridge.sceneSnapshot())

	replayed := newChatRuntimeEventBridge(&ChatSession{})
	replayed.eventLogPathOverride = logPath
	if count, err := replayed.replayEventLog(); err != nil || count != 4 {
		t.Fatalf("replay history-reset log = (%d, %v), want 4 records and nil error", count, err)
	}
	assertCanonicalBacktrackProjection(t, replayed.sceneSnapshot())
}

func assertCanonicalBacktrackProjection(t *testing.T, snapshot *scene.Snapshot) {
	t.Helper()
	if snapshot == nil {
		t.Fatal("missing canonical backtrack Scene snapshot")
	}
	var text strings.Builder
	for _, cell := range snapshot.Cells {
		if cell != nil {
			text.WriteString(cell.Source)
			text.WriteByte('\n')
		}
	}
	got := text.String()
	for _, want := range []string{
		"已回退到 user turn 0：上方旧消息已失效",
		"surviving user turn",
		"surviving assistant answer",
		"backtrack apply: turn=0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("canonical projection is missing %q:\n%s", want, got)
		}
	}
	for _, removed := range []string{"removed user turn", "removed assistant answer"} {
		if strings.Contains(got, removed) {
			t.Fatalf("truncated history reappeared as %q:\n%s", removed, got)
		}
	}
}

// Startup resume is the production history path. Keep this separate from the
// reconcile unit test above: it proves that canonical history survives the
// startup status cells, enters the unified actor, and is physically painted by
// TerminalSession with the same Markdown projection as a live assistant reply.
func TestPresentChatStartupSession_UnifiedRendersCanonicalHistoryWithMarkdown(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	assistant := runtimetypes.NewAssistantMessage("# Resumed answer\n\n- **complete**\n- `code`")
	runtimetypes.SetReasoningBlock(assistant.Metadata, &runtimetypes.ReasoningBlock{
		Summary:    "reviewed the resumed context",
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})
	loaded := runtimechat.NewSession("tester")
	loaded.ID = "unified-startup-history"
	loaded.Metadata.Title = "Unified history"
	loaded.ReplaceHistory([]runtimetypes.Message{
		*runtimetypes.NewUserMessage("continue the previous task"),
		*assistant,
	})

	session := &ChatSession{RuntimeSession: loaded}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(56, 18)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, loaded.GetMessages()); err != nil {
		t.Fatalf("restore runtime messages: %v", err)
	}

	presentChatStartupSession(session, &chatCommandOptions{OutputFormat: "interactive"}, loaded)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	assertTranscriptSourceCount(t, state.Transcript.Cells, "continue the previous task", 1)
	assertTranscriptSourceCount(t, state.Transcript.Cells, "reviewed the resumed context", 1)
	assertTranscriptSourceCount(t, state.Transcript.Cells, "# Resumed answer\n\n- **complete**\n- `code`", 1)
	if session.TerminalSession == nil || session.TerminalSession.ProjectionState().Frame == 0 {
		t.Fatal("startup history was not painted by TerminalSession")
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("startup history populated legacy historyWindow: %#v", got)
	}

	output := terminal.String()
	for _, want := range []string{"continue the previous task", "reviewed the resumed context", "Resumed answer", "complete", "code"} {
		if !strings.Contains(output, want) {
			t.Fatalf("TerminalSession startup output missing %q: %q", want, output)
		}
	}
	if strings.Contains(output, "# Resumed answer") || strings.Contains(output, "**complete**") {
		t.Fatalf("TerminalSession startup output leaked raw Markdown: %q", output)
	}
	screen := newScreenVT(56, 18)
	screen.feed(output)
	primary := strings.Join(screen.Lines(1, ui.LayoutAppScreen(state).OutputBottomRow), "\n")
	for _, want := range []string{"Resumed answer", "complete", "code"} {
		if !strings.Contains(primary, want) {
			t.Fatalf("startup primary viewport is missing rendered Markdown %q:\n%s", want, screen.dump())
		}
	}
	if strings.Contains(primary, "# Resumed answer") || strings.Contains(primary, "**complete**") {
		t.Fatalf("startup primary viewport retained raw Markdown:\n%s", screen.dump())
	}
}

// A startup can restore canonical Messages before a runtime host has supplied
// a loaded-session handle. The handle controls resume metadata only; it must
// never suppress the primary transcript projection.
func TestPresentChatStartupSession_UnifiedSeedsHistoryWithoutLoadedRuntimeHandle(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	const (
		width  = 56
		height = 14
	)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("startup canonical user without runtime handle"),
		*runtimetypes.NewAssistantMessage("startup canonical assistant without runtime handle"),
	}); err != nil {
		t.Fatalf("restore canonical history: %v", err)
	}

	presentChatStartupSession(session, &chatCommandOptions{OutputFormat: "interactive"}, nil)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	assertTranscriptSourceCount(t, state.Transcript.Cells, "startup canonical user without runtime handle", 1)
	assertTranscriptSourceCount(t, state.Transcript.Cells, "startup canonical assistant without runtime handle", 1)
	if strings.Contains(terminal.String(), "已恢复历史会话") {
		t.Fatalf("startup without a loaded handle rendered a false resume status: %q", terminal.String())
	}

	screen := newScreenVT(width, height)
	screen.feed(terminal.String())
	layout := ui.LayoutAppScreen(state)
	primary := strings.Join(screen.Lines(1, layout.OutputBottomRow), "\n")
	for _, want := range []string{
		"startup canonical user without runtime handle",
		"startup canonical assistant without runtime handle",
	} {
		if !strings.Contains(primary, want) {
			t.Fatalf("startup primary viewport is missing %q without loaded runtime handle:\n%s", want, screen.dump())
		}
	}
}

// A resume normally starts with far more rows than the retained primary
// viewport. This verifies that overflow is handed to TerminalSession scrollback
// through typed history effects instead of disappearing behind the fixed
// bottom pane or being replayed through FixedBottomSurface.
func TestPrintVisibleChatHistory_UnifiedHandoffsOverflowedCanonicalHistory(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	messages := make([]runtimetypes.Message, 0, 16)
	for index := 1; index <= 8; index++ {
		messages = append(messages,
			*runtimetypes.NewUserMessage(fmt.Sprintf("historical question %d", index)),
			*runtimetypes.NewAssistantMessage(fmt.Sprintf("historical answer %d", index)),
		)
	}
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(44, 8)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, messages); err != nil {
		t.Fatalf("restore runtime messages: %v", err)
	}
	if got := printVisibleChatHistory(session, "loaded history"); got != len(messages) {
		t.Fatalf("visible history count=%d want %d", got, len(messages))
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.State()
	if len(state.AppState.Transcript.Cells) != len(messages)+1 { // header + every canonical message
		t.Fatalf("transcript cells=%d want header plus %d history messages", len(state.AppState.Transcript.Cells), len(messages))
	}
	for index := 1; index <= 8; index++ {
		assertTranscriptSourceCount(t, state.AppState.Transcript.Cells, fmt.Sprintf("historical question %d", index), 1)
		assertTranscriptSourceCount(t, state.AppState.Transcript.Cells, fmt.Sprintf("historical answer %d", index), 1)
	}
	acked := 0
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == ui.HistoryCommitAcked {
			acked++
		}
	}
	if acked == 0 {
		t.Fatalf("overflowed history did not reach a TerminalSession history acknowledgement: %#v", state.HistoryEffects.Entries())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("overflowed unified history populated legacy historyWindow: %#v", got)
	}
	output := terminal.String()
	for _, want := range []string{"historical question 1", "historical answer 1", "historical question 8", "historical answer 8"} {
		if !strings.Contains(output, want) {
			t.Fatalf("TerminalSession history handoff omitted %q: %q", want, output)
		}
	}
}

// A byte-stream assertion only proves that history was emitted at some point.
// Reconstruct the primary terminal screen to guard the actual user-facing
// contract: retained finalized history remains visible while a new reasoning
// cell occupies the active band.
func TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning(t *testing.T) {
	const (
		width  = 52
		height = 15
	)
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	messages := make([]runtimetypes.Message, 0, 12)
	for index := 1; index <= 6; index++ {
		messages = append(messages,
			*runtimetypes.NewUserMessage(fmt.Sprintf("history user %d", index)),
			*runtimetypes.NewAssistantMessage(fmt.Sprintf("history assistant %d", index)),
		)
	}

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, messages); err != nil {
		t.Fatalf("restore runtime messages: %v", err)
	}
	if got := printVisibleChatHistory(session, "loaded history"); got != len(messages) {
		t.Fatalf("visible history count=%d want %d", got, len(messages))
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// Fill the bounded active band rather than testing only a single reasoning
	// row. The normal primary screen must still retain its finalized tail when
	// a long in-progress reasoning body occupies the full bottom allocation.
	activeReasoning := strings.Join([]string{
		"active reasoning line 01",
		"active reasoning line 02",
		"active reasoning line 03",
		"active reasoning line 04",
		"active reasoning line 05",
		"active reasoning line 06",
		"active reasoning line 07",
		"active reasoning line 08",
	}, "\n")
	// The production local loop emits dotted assistant.reasoning events with a
	// nested stream-delta ReasoningBlock. Leave it mutable so the assertion
	// covers the active-band plus retained-transcript composition.
	bridge.encodeRenderModelEvent(runtimeevents.Event{Type: "assistant.reasoning", Payload: map[string]interface{}{
		"trace_id": "primary-history-active-reasoning",
		"reasoning": map[string]interface{}{
			"format":  "stream_delta",
			"summary": activeReasoning,
		},
	}})
	coordinator.postTranscriptSnapshotFromBridge(bridge)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellMutable || !strings.HasSuffix(state.Active.Source, activeReasoning) {
		t.Fatalf("active reasoning was not projected from AppState: %+v", state.Active)
	}
	acked := 0
	for _, entry := range coordinator.uiActor.State().HistoryEffects.Entries() {
		if entry.State == ui.HistoryCommitAcked {
			acked++
		}
	}
	if acked == 0 {
		t.Fatal("overflowed primary history did not reach TerminalSession scrollback handoff")
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified primary history populated legacy historyWindow: %#v", got)
	}

	screen := newScreenVT(width, height)
	screen.feed(terminal.String())
	dump := screen.dump()
	layout := ui.LayoutAppScreen(state)
	primary := strings.Join(screen.Lines(1, layout.OutputBottomRow), "\n")
	for _, want := range []string{
		"history user 6",
		"history assistant 6",
	} {
		if !strings.Contains(primary, want) {
			t.Fatalf("primary history viewport is missing %q:\n%s", want, dump)
		}
	}
	active := strings.Join(screen.Lines(layout.OutputBottomRow+1, height), "\n")
	if !strings.Contains(active, "active reasoning line 08") {
		t.Fatalf("active reasoning band is missing from bottom pane:\n%s", dump)
	}
}

// The production setup mounts the fenced compatibility facade before the
// TerminalSession presenter. Keep that order in a screen-level regression:
// runtime-log replay, canonical persisted history, and a new mutable reasoning
// cell must converge into one frame source without dropping the transcript
// tail or redirecting history into the retired surface history window.
func TestUnifiedStartupOrderRetainsHistoryTailAndScrollback(t *testing.T) {
	const (
		width  = 52
		height = 15
	)
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	messages := make([]runtimetypes.Message, 0, 12)
	for index := 1; index <= 6; index++ {
		messages = append(messages,
			*runtimetypes.NewUserMessage(fmt.Sprintf("startup history user %d", index)),
			*runtimetypes.NewAssistantMessage(fmt.Sprintf("startup history assistant %d", index)),
		)
	}

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	// This matches buildChatSession: enable/fence the facade, mount it, then
	// attach the single TerminalSession writer. The mount must publish real
	// geometry before the presenter's initial recovery transaction.
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}

	if err := replaceRuntimeMessages(session, messages); err != nil {
		t.Fatalf("restore runtime messages: %v", err)
	}
	if got := printVisibleChatHistory(session, "loaded history"); got != len(messages) {
		t.Fatalf("visible history count=%d want %d", got, len(messages))
	}

	// A runtime-event Scene update after persisted reconcile is the sequence
	// that previously made real sessions appear to retain only reasoning.
	bridge.encodeRenderModelEvent(runtimeevents.Event{Type: "assistant.reasoning", Payload: map[string]interface{}{
		"trace_id": "startup-history-active-reasoning",
		"reasoning": map[string]interface{}{
			"format":  "stream_delta",
			"summary": "startup active reasoning remains visible",
		},
	}})
	coordinator.postTranscriptSnapshotFromBridge(bridge)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.State()
	if state.Geometry.Width != width || state.Geometry.Height != height || state.LayoutGeneration == 0 {
		t.Fatalf("startup geometry did not come from mounted surface: %+v", state.AppState.Geometry)
	}
	if state.Active.Phase != ui.ActiveCellMutable || !strings.HasSuffix(state.Active.Source, "startup active reasoning remains visible") {
		t.Fatalf("startup reasoning did not remain semantic active content: %+v", state.Active)
	}
	assertTranscriptSourceCount(t, state.AppState.Transcript.Cells, "startup history user 6", 1)
	assertTranscriptSourceCount(t, state.AppState.Transcript.Cells, "startup history assistant 6", 1)
	if session.TerminalSession == nil {
		t.Fatal("startup did not publish TerminalSession")
	}
	projection := session.TerminalSession.ProjectionState()
	if projection.Validity != renderengine.ProjectionKnown || projection.Geometry.Width != width || projection.Geometry.Height != height || projection.LayoutGeneration != state.LayoutGeneration {
		t.Fatalf("terminal projection does not match startup AppState: projection=%+v state=%+v", projection, state.AppState)
	}
	acked := 0
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == ui.HistoryCommitAcked {
			acked++
		}
	}
	if acked == 0 {
		t.Fatalf("startup history never reached native-scrollback handoff: %#v", state.HistoryEffects.Entries())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("startup history entered legacy historyWindow: %#v", got)
	}

	screen := newScreenVT(width, height)
	screen.feed(terminal.String())
	layout := ui.LayoutAppScreen(state.AppState)
	primary := strings.Join(screen.Lines(1, layout.OutputBottomRow), "\n")
	for _, want := range []string{"startup history user 6", "startup history assistant 6"} {
		if !strings.Contains(primary, want) {
			t.Fatalf("startup primary history viewport is missing %q:\n%s", want, screen.dump())
		}
	}
	active := strings.Join(screen.Lines(layout.OutputBottomRow+1, height), "\n")
	if !strings.Contains(active, "startup active reasoning remains visible") {
		t.Fatalf("startup active reasoning band is missing:\n%s", screen.dump())
	}
	scrollback := strings.Join(screen.ScrollbackLines(), "\n")
	if !strings.Contains(scrollback, "startup history user 1") {
		t.Fatalf("startup history prefix is absent from native scrollback:\n%s", screen.dump())
	}
}

// A resumed session has two independent recovery inputs: runtime-events.jsonl
// may contain an already-rendered prefix, while durable history is the full
// canonical transcript. Exercise the real bridge.start replay path and a
// subsequently published EventBus event so this sequence cannot regress into
// a reasoning-only primary frame or append the replayed prefix twice.
func TestUnifiedStartupReplaysEventLogThenReconcilesCanonicalHistoryWithoutDuplicates(t *testing.T) {
	const (
		width  = 52
		height = 14
	)
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	messages := make([]runtimetypes.Message, 0, 12)
	for index := 1; index <= 6; index++ {
		messages = append(messages,
			*runtimetypes.NewUserMessage(fmt.Sprintf("replayed canonical user %d", index)),
			*runtimetypes.NewAssistantMessage(fmt.Sprintf("replayed canonical assistant %d", index)),
		)
	}

	// Persist a real JSONL prefix through the same append-only producer used by
	// live runtime events. The restored bridge below must reconstruct this
	// prefix before canonical history fills in its missing suffix.
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	producer := newChatRuntimeEventBridge(&ChatSession{})
	producer.eventLogPathOverride = logPath
	producer.submitUserInput(messages[0].Content)
	producer.encodeRenderModelEvent(runtimeevents.Event{
		Type: runtimechat.EventAssistantMessage,
		Payload: map[string]interface{}{
			"content": messages[1].Content,
		},
	})

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "unified-replay-reconcile"
	eventBus := runtimeevents.NewBusWithRetention(16)
	session := &ChatSession{
		RuntimeSession:   runtimeSession,
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: eventBus},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.eventLogPathOverride = logPath
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	// Match buildChatSession's authority order: mount the fenced facade first,
	// then attach TerminalSession as the only terminal writer.
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}

	// ensureChatRuntimeEventBridge invokes bridge.start(), which calls the real
	// replayEventLog implementation before it subscribes to the runtime bus.
	if got := ensureChatRuntimeEventBridge(session); got != bridge {
		t.Fatal("startup replaced the prepared runtime-event bridge")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	_, _, replayed, failures := bridge.eventLogStats()
	if replayed != 2 || failures != 0 {
		t.Fatalf("runtime event log replay stats: replayed=%d failures=%d, want 2 and 0", replayed, failures)
	}

	if err := replaceRuntimeMessages(session, messages); err != nil {
		t.Fatalf("restore canonical runtime messages: %v", err)
	}
	if got := printVisibleChatHistory(session, "loaded canonical history"); got != len(messages) {
		t.Fatalf("visible history count=%d want %d", got, len(messages))
	}
	// Resume/history presentation can be requested more than once. The replayed
	// prefix and canonical suffix must remain a single semantic transcript.
	if got := printVisibleChatHistory(session, "loaded canonical history"); got != len(messages) {
		t.Fatalf("second visible history count=%d want %d", got, len(messages))
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// This reaches the live EventBus subscription established by bridge.start,
	// rather than injecting directly into the Scene. It is deliberately mutable
	// so the final frame must compose active reasoning below the history tail.
	eventBus.Publish(runtimeevents.Event{
		Type:      "assistant.reasoning",
		SessionID: runtimeSession.ID,
		Payload: map[string]interface{}{
			"trace_id": "unified-replay-live-reasoning",
			"reasoning": map[string]interface{}{
				"format":  "stream_delta",
				"summary": "live reasoning after replay remains visible",
			},
		},
	})
	bridge.WaitForCurrentEvents(2 * time.Second)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.State()
	if state.Geometry.Width != width || state.Geometry.Height != height || state.LayoutGeneration == 0 {
		t.Fatalf("startup geometry did not come from mounted surface: %+v", state.AppState.Geometry)
	}
	if state.Active.Phase != ui.ActiveCellMutable || !strings.HasSuffix(state.Active.Source, "live reasoning after replay remains visible") {
		t.Fatalf("live EventBus reasoning was not projected to the active band: %+v", state.Active)
	}
	if len(state.AppState.Transcript.Cells) != len(messages)+1 {
		t.Fatalf("reconciled transcript cells=%d want %d canonical messages plus the active reasoning cell: %#v", len(state.AppState.Transcript.Cells), len(messages), state.AppState.Transcript.Cells)
	}
	for index := 1; index <= 6; index++ {
		assertTranscriptSourceCount(t, state.AppState.Transcript.Cells, fmt.Sprintf("replayed canonical user %d", index), 1)
		assertTranscriptSourceCount(t, state.AppState.Transcript.Cells, fmt.Sprintf("replayed canonical assistant %d", index), 1)
	}
	if session.TerminalSession == nil {
		t.Fatal("startup did not publish TerminalSession")
	}
	projection := session.TerminalSession.ProjectionState()
	if projection.Validity != renderengine.ProjectionKnown || projection.Geometry.Width != width || projection.Geometry.Height != height || projection.LayoutGeneration != state.LayoutGeneration {
		t.Fatalf("terminal projection does not match reconciled AppState: projection=%+v state=%+v", projection, state.AppState)
	}
	acked := 0
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == ui.HistoryCommitAcked {
			acked++
		}
	}
	if acked == 0 {
		t.Fatalf("reconciled history never reached native-scrollback handoff: %#v", state.HistoryEffects.Entries())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified replay/reconcile populated legacy historyWindow: %#v", got)
	}

	screen := newScreenVT(width, height)
	screen.feed(terminal.String())
	layout := ui.LayoutAppScreen(state.AppState)
	primary := strings.Join(screen.Lines(1, layout.OutputBottomRow), "\n")
	for _, want := range []string{"replayed canonical user 6", "replayed canonical assistant 6"} {
		if !strings.Contains(primary, want) {
			t.Fatalf("primary history viewport is missing %q:\n%s", want, screen.dump())
		}
	}
	active := strings.Join(screen.Lines(layout.OutputBottomRow+1, height), "\n")
	if !strings.Contains(active, "live reasoning after replay remains visible") {
		t.Fatalf("active reasoning band is missing after replay/reconcile:\n%s", screen.dump())
	}
	scrollback := strings.Join(screen.ScrollbackLines(), "\n")
	for _, want := range []string{"replayed canonical user 1", "replayed canonical assistant 1"} {
		if !strings.Contains(scrollback, want) {
			t.Fatalf("native scrollback is missing replayed prefix %q:\n%s", want, screen.dump())
		}
	}
}

// A single persisted message can be taller than the primary output region.
// The historical prefix must be handed to native scrollback while its newest
// lines stay visible in the normal primary viewport. A transcript pager is an
// additional browser, not a substitute for either half of this contract.
func TestPrintVisibleChatHistory_UnifiedOversizedSingleCellPreservesScrollbackAndPrimaryTail(t *testing.T) {
	const (
		width  = 48
		height = 8
	)
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	var history strings.Builder
	for index := 1; index <= 16; index++ {
		chunk := fmt.Sprintf("unbroken-history-%02d", index)
		history.WriteString(chunk)
		history.WriteString(strings.Repeat("x", width-len(chunk)))
	}
	largeHistory := history.String()
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewAssistantMessage(largeHistory),
	}); err != nil {
		t.Fatalf("restore runtime messages: %v", err)
	}
	if got := printVisibleChatHistory(session, "loaded history"); got != 1 {
		t.Fatalf("visible history count=%d want 1", got)
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.State()
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("oversized unified history populated legacy historyWindow: %#v", got)
	}
	if len(state.HistoryEffects.Entries()) == 0 {
		t.Fatal("oversized single cell did not create history handoff entries")
	}

	screen := newScreenVT(width, height)
	screen.feed(terminal.String())
	layout := ui.LayoutAppScreen(state.AppState)
	primary := strings.Join(screen.Lines(1, layout.OutputBottomRow), "\n")
	scrollback := strings.Join(screen.ScrollbackLines(), "\n")
	if !strings.Contains(primary, "unbroken-history-16") {
		t.Fatalf("latest oversized-history tail is absent from primary viewport:\n%s", screen.dump())
	}
	if strings.Contains(primary, "unbroken-history-01") {
		t.Fatalf("old oversized-history prefix remained in primary viewport:\n%s", screen.dump())
	}
	if !strings.Contains(scrollback, "unbroken-history-01") {
		t.Fatalf("old oversized-history prefix was not handed to scrollback:\nprimary:\n%s\nscrollback:\n%s", screen.dump(), scrollback)
	}
	if strings.Contains(scrollback, "unbroken-history-16") {
		t.Fatalf("retained primary tail was duplicated into scrollback:\nprimary:\n%s\nscrollback:\n%s", screen.dump(), scrollback)
	}
}

// Rich Markdown follows the same primary/scrollback contract as plain text.
// The renderer can expand source blocks into several physical rows, so this
// exercises the renderer-fragment handoff rather than a raw-source fallback.
func TestPrintVisibleChatHistory_UnifiedOversizedMarkdownPreservesScrollbackAndPrimaryTail(t *testing.T) {
	const (
		width  = 48
		height = 8
	)
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	var history strings.Builder
	history.WriteString("# Markdown history\n\n")
	for index := 1; index <= 16; index++ {
		fmt.Fprintf(&history, "- **markdown-history-%02d**\n", index)
	}
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewAssistantMessage(history.String()),
	}); err != nil {
		t.Fatalf("restore Markdown runtime message: %v", err)
	}
	if got := printVisibleChatHistory(session, "loaded history"); got != 1 {
		t.Fatalf("visible Markdown history count=%d want 1", got)
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.State()
	if len(state.HistoryEffects.Entries()) == 0 {
		t.Fatal("oversized Markdown cell did not create history handoff entries")
	}
	layout := ui.LayoutAppScreen(state.AppState)
	if !strings.Contains(terminal.String(), fmt.Sprintf("\x1b[1;%dr", layout.OutputBottomRow)) {
		t.Fatalf("unified history writer did not emit the top history scroll region: %q", terminal.String())
	}
	if strings.Contains(terminal.String(), fmt.Sprintf("\x1b[1;%dr", height)) {
		t.Fatalf("history scroll region included the bottom inline viewport: %q", terminal.String())
	}
	screen := newScreenVT(width, height)
	screen.feed(terminal.String())
	primary := strings.Join(screen.Lines(1, layout.OutputBottomRow), "\n")
	scrollback := strings.Join(screen.ScrollbackLines(), "\n")
	if !strings.Contains(primary, "markdown-history-16") {
		t.Fatalf("latest Markdown history tail is absent from primary viewport:\n%s", screen.dump())
	}
	if strings.Contains(primary, "markdown-history-01") {
		t.Fatalf("old Markdown history prefix remained in primary viewport:\n%s", screen.dump())
	}
	if !strings.Contains(scrollback, "markdown-history-01") {
		t.Fatalf("old Markdown history prefix was not handed to scrollback:\nprimary:\n%s\nscrollback:\n%s", screen.dump(), scrollback)
	}
	if strings.Contains(scrollback, "markdown-history-16") {
		t.Fatalf("retained Markdown primary tail was duplicated into scrollback:\nprimary:\n%s\nscrollback:\n%s", screen.dump(), scrollback)
	}
	if strings.Contains(primary, "**markdown-history") || strings.Contains(scrollback, "**markdown-history") ||
		strings.Contains(primary, "# Markdown history") || strings.Contains(scrollback, "# Markdown history") {
		t.Fatalf("raw Markdown leaked into terminal history:\n%s", screen.dump())
	}
}

func assertTranscriptSourceCount(t *testing.T, cells []scene.TranscriptCell, want string, count int) {
	t.Helper()
	got := 0
	for _, cell := range cells {
		if cell.Source == want {
			got++
		}
	}
	if got != count {
		t.Fatalf("transcript source %q occurs %d times, want %d: %#v", want, got, count, cells)
	}
}

func assertPersistedHistoryScene(t *testing.T, snapshot *scene.Snapshot) {
	t.Helper()
	if snapshot == nil || len(snapshot.Cells) != 4 {
		count := 0
		if snapshot != nil {
			count = len(snapshot.Cells)
		}
		t.Fatalf("scene cells=%d want exactly 4 canonical rows", count)
	}
	wantKinds := []scene.CellKind{
		scene.KindUser,
		scene.KindAssistant,
		scene.KindUser,
		scene.KindAssistant,
	}
	wantSources := []string{"repeat", "first response", "repeat", "second response"}
	for index, cell := range snapshot.Cells {
		if cell.Kind != wantKinds[index] || cell.Source != wantSources[index] {
			t.Fatalf("cell[%d]=%+v, want kind=%v source=%q", index, cell, wantKinds[index], wantSources[index])
		}
	}
}

func TestSeedPersistedHistory_ImportsFinalToolChainOnce(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	messages := []runtimetypes.Message{
		{
			Role: "assistant",
			ToolCalls: []runtimetypes.ToolCall{{
				ID: "call-history-1", Name: "read_file",
			}},
			Metadata: runtimetypes.NewMetadata(),
		},
		*runtimetypes.NewToolMessage("call-history-1", "README contents"),
	}

	bridge.seedPersistedHistory(messages, "")
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		count := 0
		if snapshot != nil {
			count = len(snapshot.Cells)
		}
		t.Fatalf("scene cells=%d want one committed tool chain", count)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindToolChain || cell.Phase != scene.CellCommitted || cell.Source != "• Completed read_file\nREADME contents" {
		t.Fatalf("tool history cell=%+v", cell)
	}

	bridge.seedPersistedHistory(messages, "")
	if got := bridge.sceneSnapshot(); got == nil || len(got.Cells) != 1 {
		count := 0
		if got != nil {
			count = len(got.Cells)
		}
		t.Fatalf("second seed duplicated tool chain: cells=%d", count)
	}
}
