package contextmgr

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/factledger"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newFactStore(t *testing.T) *artifact.Store {
	t.Helper()
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: t.TempDir() + "/facts.sqlite"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// compactionBoundary marks the point where prose stopped reaching the model.
// The fact ledger exists only past this line.
func compactionBoundary() types.Message {
	boundary := types.NewUserMessage("Compacted context from earlier turns:\nEarlier turns were summarized.")
	boundary.Metadata["context_stage"] = "compaction"
	return *boundary
}

// factHistory builds a transcript ending on an open user turn. With
// afterCompaction=false the transcript is still intact, which is the state where
// the ledger must stay silent.
func factHistory(afterCompaction bool, extra ...types.Message) []types.Message {
	messages := []types.Message{*types.NewSystemMessage("system prompt")}
	if afterCompaction {
		messages = append(messages, compactionBoundary())
	}
	messages = append(messages, extra...)
	return append(messages, *types.NewUserMessage("continue"))
}

func ledgerMessages(messages []types.Message) []types.Message {
	found := make([]types.Message, 0, 1)
	for _, message := range messages {
		if message.Metadata.GetString("context_stage", "") == "fact_ledger" {
			found = append(found, message)
		}
	}
	return found
}

func appendedFact(t *testing.T, store *artifact.Store, fact factledger.Fact) {
	t.Helper()
	_, err := factledger.New(store).Append(context.Background(), fact)
	require.NoError(t, err)
}

func listActiveFacts(t *testing.T, store *artifact.Store, sessionID string) []factledger.Fact {
	t.Helper()
	facts, err := factledger.New(store).ListActive(context.Background(), factledger.Query{
		SessionID: sessionID, Kinds: declarativeFactKinds(), Limit: 64,
	})
	require.NoError(t, err)
	return facts
}

func declarativeFact(id, goalID, kind, subject, predicate, value string, at time.Time) factledger.Fact {
	return factledger.Fact{
		FactID: id, SessionID: "s", GoalID: goalID,
		Scope: func() string {
			if goalID == "" {
				return factledger.ScopeSession
			}
			return factledger.ScopeGoal
		}(),
		Kind: kind, Subject: subject, Predicate: predicate, Value: value,
		SourceEventID: "event:" + id, EvidenceRefs: []string{"evidence:" + id},
		Confidence: 0.9, UpdatedAt: at, ValidFrom: at,
	}
}

// The ledger is the residue of a compaction. While the transcript is intact the
// model can still read every turn, so injecting facts there is duplication.
func TestBuildKeepsFactLedgerSilentUntilCompactionBoundary(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, declarativeFact("rule-1", "", "constraint", "user", "requires", "Chinese output", now))

	manager := NewManager(DefaultBudget(), store)
	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(false),
	})
	require.Nil(t, result.Metadata["fact_ledger_injected"])
	require.Empty(t, ledgerMessages(result.Messages))

	metrics := result.Metadata["context_layer_metrics"].(map[string]interface{})["facts"].(map[string]interface{})
	require.Equal(t, true, metrics["suppressed_pre_compaction"])
	require.Equal(t, false, metrics["compaction_boundary"])

	// Past the boundary the same fact is delivered.
	after := manager.Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(true),
	})
	require.Equal(t, true, after.Metadata["fact_ledger_injected"])
	require.Contains(t, ledgerMessages(after.Messages)[0].Content, "Chinese output")
}

func TestBuildInjectsOnlyActiveGoalDeclarativeFacts(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, declarativeFact("rule-1", "", "constraint", "user", "requires", "Chinese output", now))
	appendedFact(t, store, declarativeFact("decision-a", "a", "decision", "runtime", "decided", "port 8201", now.Add(time.Second)))
	appendedFact(t, store, declarativeFact("question-b", "b", "open_question", "release", "needs_verification", "other goal question", now.Add(time.Second)))

	manager := NewManager(DefaultBudget(), store)
	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s", GoalID: "a", Goal: "continue", History: factHistory(true),
	})
	require.Equal(t, true, result.Metadata["fact_ledger_injected"])
	require.Equal(t, 2, result.Metadata["fact_count"])

	ledger := ledgerMessages(result.Messages)
	require.Len(t, ledger, 1)
	content := ledger[0].Content
	require.Contains(t, content, "Chinese output")
	require.Contains(t, content, "port 8201")
	require.NotContains(t, content, "other goal question")

	// Evidence stays metadata: an opaque event id inlined in prose is a token
	// cost the model cannot spend.
	require.NotContains(t, content, "evidence:")
	require.NotContains(t, content, "event:")
	require.Contains(t, ledger[0].Metadata["evidence_refs"], "evidence:decision-a")
	require.NotContains(t, ledger[0].Metadata["evidence_refs"], "evidence:question-b")
}

