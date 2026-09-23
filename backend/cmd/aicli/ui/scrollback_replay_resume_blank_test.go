package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 需求：/resume（ReplaceTranscriptAction + ArmScrollbackReplay）的销毁式重放必须把
// 常驻历史区重新填满。这是 live 事故 session_20260919224618_bXDmrK6x 的固化：
// 进程在启动恢复时正常交付了整份 transcript（append_count=8793），随后一次 armed
// replay 执行了 \x1b[3J，但重放没有再投递任何行，于是历史区永久空白
// （projection.history_rows=0 / history_known=true / pending=0，且执行器空闲）。
//
// 该测试断言的是物理结果，而不是"计划里有内容"：reset 之后必须真的写出 transcript。
func TestArmedResumeReplayRepopulatesResidentHistory(t *testing.T) {
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

	// 阶段 2：/resume —— 同一份 transcript 以 armed replay 重新装载。
	post(ReplaceTranscriptAction{
		Snapshot:            regressionCommittedSnapshot(2, cell(2)),
		ArmScrollbackReplay: true,
	})
	converge()

	state := controller.State()
	projection := session.ProjectionState()
	if projection.ScrollbackResetCount != 1 {
		t.Fatalf("armed resume performed %d scrollback replacements, want exactly one: %+v",
			projection.ScrollbackResetCount, projection)
	}
	if state.HistoryEffects.TerminalEpoch == 0 {
		t.Fatalf("armed resume did not open a fresh terminal epoch: %#v", state.HistoryEffects)
	}
	raw := physical.String()
	reset := strings.LastIndex(raw, "\x1b[3J")
	if reset < 0 {
		t.Fatal("armed resume never physically replaced scrollback")
	}
	replayed := raw[reset:]
	if !strings.Contains(replayed, markers[0]) || !strings.Contains(replayed, markers[len(markers)-1]) {
		t.Fatalf("scrollback replacement was not followed by a transcript replay: %q", replayed)
	}
	if projection.HistoryRows == 0 {
		t.Fatalf("resident history region is empty after the armed replay: %+v", projection)
	}
	if !projection.HistoryKnown {
		t.Fatalf("armed replay did not prove the history projection: %+v", projection)
	}
}
