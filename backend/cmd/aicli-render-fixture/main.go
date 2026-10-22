//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"golang.org/x/term"
)

const historyCount = 72

// coldPrefixKeep 是 T9 R1 冷前缀模式的驻留窗口：history 交付后把前缀写穿并
// 从后续快照里裁掉（等价于 bridge 的写穿裁剪），窗口内保留最后这么多 cell。
const coldPrefixKeep = 24

// fixtureTrimStart 是冷前缀逐出后的窗口起点（0 = 未裁剪）。所有后续阶段的
// 快照都从这里开始构造，模拟"生产逐出后 bridge 只持有窗口"的真实形态：
// 若后续阶段重新发布全量快照，已逐出前缀会被"接回来"，冷前缀门禁就失效了。
var fixtureTrimStart int

// streamDeltaCount is the number of mutable-cell revisions in the streaming
// phase; the host script samples the real terminal while they are applied.
const streamDeltaCount = 16

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
	coldPrefixSummary := ""

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

	// Streaming phase: a mutable assistant cell grows one line per delta while
	// the host samples the real terminal. The structured streaming window path
	// (P3-S4b) must render every prefix exactly once in the active band, and the
	// finalized content must hand off to native scrollback exactly once. The
	// host detects the phase from the numbered stream markers themselves; a
	// "begin" line would be overwritten by the active band repaint.
	streamLines := make([]string, 0, streamDeltaCount)
	for step := 1; step <= streamDeltaCount; step++ {
		streamLines = append(streamLines, fmt.Sprintf("AICLI-E2E-STREAM-%03d", step-1))
		post(controller, ui.ReplaceTranscriptAction{
			Snapshot: streamFixtureSnapshot(uint64(step), strings.Join(streamLines, "\n\n"), false),
		})
		controller.WaitIdle()
		executor.Request()
		executor.WaitIdle()
		controller.WaitIdle()
		time.Sleep(300 * time.Millisecond)
	}
	post(controller, ui.ReplaceTranscriptAction{
		Snapshot: streamFixtureSnapshot(uint64(streamDeltaCount+1), strings.Join(streamLines, "\n\n"), true),
	})
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	assertHistoryAcknowledged(controller)
	assertAppendOnly(controller)

	// T9 R1：冷前缀模式（AICLI_RENDER_FIXTURE_COLD_PREFIX=1）。
	// 必须放在最后：earlier phases 会重新发布完整快照，若冷前缀阶段先跑，
	// 后续完整快照会把裁掉的前缀"接回去"（本项目实测：marker 全在、等于没裁）。
	// 这里对**当前**已交付内容写穿并裁剪，与生产逐出路径同一语义（写穿成功才裁剪）。
	// 若 uiActor/ledger 在快照变短后重铸前缀，终端文档里就会出现重复 marker。
	if os.Getenv("AICLI_RENDER_FIXTURE_COLD_PREFIX") == "1" {
		coldPrefixSummary = runColdPrefixPhase(controller, executor)
	}

	// 结束标记在冷前缀阶段之后打印：该阶段会重绘内联视口，先打印会被擦掉
	// （实测 STREAM-END 在文档里消失，脚本 readiness 永不满足）。
	fmt.Fprintf(os.Stdout, "AICLI-E2E-STREAM-END\r\n")
	fmt.Fprintf(os.Stdout, "AICLI-E2E-CLEAR-3J=%d\r\n", counter.count())
	// 冷前缀证据在**结束时**输出：运行中的打印会被 active band 重绘覆盖
	// （与流式阶段 "begin" 行同一现象），UIA 文档里就抓不到。
	if coldPrefixSummary != "" {
		fmt.Fprintf(os.Stdout, "AICLI-E2E-COLD-PREFIX=%s\r\n", coldPrefixSummary)
	}
	// 上述 raw 打印落在"内联视口最后一行的下一行"。冷前缀阶段把视口从满屏缩到
	// 窗口后，这一行正好压在固定底部区（状态行）上——实测 STATUS 标记因此消失。
	// 结论：raw 打印之后必须再重绘一次底部区，且用过渡文案破掉状态模型的值去重。
	post(controller, ui.SetStatusModelAction{Status: style.StatusLineModel{
		State: style.RunReady, StateText: "AICLI-E2E-STATUS-FINAL-REPAINT",
	}})
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	post(controller, ui.SetStatusModelAction{Status: style.StatusLineModel{
		State: style.RunReady, StateText: "AICLI-E2E-STATUS-VIEWPORT",
	}})
	post(controller, ui.ShowPromptAction{Line: "AICLI-E2E-PROMPT-VIEWPORT> "})
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
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

