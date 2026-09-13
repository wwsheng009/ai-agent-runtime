package toolbroker

import (
	"context"
	"strings"
	"testing"
)

// scriptedReadController serves queued read windows so repeat-read behavior can
// be asserted without a real event store. The last window is sticky, which keeps
// the call sites readable for long polling streaks.
type scriptedReadController struct {
	*fakeAgentSessionController
	windows []AgentEventsResult
	calls   int
}

func newScriptedReadController(windows ...AgentEventsResult) *scriptedReadController {
	return &scriptedReadController{fakeAgentSessionController: &fakeAgentSessionController{}, windows: windows}
}

func (c *scriptedReadController) ReadEvents(ctx context.Context, args ReadAgentEventsArgs) (*AgentEventsResult, error) {
	c.fakeAgentSessionController.lastRead = args
	index := c.calls
	c.calls++
	if len(c.windows) == 0 {
		return &AgentEventsResult{SessionID: args.ID}, nil
	}
	if index >= len(c.windows) {
		index = len(c.windows) - 1
	}
	window := c.windows[index]
	window.Events = append([]AgentEventItem(nil), window.Events...)
	return &window, nil
}

func executeReadAgentEvents(t *testing.T, broker *Broker, caller string, args map[string]interface{}) (*AgentEventsResult, map[string]interface{}) {
	t.Helper()
	rawResult, meta, err := broker.Execute(context.Background(), caller, ToolReadAgentEvents, args)
	if err != nil {
		t.Fatalf("read_agent_events failed: %v", err)
	}
	result, ok := rawResult.(*AgentEventsResult)
	if !ok || result == nil {
		t.Fatalf("unexpected read_agent_events result: %#v", rawResult)
	}
	return result, meta
}

func TestBroker_ReadAgentEvents_RepeatedWindowMarksUnchanged(t *testing.T) {
	controller := newScriptedReadController(
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
	)
	broker := &Broker{AgentSessions: controller}
	read := func(afterSeq float64) (*AgentEventsResult, map[string]interface{}) {
		t.Helper()
		return executeReadAgentEvents(t, broker, "parent-session", map[string]interface{}{
			"id":        "child-1",
			"after_seq": afterSeq,
		})
	}

	first, firstMeta := read(5)
	if first.Unchanged || first.RepeatCount != 0 {
		t.Fatalf("first read must not be marked unchanged: %#v", first)
	}
	if _, ok := firstMeta["unchanged"]; ok {
		t.Fatalf("first read must not report unchanged metadata: %#v", firstMeta)
	}

	second, secondMeta := read(5)
	if !second.Unchanged || second.RepeatCount != 1 {
		t.Fatalf("second identical read must be marked unchanged with repeat_count=1: %#v", second)
	}
	if !strings.Contains(second.NextAction, "unchanged_window") {
		t.Fatalf("expected repeat guidance to replace the generic next_action, got %q", second.NextAction)
	}
	if secondMeta["unchanged"] != true || secondMeta["repeat_count"] != 1 {
		t.Fatalf("expected explicit unchanged metadata, got %#v", secondMeta)
	}
	summary, _ := secondMeta[cacheSafeSummaryMetadataKey].(string)
	if !strings.Contains(summary, "identical read #1") {
		t.Fatalf("expected cache-safe summary to state the repeat, got %q", summary)
	}

	third, _ := read(5)
	if !third.Unchanged || third.RepeatCount != 2 {
		t.Fatalf("streak must keep counting: %#v", third)
	}

	advanced, _ := read(7)
	if advanced.Unchanged || advanced.RepeatCount != 0 {
		t.Fatalf("a new after_seq starts a fresh streak: %#v", advanced)
	}
	advancedRepeat, _ := read(7)
	if !advancedRepeat.Unchanged || advancedRepeat.RepeatCount != 1 {
		t.Fatalf("expected the new cursor's own streak: %#v", advancedRepeat)
	}
}

