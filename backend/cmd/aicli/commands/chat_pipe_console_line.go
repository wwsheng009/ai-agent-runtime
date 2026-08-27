// chat_pipe_console_line.go 处理 stdin 不是真实控制台（MobaXterm 本地
// shell/cygwin/mintty、winpty、SSH 管道等）时的逐键行输入。
//
// 这类环境下 ReadConsoleInputW 读不到按键记录，传统控制台行编辑器不可用；
// 若回退到 buffered 行读取（ReadString('\n')），backspace/Delete/方向键的
// 字节序列（\x7f、ESC[3~、ESC[C 等）会原样进入输入行，表现为「按退格没反应、
// 输入行里冒出 [3~、[C 之类的乱码」。这里改用 ui 包的字节逐键编辑器
// （ANSI 重绘 + 完整按键解析）接管输入。

package commands

import (
	"context"
	"io"
	"os"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// pipeFrameDiagWriter 在 --debug 下把编辑器每次 Write 的渲染帧原始字节以
// repr 形式转发到 stderr，用于真机取证：一眼确认帧格式、是否以 \n 结尾、
// 是否走了补偿模式。
type pipeFrameDiagWriter struct {
	w io.Writer
}

func (p *pipeFrameDiagWriter) Write(b []byte) (int, error) {
	if chatDebugFlagEnabled() {
		aicliDiagf("[aicli-diag] pipe-frame: %q\n", string(b))
	}
	return p.w.Write(b)
}

// readPipeInteractiveLineFn 是「管道/PTY 终端逐键行编辑器」的入口，测试可替换。
var readPipeInteractiveLineFn = readPipeInteractiveLine

// chatInteractiveInputPromptText 返回主循环读行前打印的提示符文本，用于把
// 桥接输出补偿帧的列对齐（整行重绘从提示符之后开始，避免覆盖提示符）。
func chatInteractiveInputPromptText(session *ChatSession) string {
	if session == nil {
		return ""
	}
	return ui.FormatUserPromptWithAttachments(len(session.ImagePaths))
}

// readPipeInteractiveLine 在 stdin/stdout 均为管道或字符设备（即非普通文件
// 重定向、非真实控制台）时，用 ui 包的逐键编辑器读取一行。返回 ok=false
// 表示当前环境不适合逐键编辑，调用方应保持原有 buffered 回退读取。
func readPipeInteractiveLine(ctx context.Context, prompt string) (string, bool, error) {
	if !pipeConsoleLineEditorSupported() {
		return "", false, nil
	}
	injected := chatDebugFlagEnabled()
	if injected {
		aicliDiagln("[aicli-diag] input path: pipe/PTY interactive line editor (byte-key editor, direct read)")
		ui.SetInteractiveInputDebugHook(aicliDiagf)
		defer ui.SetInteractiveInputDebugHook(nil)
	}
	writer := io.Writer(os.Stdout)
	if chatDebugFlagEnabled() {
		writer = &pipeFrameDiagWriter{w: os.Stdout}
	}
	line, err := ui.ReadInteractiveLineContextWithPrompt(ctx, os.Stdin, writer, prompt)
	if err != nil {
		return "", false, err
	}
	return line, true, nil
}

// pipeConsoleLineEditorSupported 判断 stdin/stdout 是否适合字节逐键编辑器：
// 两者都必须是管道或字符设备（不是普通文件重定向）。真实控制台场景由
// legacy 控制台行编辑器先行处理，不会走到这里。
func pipeConsoleLineEditorSupported() bool {
	if os.Stdin == nil || os.Stdout == nil {
		return false
	}
	if !fdIsPipeOrChar(os.Stdin) || !fdIsPipeOrChar(os.Stdout) {
		return false
	}
	return ui.SupportsCancelableInteractiveInputRead()
}

// chatPipeLineEditorPreferred 判断本会话是否应以「管道/PTY 逐键编辑器」为主
// 输入路径（MobaXterm 本地 shell、cygwin/mintty、winpty、SSH 管道等）：
//
//   - stdin/stdout 是管道或字符设备且 ui 编辑器支持（pipeConsoleLineEditorSupported）；
//   - legacy 控制台行编辑器不可用（非真实 conhost，ReadConsoleInputW 无按键记录）。
//
// 为真时 runChatLoop 不启动 queue/pump，主循环 chatInteractiveReadLine 的
// 回退分支独占 stdin 打开逐键编辑器，避免 pump 后台读取与其竞争同一句柄。
func chatPipeLineEditorPreferred() bool {
	if !pipeConsoleLineEditorSupported() {
		return false
	}
	if !chatLegacyConsoleInputEnabled() {
		return true
	}
	return !legacyConsoleLineEditorUsable()
}

func fdIsPipeOrChar(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	m := fi.Mode()
	return m&os.ModeNamedPipe != 0 || m&os.ModeCharDevice != 0
}