// runColdPrefixPhase 对当前已交付内容执行一次"写穿 + 裁剪"：
//   - 把前 len(full)-coldPrefixKeep 个 cell 写穿到内存 store；
//   - 把同样的裁剪结果作为新快照投给 UI（不 arm replay，不清 scrollback）；
//   - 逐 cell 读回校验（store → 指纹）；
//   - 输出 AICLI-E2E-COLD-PREFIX 证据行供脚本断言。
func runColdPrefixPhase(controller *ui.UIController, executor *ui.TerminalSessionExecutor) string {
	full := controller.State().AppState.Transcript.Snapshot()
	if full == nil || len(full.Cells) == 0 {
		fmt.Fprintln(os.Stderr, "cold-prefix: controller has no transcript to evict")
		os.Exit(3)
	}
	// fixtureSnapshot 除 72 个 history marker 外还带一个 markdown cell；按
	// 「驻留 cell 数 = coldPrefixKeep」裁剪，逐出数因此是 len(full)-keep。
	evictCount := len(full.Cells) - coldPrefixKeep
	if evictCount <= 0 {
		return ""
	}
	// 后续阶段（load/append/stream）若重新发布全量快照，会把刚裁掉的前缀
	// "接回来"（实测：冷前缀阶段前置时 72 个 marker 全在）。因此本阶段放在
	// 流程末尾，并把裁剪起点记进 fixtureTrimStart，供任何后续快照构造函数使用。
	fixtureTrimStart = len(full.Cells) - coldPrefixKeep
	store := ui.NewMemoryCellBodyStore()
	persisted := 0
	for _, cell := range full.Cells[:evictCount] {
		body, err := ui.MarshalTranscriptCellBody(*cell, 1, 1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cold-prefix: marshal cell %d: %v\n", cell.ID, err)
			os.Exit(3)
		}
		if err := store.Put(context.Background(), "fixture-cold-prefix", body); err != nil {
			fmt.Fprintf(os.Stderr, "cold-prefix: write-through cell %d: %v\n", cell.ID, err)
			os.Exit(3)
		}
		persisted++
	}
	readback := 0
	for _, cell := range full.Cells[:evictCount] {
		body, ok, err := store.Get(context.Background(), "fixture-cold-prefix", cell.ID, cell.Revision)
		if err != nil || !ok {
			fmt.Fprintf(os.Stderr, "cold-prefix: readback cell %d ok=%v err=%v\n", cell.ID, ok, err)
			os.Exit(3)
		}
		if _, err := ui.ValidateTranscriptCellBody(body); err != nil {
			fmt.Fprintf(os.Stderr, "cold-prefix: validate cell %d: %v\n", cell.ID, err)
			os.Exit(3)
		}
		readback++
	}
	trimmed := &scene.Snapshot{Revision: full.Revision, Cells: full.Cells[evictCount:]}
	post(controller, ui.ReplaceTranscriptAction{Snapshot: trimmed})
	// 缩短快照的整帧替换会重画内联视口（可能擦掉状态行）。状态模型按值去重：
	// 先切到过渡文案强制重绘，再切回 E2E 文案，保证脚本的"各恰好一次"断言在
	// 冷前缀模式下同样成立。
	post(controller, ui.SetStatusModelAction{Status: style.StatusLineModel{
		State: style.RunReady, StateText: "AICLI-E2E-STATUS-COLD-REPAINT",
	}})
	controller.WaitIdle()
	post(controller, ui.SetStatusModelAction{Status: style.StatusLineModel{
		State: style.RunReady, StateText: "AICLI-E2E-STATUS-VIEWPORT",
	}})
	post(controller, ui.ShowPromptAction{Line: "AICLI-E2E-PROMPT-VIEWPORT> "})
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	assertHistoryAcknowledged(controller)
	assertAppendOnly(controller)
	return fmt.Sprintf("persisted:%d,readback:%d,resident:%d", persisted, readback, len(trimmed.Cells))
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
	full := make([]*scene.TranscriptCell, 0, count+1)
	full = append(full, &scene.TranscriptCell{
		ID:       scene.CellID(historyCount + 1),
		Sequence: 1,
		Revision: 1,
		Kind:     scene.KindAssistant,
		Source:   markdownFixtureSource,
		Phase:    scene.CellCommitted,
	})
	for index := 0; index < count; index++ {
		full = append(full, &scene.TranscriptCell{
			ID:       scene.CellID(index + 1),
			Sequence: uint64(index + 2),
			Revision: 1,
			Kind:     scene.KindAssistant,
			Source:   fmt.Sprintf("AICLI-E2E-HISTORY-%03d", index),
			Phase:    scene.CellCommitted,
		})
	}
	cells := full
	if fixtureTrimStart > 0 {
		if fixtureTrimStart >= len(full) {
			cells = nil
		} else {
			cells = full[fixtureTrimStart:]
		}
	}
	return &scene.Snapshot{Revision: 1, Cells: cells}
}

// streamFixtureSnapshot keeps the delivered append row in place and appends a
// mutable (or finalized) streaming assistant cell at the transcript tail.
func streamFixtureSnapshot(revision uint64, source string, committed bool) *scene.Snapshot {
	snapshot := fixtureSnapshot(historyCount)
	snapshot.Cells = append(snapshot.Cells, &scene.TranscriptCell{
		ID:       scene.CellID(historyCount + 2),
		Sequence: uint64(historyCount + 2),
		Revision: 1,
		Kind:     scene.KindAssistant,
		Source:   "AICLI-E2E-HISTORY-072",
		Phase:    scene.CellCommitted,
	})
	phase := scene.CellMutable
	if committed {
		phase = scene.CellCommitted
	}
	snapshot.Cells = append(snapshot.Cells, &scene.TranscriptCell{
		ID:       scene.CellID(historyCount + 3),
		Sequence: uint64(historyCount + 3),
		Revision: revision,
		Kind:     scene.KindAssistant,
		Source:   source,
		Phase:    phase,
	})
	snapshot.Revision = revision
	return snapshot
}
