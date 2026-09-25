package ui

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 需求：预算耗尽只能**截断**规划，绝不能把「被截断」伪装成「没有可交付历史」。
//
// 规划器把「行集为空」当作「没有可交付历史」直接返回（planEligibleHistoryCommits
// 的 len(rows)==0 分支）。而 screening 的预算检查在 index=0 就会命中：resume 会话
// 里 LayoutTranscript（4497 cell → 159k 行）与 fold target 这两段前置工作本身就可能
// 超过 historyCommitPlanningBudget，于是 screening 拿到的 deadline 早已过期，循环
// 第一次检查就返回空结果。每一轮 reduce 重试都如此 → 规划永远 0 候选 → next=0 /
// pending=0（live 实测），授权过的销毁式 scrollback 重放清空屏幕后无内容可写。
//
// 因此：即使 deadline 已经过期，规划也必须产出一个非空前缀（并报告 incomplete），
// 让重试严格前进，而不是永远返回「什么都没有」。
func TestExpiredPlanningBudgetStillYieldsAPlanPrefix(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "loaded session"),
	}, 2)
	if state.HistoryEffects.NextToken == 0 {
		t.Fatal("fixture did not plan the loaded transcript")
	}

	// 已过期的 deadline 就是 resume 会话上 screening 实际拿到的输入。
	commits, _ := planEligibleHistoryCommitsWithin(state.AppState, time.Now().Add(-time.Second))
	if len(commits) == 0 {
		t.Fatal("an expired planning budget produced an empty plan: the caller cannot " +
			"distinguish a truncated plan from a transcript with no deliverable history, " +
			"so the session plans nothing and stays blank forever")
	}
}

// 需求：截断的前缀必须让**下一次**规划继续前进，而不是每轮重新产出同一个前缀、
// 重复铸造 token。fixture 的 ReplaceTranscriptAction 已经规划并入队过第一批，
// 这里重放同一前缀（过期预算下的真实输入）必须是无操作。
func TestTruncatedPlanPrefixDoesNotRequeueDeliveredSources(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "loaded session"),
	}, 2)
	before := state.HistoryEffects.NextToken
	pending := historyPendingCount(state)
	if before == 0 || pending == 0 {
		t.Fatalf("fixture did not queue anything: next=%d pending=%d", before, pending)
	}

	commits, _ := planEligibleHistoryCommitsWithin(state.AppState, time.Now().Add(-time.Second))
	if len(commits) == 0 {
		t.Fatal("fixture produced no plan prefix")
	}
	syncHistoryEffectCandidatesPrefix(&state, commits)
	if state.HistoryEffects.NextToken != before {
		t.Fatalf("a truncated prefix re-minted an already queued source: next=%d before=%d",
			state.HistoryEffects.NextToken, before)
	}
	if got := historyPendingCount(state); got != pending {
		t.Fatalf("pending=%d, want %d: the same prefix was queued twice", got, pending)
	}
}