func TestBroker_ReadAgentEvents_AdvancedWindowResetsStreak(t *testing.T) {
	controller := newScriptedReadController(
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 9},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 9},
	)
	broker := &Broker{AgentSessions: controller}
	read := func() *AgentEventsResult {
		t.Helper()
		result, _ := executeReadAgentEvents(t, broker, "parent-session", map[string]interface{}{
			"id":        "child-1",
			"after_seq": float64(5),
		})
		return result
	}

	if result := read(); result.Unchanged {
		t.Fatalf("first read must not be marked unchanged: %#v", result)
	}
	if result := read(); result.Unchanged || result.RepeatCount != 0 {
		t.Fatalf("an advanced window must not be marked unchanged: %#v", result)
	}
	if result := read(); !result.Unchanged || result.RepeatCount != 1 {
		t.Fatalf("expected the streak to restart at 1 after the window advanced: %#v", result)
	}
}

func TestBroker_ReadAgentEvents_RepeatStreakIsPerCaller(t *testing.T) {
	controller := newScriptedReadController(
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
	)
	broker := &Broker{AgentSessions: controller}
	read := func(caller string) *AgentEventsResult {
		t.Helper()
		result, _ := executeReadAgentEvents(t, broker, caller, map[string]interface{}{
			"id":        "child-1",
			"after_seq": float64(5),
		})
		return result
	}

	if result := read("parent-a"); result.Unchanged {
		t.Fatalf("first caller read must not be marked unchanged: %#v", result)
	}
	if result := read("parent-b"); result.Unchanged {
		t.Fatalf("a different caller must not inherit another caller's streak: %#v", result)
	}
	if result := read("parent-a"); !result.Unchanged || result.RepeatCount != 1 {
		t.Fatalf("expected parent-a to continue its own streak: %#v", result)
	}
}

func TestBroker_ReadAgentEvents_OwnerlessCallDoesNotTrackStreak(t *testing.T) {
	controller := newScriptedReadController(
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
	)
	broker := &Broker{AgentSessions: controller}

	for attempt := 0; attempt < 2; attempt++ {
		result, meta := executeReadAgentEvents(t, broker, "", map[string]interface{}{
			"id":        "child-1",
			"after_seq": float64(5),
		})
		if result.Unchanged || result.RepeatCount != 0 {
			t.Fatalf("attempt %d must not be marked unchanged without a caller session: %#v", attempt, result)
		}
		if _, ok := meta["unchanged"]; ok {
			t.Fatalf("attempt %d must not report unchanged metadata: %#v", attempt, meta)
		}
	}
}

func TestBroker_CloseAgent_ForgetsReadWindowStreak(t *testing.T) {
	controller := newScriptedReadController(
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
		AgentEventsResult{SessionID: "child-1", LatestSeq: 7},
	)
	broker := &Broker{AgentSessions: controller}
	read := func() *AgentEventsResult {
		t.Helper()
		result, _ := executeReadAgentEvents(t, broker, "parent-session", map[string]interface{}{
			"id":        "child-1",
			"after_seq": float64(5),
		})
		return result
	}

	if result := read(); result.Unchanged {
		t.Fatalf("first read must not be marked unchanged: %#v", result)
	}
	if _, _, err := broker.Execute(context.Background(), "parent-session", ToolCloseAgent, map[string]interface{}{"id": "child-1"}); err != nil {
		t.Fatalf("close_agent failed: %v", err)
	}
	if result := read(); result.Unchanged {
		t.Fatalf("close_agent must forget the read-window streak: %#v", result)
	}
	if result := read(); !result.Unchanged || result.RepeatCount != 1 {
		t.Fatalf("expected a fresh streak after close_agent: %#v", result)
	}
}

func TestMarkAgentEventsRepeatedRead_NormalizesRepeatCount(t *testing.T) {
	result := MarkAgentEventsRepeatedRead(&AgentEventsResult{SessionID: "child-1"}, 5, 0)
	if !result.Unchanged || result.RepeatCount != 1 {
		t.Fatalf("repeat count must be normalized to 1: %#v", result)
	}
	if !strings.Contains(result.NextAction, "after_seq=5") {
		t.Fatalf("expected the cursor to be quoted in the guidance, got %q", result.NextAction)
	}
	if MarkAgentEventsRepeatedRead(nil, 5, 1) != nil {
		t.Fatalf("nil result must stay nil")
	}
}