// Process artifacts must not come back just because older builds stored them.
func TestBuildNeverInjectsProcessArtifacts(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	legacy := map[string]interface{}{"success": true, "output": "1: package foldertrust 2: 3: import ("}
	appendedFact(t, store, factledger.Fact{
		FactID: "legacy-view", SessionID: "s", Scope: factledger.ScopeSession, Kind: "workspace",
		Subject: "view", Predicate: "succeeded", Value: legacy,
		SourceEventID: "event:v", EvidenceRefs: []string{"event:v"}, UpdatedAt: now,
	})
	appendedFact(t, store, factledger.Fact{
		FactID: "legacy-shell", SessionID: "s", Scope: factledger.ScopeSession, Kind: "execution",
		Subject: "shell", Predicate: "succeeded",
		Value:         map[string]interface{}{"success": true, "output": "Exit code: 0 Shell: pwsh Workdir: E:\\repo Output: test result: ok. 666 passed"},
		SourceEventID: "event:s", EvidenceRefs: []string{"event:s"}, UpdatedAt: now.Add(time.Second),
	})
	appendedFact(t, store, factledger.Fact{
		FactID: "legacy-patch", SessionID: "s", Scope: factledger.ScopeSession, Kind: "state_change",
		Subject: "apply_patch", Predicate: "succeeded", Value: "补丁已应用：修改 1；文件差异: ```diff\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n```",
		SourceEventID: "event:p", EvidenceRefs: []string{"event:p"}, UpdatedAt: now.Add(time.Second),
	})
	appendedFact(t, store, factledger.Fact{
		FactID: "legacy-test", SessionID: "s", Scope: factledger.ScopeSession, Kind: "test_result",
		Subject: "go_test", Predicate: "succeeded", Value: "tests: 9 passed",
		SourceEventID: "event:t", EvidenceRefs: []string{"event:t"}, UpdatedAt: now.Add(time.Second),
	})
	appendedFact(t, store, declarativeFact("rule-keep", "", "constraint", "user", "requires", "never force-push", now.Add(2*time.Second)))

	manager := NewManager(DefaultBudget(), store)
	content := ledgerMessages(manager.Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(true),
	}).Messages)[0].Content

	require.NotContains(t, content, "package foldertrust")
	require.NotContains(t, content, "Exit code:")
	require.NotContains(t, content, "apply_patch")
	require.NotContains(t, content, "666")
	require.NotContains(t, content, "tests: 9 passed")
	require.NotContains(t, content, "```")
	require.Contains(t, content, "never force-push")
}

// Facts the transcript already carries are not announced twice; they are
// re-announced only once the message carrying them is gone.
func TestBuildAnnouncesEachFactOnlyOnce(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, declarativeFact("rule-1", "", "constraint", "user", "requires", "Chinese output", now))
	appendedFact(t, store, declarativeFact("decision-1", "", "decision", "runtime", "decided", "use sqlite", now.Add(time.Second)))

	announced := types.NewDeveloperMessage(FactLedgerHeader() + "\n- [rule] user requires: Chinese output")
	announced.Metadata["context_stage"] = "fact_ledger"
	announced.Metadata["fact_ids"] = []string{"rule-1"}

	manager := NewManager(DefaultBudget(), store)
	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(true, *announced),
	})
	ledger := ledgerMessages(result.Messages)
	// The announced block stays in the transcript and only the delta is appended.
	require.Len(t, ledger, 2)
	// Only the new decision is delta-material; the rule is already visible.
	require.NotContains(t, ledger[1].Content, "Chinese output")
	require.Contains(t, ledger[1].Content, "use sqlite")
	require.Equal(t, []string{"decision-1"}, ledger[1].Metadata["fact_ids"])

	// The ids survive a JSON round-trip through storage as []interface{}.
	stamped := types.NewDeveloperMessage("x")
	stamped.Metadata["context_stage"] = "fact_ledger"
	stamped.Metadata["fact_ids"] = []interface{}{"rule-1", "decision-1"}
	require.Equal(t, []string{"rule-1", "decision-1"}, metadataStringValues(stamped.Metadata["fact_ids"]))
}

