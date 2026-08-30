package ui

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// VtTerminalEmulator 是 TerminalEmulator 的 ui/vt-backed adapter（7.3）：
// 复用 ui/vt.Screen 解释已经编码的 ANSI bytes，并在 adapter 层跟踪
// ui/vt.Screen 未覆盖的 alternate-screen 与光标可见性/形状。
//
// 设计约束：render/output 不得 import ui/vt（ui/vt 依赖 ui/render），
// 因此该 adapter 放在 ui 层，由 fixture/command setup 注入 virtual sink。
type VtTerminalEmulator struct {
	mu     sync.Mutex
	width  int
	height int
	// screen 是当前活动屏幕（primary 或 alternate）。
	screen *vt.Screen
	// primary 是 alternate 进入前的 primary 屏幕（DEC 1049 退出时恢复）。
	screenAlt   bool
	primary     *vt.Screen
	alternate   *vt.Screen
	cursorVis   bool
	cursorShape outputpkg.CursorShape
	invalid     bool
}

// NewVtTerminalEmulator 创建 emulator（初始 80x24、隐藏光标）。
func NewVtTerminalEmulator() *VtTerminalEmulator {
	s := vt.NewScreen(80, 24)
	return &VtTerminalEmulator{
		width:       80,
		height:      24,
		screen:      s,
		primary:     s,
		cursorVis:   false,
		cursorShape: outputpkg.CursorShapeBlock,
	}
}

// ApplyContext 原子设置 geometry/profile。geometry 非法时返回错误且不
// 改变当前屏幕。尺寸变化时把旧屏内容迁移到新屏（文本布局保留；SGR 样式
// 是诊断观察面，允许丢失；vt 无 Resize API，用行文本重建）。
func (e *VtTerminalEmulator) ApplyContext(g outputpkg.TerminalGeometry, _ outputpkg.TerminalProfileRef) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if g.Width < 1 || g.Height < 1 {
		return outputpkg.NewClassifiedError(outputpkg.DeliveryErrorInvalid, "invalid emulator geometry")
	}
	if g.Width == e.width && g.Height == e.height {
		return nil
	}
	// 迁移当前屏与 primary（若在 alternate 中，两者都按新尺寸重建）。
	cur := e.resizeScreen(e.screen, g.Width, g.Height)
	if e.screenAlt {
		e.primary = e.resizeScreen(e.primary, g.Width, g.Height)
		e.alternate = cur
	} else {
		e.primary = cur
		e.alternate = nil
	}
	e.screen = cur
	e.width, e.height = g.Width, g.Height
	return nil
}

// resizeScreen 按新尺寸重建屏幕：保留旧屏前 min(height) 行的文本布局。
// 行间用 \n 分隔，但末尾不写 \n——避免在"新 height ≤ 旧 height"时末行
// 触底触发 scroll（把首行推入 scrollback、底部留空行）。scrollback
// 不迁移（观察面取舍）；变窄 wrap 与真实终端 reflow 一致。
func (e *VtTerminalEmulator) resizeScreen(old *vt.Screen, width, height int) *vt.Screen {
	next := vt.NewScreen(width, height)
	if old == nil {
		return next
	}
	rows := old.Lines(1, minInt(height, old.Height()))
	var b strings.Builder
	for i, row := range rows {
		b.WriteString(row)
		if i < len(rows)-1 {
			b.WriteByte('\n')
		}
	}
	if b.Len() > 0 {
		next.Feed(b.String())
	}
	// 恢复光标位置（重建后停在末尾行首；原 cursor 是提示符后的位置）。
	oldRow, oldCol := old.CursorRow(), old.CursorCol()
	next.Feed(fmt.Sprintf("\x1b[%d;%dH", clampCursor(oldRow, height), clampCursor(oldCol, width)))
	return next
}

