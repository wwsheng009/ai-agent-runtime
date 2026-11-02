package contextmgr

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/factledger"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// The ledger is written at the compaction boundary and holds only statements
// classified as declarative. A plain restatement of a turn must not become a
// "fact" — that is how the transcript got copied into the prompt before.
func TestPersistCompactionFactsStoresOnlyDeclarativeStatements(t *testing.T) {
	store := newFactStore(t)
	manager := NewManager(DefaultBudget(), store)
	segment := []types.Message{
		*types.NewUserMessage("decision: use sqlite for the ledger store"),
		*types.NewAssistantMessage("plan: next steps are to add the regression tests"),
		*types.NewUserMessage("you must not hand-edit generated files"),
		*types.NewAssistantMessage("the build failed: error[E0308] mismatched types"),
		*types.NewUserMessage("hello there, please continue"),
		*types.NewToolMessage("call-1", "Exit code: 0 Output: test result: ok. 666 passed; 0 failed"),
	}

	// Only the user's own assertions survive: agent narration (plan / failure
	// recap) belongs to the compaction summary, and small talk and tool payloads
	// belong to nobody.
	require.Equal(t, 2, manager.PersistCompactionFacts(context.Background(), "", "s", "task-1", segment))

	facts := listActiveFacts(t, store, "s")
	kinds := make([]string, 0, len(facts))
	for _, fact := range facts {
		kinds = append(kinds, fact.Kind)
	}
	require.ElementsMatch(t, []string{"decision", "constraint"}, kinds)

	joined := ""
	for _, fact := range facts {
		joined += factRenderValue(fact) + "\n"
	}
	require.NotContains(t, joined, "hello there")
	require.NotContains(t, joined, "666")
	require.NotContains(t, joined, "Exit code:")
	require.NotContains(t, joined, "next steps")
	require.NotContains(t, joined, "E0308")
}

// Re-running the same boundary must not append a second copy.
func TestPersistCompactionFactsIsIdempotent(t *testing.T) {
	store := newFactStore(t)
	manager := NewManager(DefaultBudget(), store)
	segment := []types.Message{*types.NewUserMessage("decision: keep the ledger declarative")}
	require.Equal(t, 1, manager.PersistCompactionFacts(context.Background(), "", "s", "task-1", segment))
	require.Equal(t, 1, manager.PersistCompactionFacts(context.Background(), "", "s", "task-1", segment))

	facts, err := factledger.New(store).ListActive(context.Background(), factledger.Query{SessionID: "s"})
	require.NoError(t, err)
	require.Len(t, facts, 1)
}

func TestPersistCompactionFactsNeedsSessionAndSegment(t *testing.T) {
	store := newFactStore(t)
	manager := NewManager(DefaultBudget(), store)
	require.Equal(t, 0, manager.PersistCompactionFacts(context.Background(), "", "", "task", []types.Message{*types.NewUserMessage("decision: x")}))
	require.Equal(t, 0, manager.PersistCompactionFacts(context.Background(), "", "s", "task", nil))
}

// The header is prose the model reads as a scope statement: it must describe
// what the block is, not claim precedence over the summary beside it.
func TestFactLedgerHeaderCarriesNoBlanketAuthority(t *testing.T) {
	require.NotContains(t, FactLedgerHeader(), "authoritative")
	require.NotContains(t, FactLedgerHeader(), "do not redo")

	headers := LegacyFactLedgerHeaders()
	require.Contains(t, headers, FactLedgerHeader())
	// Both shipped labels must stay recognized, or an old session leaks its
	// internal prompt layer into the visible transcript.
	require.Contains(t, headers, "Verified fact ledger (authoritative over compacted prose):")
	require.Contains(t, headers, "Verified session facts (newest first; already checked, do not redo):")
	for _, header := range headers {
		require.True(t, strings.HasSuffix(strings.TrimSpace(header), ":"))
	}
}

func TestRenderFactLineKeepsSubjectIdentity(t *testing.T) {
	now := time.Now().UTC()
	line := renderFactLine(declarativeFact("goal-1", "goal-active", "goal", "goal-active", "objective", "finish the release gate", now))
	// Subject rewriting (goal-active -> goal_active) would break every consumer
	// that identifies the goal by name.
	require.Contains(t, line, "goal-active")
	require.Contains(t, line, "[goal]")

	rule := renderFactLine(declarativeFact("rule-1", "", "constraint", "user", "requires", "Chinese output", now))
	require.Equal(t, "- [rule] user requires: Chinese output", rule)

	pitfall := renderFactLine(declarativeFact("fail-1", "", "failure", "assistant", "failed", "cargo test exited 101", now))
	require.Equal(t, "- [pitfall] assistant failed: cargo test exited 101", pitfall)
}

