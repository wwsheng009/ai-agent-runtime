//go:build windows

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
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
	counter := &scrollbackClearCounter{inner: os.Stdout}
	executor := ui.NewTerminalSessionExecutor(controller, ui.NewTerminalSession(counter))
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
	assertAppendOnly(controller)

	// Session load: re-publish the same Scene with the load marker. The load
	// must re-prove the plan without re-emitting the delivered rows and without
	// clearing native scrollback.
	post(controller, ui.ReplaceTranscriptAction{
		Snapshot:            fixtureSnapshot(historyCount),
		ArmScrollbackReplay: true,
	})
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	assertHistoryAcknowledged(controller)
	assertAppendOnly(controller)

	// Append after the load: the new row must be appended by the ordinary
	// ordered handoff while every earlier row stays exactly once.
	appended := fixtureSnapshot(historyCount)
	appended.Cells = append(appended.Cells, &scene.TranscriptCell{
		ID:       scene.CellID(historyCount + 2),
		Sequence: uint64(historyCount + 2),
		Revision: 1,
		Kind:     scene.KindAssistant,
		Source:   "AICLI-E2E-HISTORY-072",
		Phase:    scene.CellCommitted,
	})
	post(controller, ui.ReplaceTranscriptAction{Snapshot: appended})
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	assertHistoryAcknowledged(controller)
	assertAppendOnly(controller)

	fmt.Fprintf(os.Stdout, "AICLI-E2E-CLEAR-3J=%d\r\n", counter.count())
	fmt.Fprintf(os.Stdout, "\x1b]0;AICLI-E2E-READY-%s\x07", runID)
	hold := 30 * time.Second
	if milliseconds, parseErr := strconv.Atoi(os.Getenv("AICLI_RENDER_FIXTURE_HOLD_MS")); parseErr == nil && milliseconds > 0 {
		hold = time.Duration(milliseconds) * time.Millisecond
	}
	time.Sleep(hold)
}

func assertHistoryAcknowledged(controller *ui.UIController) {
	for _, entry := range controller.State().HistoryEffects.Entries() {
		if entry.State != ui.HistoryCommitDelivered {
			fmt.Fprintf(os.Stderr, "unresolved history effect: %#v\n", entry)
			os.Exit(3)
		}
	}
}

// assertAppendOnly fails the fixture when a load/append phase left a recovery
// obligation or advanced the terminal epoch (append-only delivery never does).
func assertAppendOnly(controller *ui.UIController) {
	state := controller.State()
	if state.HistoryEffects.TerminalEpoch != 0 ||
		state.HistoryEffects.ProjectionUnknown ||
		state.HistoryEffects.ReconciliationRequired {
		fmt.Fprintf(os.Stderr, "load/append left append-only state: %#v\n", state.HistoryEffects)
		os.Exit(3)
	}
}

// scrollbackClearCounter counts CSI 3J sequences crossing the session writer,
// including sequences split across Write calls. It only observes bytes; the
// fixture prints the final count for the e2e script to assert zero.
type scrollbackClearCounter struct {
	inner   io.Writer
	mu      sync.Mutex
	tail    []byte
	count3J int
}

func (c *scrollbackClearCounter) Write(data []byte) (int, error) {
	c.mu.Lock()
	combined := append(append([]byte(nil), c.tail...), data...)
	c.count3J += bytes.Count(combined, []byte("\x1b[3J"))
	if len(combined) >= 2 {
		c.tail = append(c.tail[:0], combined[len(combined)-2:]...)
	} else {
		c.tail = append(c.tail[:0], combined...)
	}
	c.mu.Unlock()
	return c.inner.Write(data)
}

func (c *scrollbackClearCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count3J
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
