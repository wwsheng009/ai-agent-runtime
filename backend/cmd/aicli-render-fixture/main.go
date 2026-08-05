//go:build windows

package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"golang.org/x/term"
)

const historyCount = 72

const markdownFixtureSource = "# AICLI-E2E-MARKDOWN-HEADING\n\n**AICLI-E2E-MARKDOWN-BOLD**\n\n`AICLI-E2E-MARKDOWN-CODE`"

func main() {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "aicli-render-fixture requires a real terminal")
		os.Exit(2)
	}
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width < 40 || height < 10 {
		fmt.Fprintf(os.Stderr, "invalid terminal geometry %dx%d: %v\n", width, height, err)
		os.Exit(2)
	}
	runID := os.Getenv("AICLI_RENDER_FIXTURE_RUN_ID")
	if runID == "" {
		runID = "manual"
	}
	fmt.Fprintf(os.Stdout, "AICLI-E2E-BUFFER-%s\r\n", runID)

	controller := ui.NewUIController(ui.UIControllerConfig{}, nil, nil)
	go controller.Run()
	executor := ui.NewTerminalSessionExecutor(controller, ui.NewTerminalSession(os.Stdout))
	defer func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	}()

	post(controller, ui.Resize{Width: width, Height: height, Generation: 1})
	post(controller, ui.SetStatusModelAction{Status: style.StatusLineModel{
		State: style.RunReady, StateText: "AICLI-E2E-STATUS-VIEWPORT",
	}})
	post(controller, ui.ShowPromptAction{Line: "AICLI-E2E-PROMPT-VIEWPORT> "})
	post(controller, ui.ReplaceTranscriptAction{Snapshot: fixtureSnapshot(historyCount / 2)})
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	assertHistoryAcknowledged(controller)

	post(controller, ui.SetStatusModelAction{Status: style.StatusLineModel{
		State: style.RunReady, StateText: "AICLI-E2E-STATUS-VIEWPORT",
	}})
	post(controller, ui.ShowPromptAction{Line: "AICLI-E2E-PROMPT-VIEWPORT> "})
	post(controller, ui.ReplaceTranscriptAction{Snapshot: fixtureSnapshot(historyCount)})
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	assertHistoryAcknowledged(controller)

	fmt.Fprintf(os.Stdout, "\x1b]0;AICLI-E2E-READY-%s\x07", runID)
	hold := 30 * time.Second
	if milliseconds, parseErr := strconv.Atoi(os.Getenv("AICLI_RENDER_FIXTURE_HOLD_MS")); parseErr == nil && milliseconds > 0 {
		hold = time.Duration(milliseconds) * time.Millisecond
	}
	time.Sleep(hold)
}

func assertHistoryAcknowledged(controller *ui.UIController) {
	for _, entry := range controller.State().HistoryEffects.Entries() {
		if entry.State != ui.HistoryCommitAcked {
			fmt.Fprintf(os.Stderr, "unresolved history effect: %#v\n", entry)
			os.Exit(3)
		}
	}
}

func post(controller *ui.UIController, action ui.UIAction) {
	if !controller.Post(action) {
		fmt.Fprintf(os.Stderr, "failed to post %T\n", action)
		os.Exit(3)
	}
}

func fixtureSnapshot(count int) *scene.Snapshot {
	cells := make([]*scene.TranscriptCell, 0, count+1)
	cells = append(cells, &scene.TranscriptCell{
		ID:       scene.CellID(historyCount + 1),
		Sequence: 1,
		Revision: 1,
		Kind:     scene.KindAssistant,
		Source:   markdownFixtureSource,
		Phase:    scene.CellCommitted,
	})
	for index := 0; index < count; index++ {
		cells = append(cells, &scene.TranscriptCell{
			ID:       scene.CellID(index + 1),
			Sequence: uint64(index + 2),
			Revision: 1,
			Kind:     scene.KindAssistant,
			Source:   fmt.Sprintf("AICLI-E2E-HISTORY-%03d", index),
			Phase:    scene.CellCommitted,
		})
	}
	return &scene.Snapshot{Revision: 1, Cells: cells}
}
