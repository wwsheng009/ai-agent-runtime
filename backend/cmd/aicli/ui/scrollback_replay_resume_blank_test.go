package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// P2 replay 切片 S1：/resume（ReplaceTranscriptAction + ArmScrollbackReplay）不再
// 清屏重放。装载内容是 append-only 的：
//  1. 同一修订（同身份）重复装载不得再追加任何字节（delivery ledger 去重）；
//  2. 新修订只追加修正（新身份追加，旧行物理保留）；
//  3. 全路径不写 \x1b[3J，ScrollbackResetCount 恒 0，语义 epoch 不因装载推进。
func TestResumeLoadKeepsScrollbackAppendOnly(t *testing.T) {
	const width, height = 72, 12
	markers := make([]string, 30)
	for index := range markers {
		markers[index] = fmt.Sprintf("RESUME-REPLAY-%03d", index)
	}
	source := strings.Join(markers, "\n")
	cell := func(revision uint64) *scene.TranscriptCell {
		return &scene.TranscriptCell{
			ID: 901, Revision: revision, Kind: scene.KindAssistant,
			Source: source, Phase: scene.CellCommitted,
		}
	}

	controller := NewUIController(UIControllerConfig{}, nil, nil)
	go controller.Run()
	physical := &bytes.Buffer{}
	session := NewTerminalSession(physical)
	executor := NewTerminalSessionExecutor(controller, session)
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})

	post := func(actions ...UIAction) {
		t.Helper()
		for _, action := range actions {
			if !controller.Post(action) {
				t.Fatalf("post %T", action)
			}
		}
		controller.WaitIdle()
	}
	flush := func() {
		executor.Request()
		executor.WaitIdle()
		controller.WaitIdle()
	}
	converge := func() {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			state := controller.State()
			if !state.HistoryEffects.ReconciliationRequired &&
				!state.HistoryEffects.ProjectionUnknown &&
				!state.HistoryEffects.HasPending() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("history projection never converged: %#v", state.HistoryEffects)
			}
			flush()
		}
		flush()
	}

	// 阶段 1：正常交付（启动恢复/普通 transcript 更新），不得清除 scrollback。
	post(
		Resize{Width: width, Height: height, Generation: 1},
		ShowPromptAction{Line: "> "},
		ReplaceTranscriptAction{Snapshot: regressionCommittedSnapshot(1, cell(1))},
	)
	converge()
	if reset := session.ProjectionState().ScrollbackResetCount; reset != 0 {
		t.Fatalf("normal transcript delivery reset scrollback %d times", reset)
	}
	if rows := session.ProjectionState().HistoryRows; rows == 0 {
		t.Fatalf("normal transcript delivery left no resident history rows: %+v", session.ProjectionState())
	}
	afterPhase1 := physical.Len()
	markerCount1 := strings.Count(physical.String(), markers[0])
	if markerCount1 == 0 {
		t.Fatal("fixture never delivered the loaded cell")
	}

	// 阶段 2：/resume 同一修订（no-op 安装 + 重证明）—— 同身份内容不得重复追加。
	post(ReplaceTranscriptAction{
		Snapshot:            regressionCommittedSnapshot(1, cell(1)),
		ArmScrollbackReplay: true,
	})
	converge()

	state := controller.State()
	projection := session.ProjectionState()
	if state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("resume advanced the semantic epoch without a physical act: %#v", state.HistoryEffects)
	}
	if projection.ScrollbackResetCount != 0 {
		t.Fatalf("resume reset scrollback %d times, want 0: %+v", projection.ScrollbackResetCount, projection)
	}
	if strings.Contains(physical.String(), "\x1b[3J") {
		t.Fatal("resume wrote a scrollback reset sequence")
	}
	if physical.Len() < afterPhase1 {
		t.Fatal("physical output shrank across a resume")
	}
	if count := strings.Count(physical.String(), markers[0]); count != markerCount1 {
		t.Fatalf("reloading the same revision re-emitted the cell: marker count %d -> %d (append-only dedup)",
			markerCount1, count)
	}
	if projection.HistoryRows == 0 || !projection.HistoryKnown {
		t.Fatalf("resume left the history projection unpopulated: %+v", projection)
	}

	// 阶段 3：/resume 新修订（内容修正）—— 只追加，不清屏。
	post(ReplaceTranscriptAction{
		Snapshot:            regressionCommittedSnapshot(2, cell(2)),
		ArmScrollbackReplay: true,
	})
	converge()

	if resets := session.ProjectionState().ScrollbackResetCount; resets != 0 {
		t.Fatalf("corrected resume reset scrollback %d times, want 0", resets)
	}
	if strings.Contains(physical.String(), "\x1b[3J") {
		t.Fatal("corrected resume wrote a scrollback reset sequence")
	}
	if count := strings.Count(physical.String(), markers[0]); count <= markerCount1 {
		t.Fatalf("new revision was not appended after the load: marker count %d -> %d", markerCount1, count)
	}
	if projection := session.ProjectionState(); projection.HistoryRows == 0 || !projection.HistoryKnown {
		t.Fatalf("corrected resume left the history projection unpopulated: %+v", projection)
	}
}
