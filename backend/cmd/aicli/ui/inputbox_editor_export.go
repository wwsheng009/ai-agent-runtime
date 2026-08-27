package ui

import (
	"context"
	"fmt"
	"io"
)

// ReadInteractiveLineContext 是 readInteractiveLineWithHooksContext 的导出包装：
// 以默认设置（无历史、无变更回调、提交时回显、默认粘贴保持首字符）在
// reader/writer 上读取一行交互输入。适用于 stdin 不是真实控制台、但终端
// 支持 ANSI 重绘的场景（MobaXterm/cygwin/mintty、winpty、SSH 管道等），
// 完整解析 backspace、Delete、方向键、Home/End 等按键。
func ReadInteractiveLineContext(ctx context.Context, reader io.Reader, writer io.Writer) (string, error) {
	return readInteractiveLineWithHooksContext(ctx, reader, writer, "", nil, nil, nil, true, defaultPasteBurstHoldFirstRune())
}

// InteractiveInputDebugHook 接收交互行编辑器（字节逐键路径）的按键调试
// 输出。默认 nil：不输出任何内容。chat 命令在 --debug 下注入 [aicli-diag]
// 输出钩子（aicliDiagf），用于 MobaXterm/cygwin/winpty 等管道终端下取证
// backspace/Delete/方向键是否被正确解码。
type InteractiveInputDebugHook func(format string, args ...any)

var interactiveInputDebug InteractiveInputDebugHook

// SetInteractiveInputDebugHook 设置/清除（传 nil）交互输入调试钩子。
func SetInteractiveInputDebugHook(h InteractiveInputDebugHook) {
	interactiveInputDebug = h
}

func interactiveInputDebugf(format string, args ...any) {
	if interactiveInputDebug != nil {
		interactiveInputDebug(format, args...)
	}
}

var editorKeyKindNames = map[editorKeyKind]string{
	editorKeyIgnore:             "Ignore",
	editorKeyRune:               "Rune",
	editorKeyEnter:              "Enter",
	editorKeyInsertNewline:      "InsertNewline",
	editorKeyComplete:           "Complete",
	editorKeyCancelPopup:        "CancelPopup",
	editorKeyBackspace:          "Backspace",
	editorKeyDelete:             "Delete",
	editorKeyLeft:               "Left",
	editorKeyRight:              "Right",
	editorKeyUp:                 "Up",
	editorKeyDown:               "Down",
	editorKeyPageUp:             "PageUp",
	editorKeyPageDown:           "PageDown",
	editorKeyHome:               "Home",
	editorKeyEnd:                "End",
	editorKeyClearLine:          "ClearLine",
	editorKeyDeleteWord:         "DeleteWord",
	editorKeyKillToEnd:          "KillToEnd",
	editorKeyDeleteForwardWord:  "DeleteForwardWord",
	editorKeyRedraw:             "Redraw",
	editorKeyYank:               "Yank",
	editorKeyTranspose:          "Transpose",
	editorKeyBackwardWord:       "BackwardWord",
	editorKeyForwardWord:        "ForwardWord",
	editorKeyReverseSearch:      "ReverseSearch",
	editorKeyAbortSearch:        "AbortSearch",
	editorKeyPasteStart:         "PasteStart",
	editorKeyPasteEnd:           "PasteEnd",
	editorKeyPasteClipboard:     "PasteClipboard",
	editorKeyInterrupt:          "Interrupt",
	editorKeyEOF:                "EOF",
	editorKeyFocusGained:        "FocusGained",
	editorKeyFocusLost:          "FocusLost",
}

// interactiveInputDebugKey 在逐键编辑器解码出一个按键时输出调试信息：
// 原始字节序列（%q 转义）+ 解析出的按键种类；普通字符按键额外带 rune。
func interactiveInputDebugKey(k editorKey, raw []byte) {
	name, ok := editorKeyKindNames[k.kind]
	if !ok {
		name = fmt.Sprintf("Kind(%d)", int(k.kind))
	}
	if k.kind == editorKeyRune {
		interactiveInputDebugf("[aicli-diag] pipe-key: raw=%q -> %s(%q)\n", raw, name, k.r)
		return
	}
	if raw == nil {
		interactiveInputDebugf("[aicli-diag] pipe-key: special -> %s\n", name)
		return
	}
	interactiveInputDebugf("[aicli-diag] pipe-key: raw=%q -> %s\n", raw, name)
}