// A second build must not relocate the conversation prefix: the ledger only
// appends after raw history.
func TestBuildDoesNotPrependFactLedgerBeforeHistory(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, declarativeFact("rule-1", "", "constraint", "user", "requires", "Chinese output", now))

	manager := NewManager(DefaultBudget(), store)
	history := factHistory(true)
	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: history,
	})
	require.Equal(t, true, result.Metadata["fact_ledger_injected"])
	require.Equal(t, history, result.Messages[:len(history)])

	ledger := result.Messages[len(result.Messages)-1]
	require.Equal(t, "developer", ledger.Role)
	require.Equal(t, "fact_ledger", ledger.Metadata.GetString("context_stage", ""))
}

func TestBuildKeepsConversationPrefixStableWhenFactLedgerInjected(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, declarativeFact("rule-1", "", "constraint", "user", "requires", "Chinese output", now))

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		compactionBoundary(),
		*types.NewUserMessage("first user turn"),
		*types.NewAssistantMessage("first assistant turn"),
		*types.NewUserMessage("continue with facts"),
	}
	manager := NewManager(DefaultBudget(), store)

	withoutFacts := manager.Build(context.Background(), BuildInput{
		SessionID: "", Goal: "continue", History: history,
	})
	withFacts := manager.Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: history,
	})
	require.Equal(t, true, withFacts.Metadata["fact_ledger_injected"])
	require.NotEqual(t, true, withoutFacts.Metadata["fact_ledger_injected"])

	require.GreaterOrEqual(t, len(withFacts.Messages), len(withoutFacts.Messages))
	for index := range withoutFacts.Messages {
		require.Equal(t, withoutFacts.Messages[index].Role, withFacts.Messages[index].Role)
		require.Equal(t, withoutFacts.Messages[index].Content, withFacts.Messages[index].Content)
	}
	ledger := withFacts.Messages[len(withFacts.Messages)-1]
	require.Equal(t, "developer", ledger.Role)
	require.Contains(t, ledger.Content, "Chinese output")
}

// Mid-turn compaction leaves a checkpoint inside the active turn; the residue
// still has to be rehydrated append-only after it.
func TestBuildRehydratesFactLedgerAfterMidTurnCompactHistory(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, declarativeFact("rule-1", "", "constraint", "user", "requires", "Chinese output", now))

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("continue optimizing"),
		*types.NewAssistantMessage("inspecting"),
		*types.NewToolMessage("call-1", "tool output"),
		compactionBoundary(),
	}

	manager := NewManager(DefaultBudget(), store)
	manager.Strategy.RecallMode = RecallModeDisabled
	manager.Strategy.WorkspaceMode = WorkspaceModeDisabled

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue optimizing", History: history,
	})
	require.Equal(t, true, result.Metadata["fact_ledger_injected"])
	require.Equal(t, history, result.Messages[:len(history)])

	ledger := ledgerMessages(result.Messages)
	require.Len(t, ledger, 1)
	require.Equal(t, "developer", ledger[0].Role)
	require.True(t, ledger[0].Metadata.GetBool("context_snapshot", false))
	require.Contains(t, ledger[0].Content, "Chinese output")
}

// Once the active turn already carries a frozen ledger, it is never rebuilt.
func TestBuildSkipsFactLedgerWhenActiveTurnAlreadyHasSnapshot(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, declarativeFact("rule-1", "", "constraint", "user", "requires", "Chinese output", now))

	frozen := types.NewDeveloperMessage(FactLedgerHeader() + "\n- [rule] user requires: frozen only")
	frozen.Metadata["context_stage"] = "fact_ledger"
	frozen.Metadata["context_snapshot"] = true
	frozen.Metadata["fact_ids"] = []string{"rule-1"}

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		compactionBoundary(),
		*frozen,
		*types.NewAssistantMessage("calling tools"),
		*types.NewToolMessage("call-1", "tool output"),
	}
	manager := NewManager(DefaultBudget(), store)
	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: history,
	})
	require.Nil(t, result.Metadata["fact_ledger_injected"])
	require.Equal(t, history, result.Messages[:len(history)])

	ledger := ledgerMessages(result.Messages)
	require.Len(t, ledger, 1)
	require.Contains(t, ledger[0].Content, "frozen only")
	require.NotContains(t, ledger[0].Content, "Chinese output")
}

// Goal-scoped facts must not vanish when the goal ends: the ledger would
// empty itself mid-session and relocate the injected message.
func TestBuildKeepsGoalFactsAfterGoalEnds(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, declarativeFact("decision-a", "goal-1", "decision", "runtime", "decided", "port 8201", now))

	manager := NewManager(DefaultBudget(), store)
	withGoal := manager.Build(context.Background(), BuildInput{
		SessionID: "s", GoalID: "goal-1", Goal: "continue", History: factHistory(true),
	})
	require.Contains(t, ledgerMessages(withGoal.Messages)[0].Content, "port 8201")

	withoutGoal := manager.Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(true),
	})
	require.Contains(t, ledgerMessages(withoutGoal.Messages)[0].Content, "port 8201")
}

