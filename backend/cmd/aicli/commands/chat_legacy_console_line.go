package commands

import (
	"bufio"
	"context"
	"sync/atomic"
)

// chatLegacyConsoleInputMode 标记当前会话处于「无 ANSI 的交互终端降级模式」
// （windows 7 原生 conhost）。该模式下终端的 cooked 行编辑有两个致命缺陷：
//
//  1. conhost 在输出代码页 65001（UTF-8）下做字节级行编辑：中文等多字节字符
//     按字节退格，退格后的剩余字节成乱码，表现为「backspace 删不动字符」；
//  2. 传统 conhost 行编辑根本不支持 Delete 键（VK_DELETE 不进入行缓冲）。
//
// 因此降级模式下主输入/优先级输入改用传统控制台行编辑器（ReadConsoleInputW
// 读按键 + WriteConsoleW/SetConsoleCursorPosition 自绘），由程序自己维护
// rune 缓冲与光标，完全绕过 conhost 的行编辑。
var chatLegacyConsoleInputMode atomic.Bool

// chatDebugMode 控制诊断输出（[aicli-diag] 前缀），仅 --debug 时打印。
var chatDebugMode atomic.Bool

func setChatLegacyConsoleInputMode(v bool) {
	chatLegacyConsoleInputMode.Store(v)
}

func setChatDebugFlag(v bool) {
	chatDebugMode.Store(v)
}

func chatDebugFlagEnabled() bool {
	return chatDebugMode.Load()
}

func chatLegacyConsoleInputEnabled() bool {
	return chatLegacyConsoleInputMode.Load()
}

// readLegacyConsoleLineFn 是传统控制台行编辑器的探测/读取入口，测试可替换。
var readLegacyConsoleLineFn = readLegacyConsoleLine

// readChatSessionLine 读取一行交互输入。
//
// 降级模式优先走传统控制台行编辑器；当 stdin 不是控制台（管道/重定向/测试
// 注入的 reader）或编辑器不可用（GetConsoleMode 失败）时回退到 buffered
// 读取，行为与旧版完全一致。
//
// 中间插入的管道/PTY 分支（readPipeInteractiveLineFn）专门处理「stdin 是
// 管道或字符设备但不是真实控制台」的场景（MobaXterm 本地 shell、cygwin/
// mintty、winpty、SSH 管道等）：此时 legacy 控制台编辑器读不到按键记录，
// 而 buffered 回退会让 backspace/Delete/方向键的字节序列原样进入输入行；
// 改用 ui 包的逐键编辑器（ANSI 重绘 + 完整按键解析）。
func readChatSessionLine(ctx context.Context, reader *bufio.Reader) (string, error) {
	if chatLegacyConsoleInputEnabled() {
		if line, ok, err := readLegacyConsoleLineFn(ctx); ok {
			if err != nil {
				return "", err
			}
			return line, nil
		}
		if line, ok, err := readPipeInteractiveLineFn(ctx); ok {
			if err != nil {
				return "", err
			}
			return line, nil
		}
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
