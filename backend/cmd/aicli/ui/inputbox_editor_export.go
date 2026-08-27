// inputbox_editor_export.go 提供 inputbox_editor 内部实现的少量导出包装，
// 供 commands 包在「stdin 不是真实控制台」的管道/PTY 终端场景复用逐键行编辑器。

package ui

import (
	"context"
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