func TestStructuredFactClassificationRejectsRestatements(t *testing.T) {
	kind, _, _ := structuredFactClassification("fact", map[string]interface{}{"summary": "we looked at the file"})
	require.Equal(t, "conversation", kind)
	if _, injectable := injectableFactKinds[kind]; injectable {
		t.Fatal("a restatement must not be injectable")
	}

	kind, _, _ = structuredFactClassification("fact", map[string]interface{}{"summary": "you must not force push"})
	require.Equal(t, "constraint", kind)
}

// Production data contained ledger blocks mined back into facts: an older
// extractor read the injected `developer` layer and stored it as a "failure"
// fact, so the block started quoting itself.
func TestPersistCompactionFactsNeverHarvestsInjectedPromptLayers(t *testing.T) {
	store := newFactStore(t)
	manager := NewManager(DefaultBudget(), store)

	selfQuoted := types.NewDeveloperMessage("Verified fact ledger (authoritative over compacted prose):\n" +
		"- [execution] job_output failed {\"error\":\"tool not found\"}")
	checkpoint := types.NewUserMessage("Compacted context from earlier turns:\nEarlier work summarized.")
	checkpoint.Metadata["context_stage"] = "compaction"
	goal := types.NewDeveloperMessage("active goal: finish the gate")
	goal.Metadata["context_stage"] = "active_goal"
	segment := []types.Message{
		*types.NewSystemMessage("You are a helpful assistant."),
		*selfQuoted,
		*checkpoint,
		*goal,
		*types.NewToolMessage("call-1", "the command failed with error E0001"),
	}
	require.Equal(t, 0, manager.PersistCompactionFacts(context.Background(), "", "s", "task-1", segment))
	require.Empty(t, listActiveFacts(t, store, "s"))
}

// A real assertion still gets recorded once the injected layers are excluded.
func TestPersistCompactionFactsStillHarvestsConversationAfterFiltering(t *testing.T) {
	store := newFactStore(t)
	manager := NewManager(DefaultBudget(), store)
	segment := []types.Message{
		*types.NewDeveloperMessage("Verified fact ledger (authoritative over compacted prose):\n- noise"),
		*types.NewUserMessage("decision: ship the ledger as declarative residue only"),
	}
	require.Equal(t, 1, manager.PersistCompactionFacts(context.Background(), "", "s", "task-1", segment))
	facts := listActiveFacts(t, store, "s")
	require.Len(t, facts, 1)
	require.Equal(t, "decision", facts[0].Kind)
}

// Self-ingested rows already stored by older builds must not be shown again.
func TestBuildSkipsSelfIngestedFacts(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, factledger.Fact{
		FactID: "self-fed", SessionID: "s", Scope: factledger.ScopeSession, Kind: "failure",
		Subject: "developer", Predicate: "failed",
		Value:         "Verified fact ledger (authoritative over compacted prose):\n- [execution] shell failed {\"error\":\"x\"}",
		SourceEventID: "event:1", EvidenceRefs: []string{"event:1"}, UpdatedAt: now.Add(time.Second),
	})
	appendedFact(t, store, declarativeFact("rule-real", "", "constraint", "user", "requires", "Chinese output", now))

	content := ledgerMessages(NewManager(DefaultBudget(), store).Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(true),
	}).Messages)[0].Content
	require.NotContains(t, content, "Verified fact ledger")
	require.NotContains(t, content, "self fed")
	require.Contains(t, content, "Chinese output")
}

// Failure vocabulary is not a failure: a clean suite reported as "0 failed" was
// being stored as an authoritative pitfall in real sessions.
func TestReportsACleanResultIsNotRecordedAsFailure(t *testing.T) {
	store := newFactStore(t)
	manager := NewManager(DefaultBudget(), store)
	require.Equal(t, 0, manager.PersistCompactionFacts(context.Background(), "", "s", "task-1", []types.Message{
		*types.NewUserMessage("Authoritative full-suite 848 passed / 0 failed after the new area_size test"),
	}))
	require.Empty(t, listActiveFacts(t, store, "s"))

	// Positive control: a user-reported failure still lands as a pitfall.
	require.Equal(t, 1, manager.PersistCompactionFacts(context.Background(), "", "s", "task-1", []types.Message{
		*types.NewUserMessage("cargo test failed: 3 tests failed, error[E0308] mismatched types"),
	}))
	facts := listActiveFacts(t, store, "s")
	require.Len(t, facts, 1)
	require.Equal(t, "failure", facts[0].Kind)
}