func clampCursor(pos, bound int) int {
	if pos < 1 {
		return 1
	}
	if pos > bound {
		return bound
	}
	return pos
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Apply 解释 bytes；返回 nil 表示成功（本 adapter 不产生解释错误）。
func (e *VtTerminalEmulator) Apply(bytes []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.invalid {
		// invalid 后仍可继续喂 bytes（诊断），但快照保持 unknown。
	}
	stream := string(bytes)
	e.screen.Feed(stream)
	e.trackCursorAndAlternate(stream)
	return nil
}

// trackCursorAndAlternate 解析 DEC 1049 与光标可见/形状序列。
func (e *VtTerminalEmulator) trackCursorAndAlternate(stream string) {
	if strings.Contains(stream, "\x1b[?1049h") {
		if !e.screenAlt {
			// 保存 primary，切换 alternate（新空屏）。
			e.primary = e.screen
			e.alternate = vt.NewScreen(e.width, e.height)
			e.screen = e.alternate
			e.screenAlt = true
		}
	}
	if strings.Contains(stream, "\x1b[?1049l") {
		if e.screenAlt {
			// 恢复 primary 屏幕。
			e.screen = e.primary
			e.screenAlt = false
		}
	}
	if strings.Contains(stream, "\x1b[?25h") {
		e.cursorVis = true
	}
	if strings.Contains(stream, "\x1b[?25l") {
		e.cursorVis = false
	}
	// DECSCUSR：CSI Ps SP q / CSI Ps q 设置光标形状。
	e.trackCursorShape(stream)
}

// decscusrRe 匹配 DECSCUSR 形状序列（CSI Ps SP q）。预编译避免每次调用
// 正则编译开销。
var decscusrRe = regexp.MustCompile(`\x1b\[([0-9]) q`)

// trackCursorShape 解析 DECSCUSR 形状序列：
// 0/1 → block、2 → block(blink 变体)、3 → underline、4 → underline(blink)、
// 5 → bar、6 → bar(blink)。vn/vt 不跟踪形状，adapter 层维护。
func (e *VtTerminalEmulator) trackCursorShape(stream string) {
	m := decscusrRe.FindStringSubmatch(stream)
	if len(m) != 2 {
		return
	}
	switch m[1] {
	case "0", "1", "2":
		e.cursorShape = outputpkg.CursorShapeBlock
	case "3", "4":
		e.cursorShape = outputpkg.CursorShapeUnderline
	case "5", "6":
		e.cursorShape = outputpkg.CursorShapeBar
	}
}

// Invalidate 把投影标为不可证明。
func (e *VtTerminalEmulator) Invalidate() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.invalid = true
}

// InvalidateZap 恢复可证明状态（Phase 2 仅用于重建后恢复）。
func (e *VtTerminalEmulator) InvalidateClear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.invalid = false
}

// Snapshot 返回 detached 快照（row 基于当前屏幕；alternate 状态独立）。
func (e *VtTerminalEmulator) Snapshot() outputpkg.VirtualProjectionSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	screen := e.screen
	rows := screen.Lines(1, e.height)
	scrollback := screen.ScrollbackLines()
	cursor := outputpkg.TerminalCursor{
		Row:     screen.CursorRow() - 1,
		Column:  screen.CursorCol() - 1,
		Visible: e.cursorVis,
		Shape:   e.cursorShape,
	}
	if cursor.Row < 0 {
		cursor.Row = 0
	}
	if cursor.Column < 0 {
		cursor.Column = 0
	}
	validity := outputpkg.ProjectionValid
	if e.invalid {
		validity = outputpkg.ProjectionUnknown
	}
	return outputpkg.VirtualProjectionSnapshot{
		SchemaVersion: outputpkg.SchemaVersion,
		Width:         e.width,
		Height:        e.height,
		Rows:          rows,
		Scrollback:    scrollback,
		Cursor:        cursor,
		Alternate:     e.screenAlt,
		Validity:      validity,
	}
}