// 需求：被预算截断的计划必须自己走到 complete，而不是把最老前缀当成整份计划。
//
// live 事故（session_20260924072950_ltYRU9tG）：resume 的销毁式重放只投递了
// epoch2 的 356 个 acked 提交（next=1068），而 transcript 是 6585 cell /
// 290957 行；随后执行器空闲 8 分钟，pending=0、scrollback-replay-armed=false、
// recovery_actionable=false，缺口既不在 scrollback 也不在常驻区，且不可自愈。
// 原因是截断路径把续跑寄托在「下一次 reduce」上，但 resume 后的空闲会话没有任何
// transcript 迁移，ack 处理器当时也不重规划 —— 前缀排空就是这条计划的终点。
//
// 这里固化修复后的契约：截断留下 PlanIncomplete，前缀真正交付完（ack 归零）后由
// ack 处理器续跑，直到完整规划覆盖到 transcript 的最后一个 cell。
//
// 第二轮 live 事故（同一会话，端口 49296 的进程）：续跑接上之后仍然停住 ——
// next=1288 / acked=322 / pending=0 / plan_incomplete=true / plan_stalled=true，
// 注入 /status（cells 6647→6649，transcript 真的变了）之后 60s 内 next 与
// cell_rows_misses 一动不动。成因是续跑那一轮**仍然吃预算**：极小预算（或冷缓存的
// 大前缀）下每一轮都在第一个采样点截断，返回同一个前缀 —— 前缀全是缓存命中
// （没有新的 cell_rows miss），前缀里的 cell 也早已有终态记录（没有新的 token），
// 于是 NextToken 不变、PlanStalled 被永久置位、执行器的续跑 kick 被解除。
// 契约因此收紧为：续跑用**无预算**的一轮走到尾部，交付阶段保持 0 预算也必须收敛。
func TestTruncatedTranscriptPlanContinuesUntilComplete(t *testing.T) {
	// 物理行数必须越过 layoutBudgetCheckRows（4096）才可能被预算切断：600 cell
	// × 6 行 + cell 边界 gap 行才 4200 行，margin 太薄，这里取 900。
	const cells = 900
	snapshot := benchResumedSnapshot(cells)
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 2)

	full, complete := planEligibleHistoryCommitsWithin(state.AppState, time.Time{})
	if !complete || len(full) == 0 {
		t.Fatalf("unbudgeted plan must be complete and non-empty: complete=%t commits=%d", complete, len(full))
	}

	// resume 的对账 epoch 会整体退休 ledger（reconcileScrollback），随后的重规划
	// 必须重新覆盖整份 transcript。真实预算下截断与否取决于机器速度与布局缓存
	// 状态（warm cache 上 250ms 往往就够走完整份历史），所以把预算压到 0：布局在
	// 第一个采样点（layoutBudgetCheckRows 行）必然截断，截断路径因此可以被确定性
	// 地测到。
	if !state.HistoryEffects.reconcileScrollback(state.HistoryEffects.TerminalEpoch + 1) {
		t.Fatal("fixture could not open a fresh terminal epoch")
	}
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	syncHistoryEffectsForTranscript(&state)
	if !state.HistoryEffects.PlanIncomplete {
		t.Fatalf("a truncated plan did not record the incomplete obligation: %#v", state.HistoryEffects)
	}
	prefix := historyPendingCount(state)
	if prefix == 0 {
		t.Fatal("a truncated plan produced no deliverable prefix")
	}
	if prefix >= len(full) {
		t.Fatalf("fixture did not truncate: prefix=%d full=%d", prefix, len(full))
	}
	// 前缀只允许铸造完整规划里也存在的身份。被窗口切断的 cell 一旦走 wholeCell
	// 回退，就会拿到「整个 cell」的身份，而完整窗口给出的是逐行身份 —— 两份提交
	// 都会投递，边界 cell 的可见行会被写两遍。
	fullIdentities := make(map[historyCommitSourceKey]struct{}, len(full))
	for _, commit := range full {
		fullIdentities[historyCommitSourceIdentity(commit)] = struct{}{}
	}
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == HistoryCommitInvalidated {
			continue
		}
		if _, ok := fullIdentities[historyCommitSourceIdentity(entry.Commit)]; !ok {
			t.Fatalf("the truncated prefix minted cell %d range=%+v that the complete plan does not contain: "+
				"the continuation would re-deliver those rows", entry.Commit.CellID, entry.Commit.SourceRange)
		}
	}
	// 前缀还在队列里时不得续规划：executor 已经在把它往前推，此时重规划只会每个
	// ack 白烧一次 250ms 布局预算。
	before := state.HistoryEffects.NextToken
	if continueTruncatedHistoryPlan(&state) {
		t.Fatal("a truncated plan continued while its delivered prefix was still pending")
	}
	if state.HistoryEffects.NextToken != before {
		t.Fatalf("continuation while pending minted tokens: next=%d before=%d", state.HistoryEffects.NextToken, before)
	}

	// 交付阶段**保持 0 预算**：0 预算下每一轮有预算的规划都必然停在第一个采样点
	// （index=layoutBudgetCheckRows），缓存再热也一样 —— 这正是 live 冻结前缀的
	// 成因。续跑必须不依赖预算：它不在流式热路径上（队列已经排空，且每个截断计划
	// 最多走到这里一次），所以用无预算的一轮把剩余部分一次走完。续跑若也吃预算，
	// 下面就会永远停在同一个前缀上：没有新 miss、没有新 token、PlanStalled 永久
	// 置位，覆盖度断言必然失败。
	_ = restoreBudget

	// 交付前缀：每次 ack 都会尝试续跑，只有 pending 归零的那一次真的续规划。
	revision := uint64(3)
	deadline := time.Now().Add(60 * time.Second)
	for {
		commits := state.HistoryEffects.Pending()
		if len(commits) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a truncated plan never converged: pending=%d next=%d incomplete=%t",
				historyPendingCount(state), state.HistoryEffects.NextToken, state.HistoryEffects.PlanIncomplete)
		}
		commit := commits[0]
		if err := state.HistoryEffects.markInFlight(commit.Token, commit.LayoutGeneration); err != nil {
			t.Fatalf("claim token %d: %v", commit.Token, err)
		}
		state = reduceUIControllerState(state, HistoryCommitAcknowledged{
			Token:            commit.Token,
			Frame:            1,
			LayoutGeneration: commit.LayoutGeneration,
		}, revision)
		revision++
	}

	if state.HistoryEffects.PlanIncomplete {
		t.Fatal("the delivered prefix did not continue the truncated plan: the rest of the " +
			"transcript is never planned, so those rows can never reach native scrollback")
	}
	planned := make(map[scene.CellID]struct{}, cells)
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == HistoryCommitInvalidated {
			continue
		}
		planned[entry.Commit.CellID] = struct{}{}
	}
	if len(planned) != cells {
		t.Fatalf("truncated plan converged over %d/%d cells: the tail of the transcript was never planned",
			len(planned), cells)
	}
}