// Agent narration is the summary's job, never the ledger's.
func TestLedgerHarvestableMessageAcceptsOnlyUserRoleAssertions(t *testing.T) {
	require.True(t, ledgerHarvestableMessage(*types.NewUserMessage("do not touch generated files")))
	require.False(t, ledgerHarvestableMessage(*types.NewAssistantMessage("decision: I will refactor")))
	require.False(t, ledgerHarvestableMessage(*types.NewSystemMessage("you must obey")))
	require.False(t, ledgerHarvestableMessage(*types.NewDeveloperMessage("goal: ship it")))
	require.False(t, ledgerHarvestableMessage(*types.NewToolMessage("call-1", "failed")))
	require.False(t, ledgerHarvestableMessage(*types.NewUserMessage("   ")))

	stamped := types.NewUserMessage("Compacted context from earlier turns:\nsummary")
	stamped.Metadata["context_stage"] = "compaction"
	require.False(t, ledgerHarvestableMessage(*stamped), "an injected layer is not a user assertion")
}

// Real stored rows carried the originating role in Subject, so agent narration
// was filed as rules and todos. They must not be re-injected.
func TestBuildDropsNarrationSourcedFacts(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	for _, subject := range []string{"assistant", "developer", "system", "tool"} {
		appendedFact(t, store, declarativeFact("narrated-"+subject, "", "constraint", subject, "requires",
			"Let me do final due-diligence on the lane", now))
	}
	appendedFact(t, store, declarativeFact("user-rule", "", "constraint", "user", "requires",
		"field attribute blocks must come before geometry", now.Add(time.Second)))

	content := ledgerMessages(NewManager(DefaultBudget(), store).Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(true),
	}).Messages)[0].Content
	require.NotContains(t, content, "due-diligence")
	require.Contains(t, content, "field attribute blocks must come before geometry")
}

// The last two junk rows found in production data were host supervision prompts
// that ride in as user turns: a digest line saying "child session failed" is
// runtime bookkeeping, not something the user claimed.
func TestPersistCompactionFactsRejectsHostControlPlanePrompts(t *testing.T) {
	store := newFactStore(t)
	manager := NewManager(DefaultBudget(), store)

	digest := types.NewUserMessage("[子任务与后台作业进展] 有 20 项待处理。 - 子任务会话 " +
		"session_20261027073826_2rWb8QSo：已结束。child session failed 结果状态：failed")
	resume := types.NewUserMessage("[supervision] resume：子任务生命周期事件触发**同一 turn 续跑**。" +
		"以下 rollup 由 runtime 组装，无需回溯历史。")
	transient := types.NewUserMessage("please finish: the suite must be green before we stop")
	transient.Metadata["transient_prompt"] = true
	genuine := types.NewUserMessage("字段类的属性块必须在几何之前，decision: keep the vendor order")

	segment := []types.Message{*digest, *resume, *transient, *genuine}
	require.Equal(t, 1, manager.PersistCompactionFacts(context.Background(), "", "s", "task-1", segment))

	facts := listActiveFacts(t, store, "s")
	require.Len(t, facts, 1)
	require.Equal(t, "decision", facts[0].Kind)
	require.Contains(t, factRenderValue(facts[0]), "字段类的属性块")
}

// The rejection is a prefix rule on host tags, not a keyword ban on the words
// those prompts happen to contain.
func TestLedgerHarvestableMessageKeepsUserSpeechContainingSameWords(t *testing.T) {
	require.False(t, ledgerHarvestableMessage(*types.NewUserMessage("[supervision] wake body")))
	require.True(t, ledgerHarvestableMessage(*types.NewUserMessage(
		"the doc says [supervision] output is noisy; ignore it and keep going")))
}

// Rows already stored by the old writer carry the host digest text as a
// user-subject fact, so the rejection has to apply at read time as well or the
// two junk rows found in production would keep being injected.
func TestBuildDropsStoredControlPlaneRows(t *testing.T) {
	store := newFactStore(t)
	now := time.Now().UTC()
	appendedFact(t, store, factledger.Fact{
		FactID: "digest-row", SessionID: "s", Scope: factledger.ScopeSession, Kind: "failure",
		Subject: "user", Predicate: "failed",
		Value: "[子任务与后台作业进展] 有 20 项待处理。 - 子任务会话 session_20261027073826_2rWb8QSo：" +
			"已结束。child session failed 结果状态：failed",
		SourceEventID: "event:1", EvidenceRefs: []string{"event:1"}, UpdatedAt: now,
	})
	appendedFact(t, store, declarativeFact("real-rule", "", "constraint", "user", "requires",
		"attribute blocks must precede geometry", now))

	content := ledgerMessages(NewManager(DefaultBudget(), store).Build(context.Background(), BuildInput{
		SessionID: "s", Goal: "continue", History: factHistory(true),
	}).Messages)[0].Content
	require.NotContains(t, content, "子任务与后台作业进展")
	require.NotContains(t, content, "child session failed")
	require.Contains(t, content, "attribute blocks must precede geometry")
}
