package commands

import (
	"bufio"
	"context"
	"sync/atomic"
)

// chatConsoleLineInputMode 标记当前会话处于「无 ANSI 的交互终端降级模式」
// （Windows 7 原生 conhost），并选择具体的控制台行读取策略。
type chatConsoleLineInputMode int32

const (
	chatConsoleLineInputDisabled chatConsoleLineInputMode = iota
	chatConsoleLineInputAuto
	chatConsoleLineInputCustom
	chatConsoleLineInputSystem
)

const (
	chatConsoleInputAuto   = "auto"
	chatConsoleInputSystem = "system"
	chatConsoleInputCustom = "custom"
)

// 自定义模式解决传统 conhost 行编辑的两个问题：
//
//  1. conhost 在输出代码页 65001（UTF-8）下做字节级行编辑：中文等多字节字符
//     按字节退格，退格后的剩余字节成乱码，表现为「backspace 删不动字符」；
//  2. 传统 conhost 行编辑根本不支持 Delete 键（VK_DELETE 不进入行缓冲）。
//
// system 模式则直接调用 ReadConsoleW，并保留 ENABLE_LINE_INPUT /
// ENABLE_ECHO_INPUT，让 conhost 承担 IME 组合、候选词和提交。auto 默认优先
// system；若系统 Unicode 行读取不可用，再回退 custom，最后回退 buffered。
var chatConsoleInputMode atomic.Int32

// chatDebugMode 控制诊断输出（[aicli-diag] 前缀），仅 --debug 时打印。
var chatDebugMode atomic.Bool

func setChatLegacyConsoleInputMode(v bool) {
	if v {
		chatConsoleInputMode.Store(int32(chatConsoleLineInputCustom))
		return
	}
	resetChatConsoleInputMode()
}

func resetChatConsoleInputMode() {
	chatConsoleInputMode.Store(int32(chatConsoleLineInputDisabled))
}

func setChatConsoleInputMode(mode string) {
	switch mode {
	case "", chatConsoleInputAuto:
		chatConsoleInputMode.Store(int32(chatConsoleLineInputAuto))
	case chatConsoleInputSystem:
		chatConsoleInputMode.Store(int32(chatConsoleLineInputSystem))
	case chatConsoleInputCustom:
		chatConsoleInputMode.Store(int32(chatConsoleLineInputCustom))
	default:
		resetChatConsoleInputMode()
	}
}

func setChatDebugFlag(v bool) {
	chatDebugMode.Store(v)
}

func chatDebugFlagEnabled() bool {
	return chatDebugMode.Load()
}

func chatLegacyConsoleInputEnabled() bool {
	return currentChatConsoleInputMode() != chatConsoleLineInputDisabled
}

func currentChatConsoleInputMode() chatConsoleLineInputMode {
	return chatConsoleLineInputMode(chatConsoleInputMode.Load())
}

// readLegacyConsoleLineFn 是传统控制台行编辑器的探测/读取入口，测试可替换。
var readLegacyConsoleLineFn = readLegacyConsoleLine

// readSystemConsoleLineFn 是保留 Win7 conhost IME 的 Unicode cooked 行读取入口。
var readSystemConsoleLineFn = readSystemConsoleLine

// readConfiguredChatConsoleLine applies the selected console reader. Auto keeps
// the custom editor as a compatibility fallback when system cooked input is
// unavailable; explicit system/custom modes never silently switch editors.
func readConfiguredChatConsoleLine(ctx context.Context) (string, bool, error) {
	switch currentChatConsoleInputMode() {
	case chatConsoleLineInputAuto:
		if line, ok, err := readSystemConsoleLineFn(ctx); ok {
			return line, true, err
		}
		return readLegacyConsoleLineFn(ctx)
	case chatConsoleLineInputSystem:
		return readSystemConsoleLineFn(ctx)
	case chatConsoleLineInputCustom:
		return readLegacyConsoleLineFn(ctx)
	default:
		return "", false, nil
	}
}

// readChatSessionLine 读取一行交互输入。
//
// 降级模式按配置优先走系统 Unicode cooked 输入或自定义控制台编辑器；当
// stdin 不是控制台（管道/重定向/测试注入的 reader）或编辑器不可用
// （GetConsoleMode 失败）时回退到 buffered 读取，行为与旧版完全一致。
//
// 注意：本函数会被 queue 的 pump 后台 goroutine（stdinReadLoop）调用，
// 不能在这里打开逐键编辑器（readPipeInteractiveLineFn）——pump 与主循环
// 会并发抢读同一 stdin，编辑器收不到键。管道/PTY 场景（MobaXterm、cygwin/
// mintty、winpty、SSH 管道）由运行路径选择决定不启动 queue/pump，改走
// chatInteractiveReadLine 的回退分支独占 stdin 逐键读取。
func readChatSessionLine(ctx context.Context, reader *bufio.Reader) (string, error) {
	if line, ok, err := readConfiguredChatConsoleLine(ctx); ok {
		if err != nil {
			return "", err
		}
		return line, nil
	}
	if reader == nil {
		reader = newChatInputReader()
	}
	line, err := reader.ReadString('\n')
	if line != "" {
		return line, err
	}
	return "", err
}