func TestBuildOrdersDeclarativeFactsNewestFirst(t *testing.T) {
	store := newFactStore(t)
	base := time.Now().UTC()
	appendedFact(t, store, declarativeFact("old-rule", "", "constraint", "user", "requires", "older rule", base))
	appendedFact(t, store, declarativeFact("new-decision", "", "decision", "runtime", "decided", "newer decision", base.Add(time.Minute)))
	// A stored failure (confidence 1.0) must not outrank a newer decision.
	appendedFact(t, store, factledger.Fact{
		FactID: "old-failure", SessionID: "s", Scope: factledger.ScopeSession, Kind: "failure",
		Subject: "assistant", Predicate: "failed", Value: "older pitfall", Confidence: 1,
		SourceEventID: "event:f", EvidenceRefs: []string{"event:f"}, UpdatedAt: base.Add(30 * time.Second),
	})

	content := ledgerMessages(NewManager(DefaultBudget(), store).Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(true),
	}).Messages)[0].Content
	lines := strings.Split(content, "\n")
	require.Contains(t, lines[1], "newer decision", "newest fact must render first")
	require.NotContains(t, lines[1], "older")
}

// Regression for the shape measured in production: a long session filed hundreds
// of process-artifact rows before the split, and its few declarative facts are
// the OLDEST rows in it. When the read window is counted in raw rows, the window
// fills with rows the selection is about to discard and the declarative fact is
// never reached — the ledger then silently contributes nothing to a session that
// genuinely has residue to carry.
func TestBuildReachesDeclarativeFactBuriedUnderProcessArtifacts(t *testing.T) {
	store := newFactStore(t)
	ledger := factledger.New(store)
	base := time.Now().UTC().Add(-time.Hour)

	// The constraint is recorded first, then buried under 400 newer rows.
	appendedFact(t, store, declarativeFact("rule-old", "", "constraint", "user", "requires", "never force-push to release", base))
	for index := 0; index < 400; index++ {
		appendedFact(t, store, factledger.Fact{
			FactID: "run-" + strconv.Itoa(index), SessionID: "s", Scope: factledger.ScopeSession,
			Kind: "execution", Subject: "shell", Predicate: "succeeded",
			Value:         "test result: ok. " + strconv.Itoa(index) + " passed",
			SourceEventID: "event:run-" + strconv.Itoa(index),
			EvidenceRefs:  []string{"event:run-" + strconv.Itoa(index)},
			Confidence:    0.95,
			ValidFrom:     base.Add(time.Duration(index+1) * time.Minute),
			UpdatedAt:     base.Add(time.Duration(index+1) * time.Minute),
		})
	}

	// Prove the failure is the window, not the filter: a raw-row read cannot
	// reach the buried fact, while the pushed-down kind filter can.
	rawWindow, err := ledger.ListActive(context.Background(), factledger.Query{SessionID: "s", Limit: 12})
	require.NoError(t, err)
	require.Len(t, rawWindow, 12)
	declarative := 0
	for _, fact := range rawWindow {
		if _, ok := injectableFactKinds[fact.Kind]; ok {
			declarative++
		}
	}
	require.Zero(t, declarative, "a raw-row window must not reach the buried fact; the pushed-down filter is what fixes it")

	filtered, err := ledger.ListActive(context.Background(), factledger.Query{
		SessionID: "s", Kinds: declarativeFactKinds(), Limit: 12,
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(filtered))
	require.Equal(t, "rule-old", filtered[0].FactID)

	result := NewManager(DefaultBudget(), store).Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(true),
	})
	require.Equal(t, true, result.Metadata["fact_ledger_injected"])
	content := ledgerMessages(result.Messages)[0].Content
	require.Contains(t, content, "never force-push to release")
	require.NotContains(t, content, "test result: ok.")
}

// The pushed-down filter must degrade rather than over-fetch when a store cannot
// answer a content-kind predicate.
func TestDeclarativeFactKindsIsStableAndComplete(t *testing.T) {
	first, second := declarativeFactKinds(), declarativeFactKinds()
	require.Equal(t, first, second, "query shape must stay stable")
	require.Len(t, first, len(injectableFactKinds))
	for _, kind := range first {
		if _, ok := injectableFactKinds[kind]; !ok {
			t.Fatalf("unknown kind %q in declarativeFactKinds", kind)
		}
	}
	require.NotContains(t, first, "execution")
	require.NotContains(t, first, "test_result")
}