func TestAgentEventsReadMemo_ObserveForgetAndEvict(t *testing.T) {
	var nilMemo *agentEventsReadMemo
	if repeats, ok := nilMemo.observe(agentEventsReadKey{}, 1); ok || repeats != 0 {
		t.Fatalf("nil memo must be a no-op, got repeats=%d ok=%v", repeats, ok)
	}
	nilMemo.forgetTarget("child-1")

	memo := newAgentEventsReadMemo(4)
	keyA := agentEventsReadKey{callerSessionID: "parent", targetSessionID: "child-a", afterSeq: 1}
	keyB := agentEventsReadKey{callerSessionID: "parent", targetSessionID: "child-b", afterSeq: 1}

	if repeats, ok := memo.observe(keyA, 3); ok || repeats != 0 {
		t.Fatalf("first observe must be fresh, got repeats=%d ok=%v", repeats, ok)
	}
	if repeats, ok := memo.observe(keyA, 3); !ok || repeats != 1 {
		t.Fatalf("identical observe must report the first repeat, got repeats=%d ok=%v", repeats, ok)
	}
	if repeats, ok := memo.observe(keyA, 4); ok || repeats != 0 {
		t.Fatalf("advanced window must reset the streak, got repeats=%d ok=%v", repeats, ok)
	}
	if repeats, ok := memo.observe(keyA, 4); !ok || repeats != 1 {
		t.Fatalf("expected the streak to restart at 1, got repeats=%d ok=%v", repeats, ok)
	}

	if repeats, ok := memo.observe(keyB, 3); ok || repeats != 0 {
		t.Fatalf("first observe of another target must be fresh, got repeats=%d ok=%v", repeats, ok)
	}
	memo.forgetTarget("child-b")
	if repeats, ok := memo.observe(keyB, 3); ok || repeats != 0 {
		t.Fatalf("forgotten target must be treated as fresh, got repeats=%d ok=%v", repeats, ok)
	}
	// keyA already repeated its latestSeq=4 window once; activity on other
	// targets must not reset that caller/target streak (2 = third identical read).
	if repeats, ok := memo.observe(keyA, 4); !ok || repeats != 2 {
		t.Fatalf("other targets must not reset a streak, got repeats=%d ok=%v", repeats, ok)
	}
}

func TestAgentEventsReadMemo_EvictsOldestWindow(t *testing.T) {
	memo := newAgentEventsReadMemo(2)
	keyA := agentEventsReadKey{callerSessionID: "parent", targetSessionID: "child-a", afterSeq: 1}
	keyB := agentEventsReadKey{callerSessionID: "parent", targetSessionID: "child-b", afterSeq: 1}
	keyC := agentEventsReadKey{callerSessionID: "parent", targetSessionID: "child-c", afterSeq: 1}

	memo.observe(keyA, 3)
	memo.observe(keyB, 3)
	memo.observe(keyC, 3) // evicts keyA (limit 2)

	if repeats, ok := memo.observe(keyA, 3); ok || repeats != 0 {
		t.Fatalf("evicted key must be treated as fresh, got repeats=%d ok=%v", repeats, ok)
	}
	if repeats, ok := memo.observe(keyC, 3); !ok || repeats != 1 {
		t.Fatalf("surviving key must keep its streak, got repeats=%d ok=%v", repeats, ok)
	}
}

func TestAgentEventsCacheSafeSummary_UnchangedWindow(t *testing.T) {
	summary := agentEventsCacheSafeSummary(&AgentEventsResult{
		SessionID:   "child-1",
		Unchanged:   true,
		RepeatCount: 2,
		NextAction:  "unchanged_window: identical read #2",
	})
	if !strings.Contains(summary, "identical read #2") {
		t.Fatalf("expected the summary to state the repeat count, got %q", summary)
	}
}

func TestBrokerDefinitions_ReadAgentEventsDocumentsUnchangedSignal(t *testing.T) {
	broker := &Broker{AgentSessions: &fakeAgentSessionController{}}
	seen := 0
	for _, definition := range broker.Definitions() {
		if definition.Name != ToolReadAgentEvents {
			continue
		}
		seen++
		if !strings.Contains(definition.Description, "unchanged=true") {
			t.Fatalf("read_agent_events description must document the unchanged signal: %q", definition.Description)
		}
	}
	if seen == 0 {
		t.Fatalf("expected a read_agent_events definition")
	}
}
