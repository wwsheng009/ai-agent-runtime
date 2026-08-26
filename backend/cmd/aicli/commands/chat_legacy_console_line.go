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

func setChatLegacyConsoleInputMode(v bool) {
	chatLegacyConsoleInputMode.Store(v)
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
func readChatSessionLine(ctx context.Context, reader *bufio.Reader) (string, error) {
	if chatLegacyConsoleInputEnabled() {
		if line, ok, err := readLegacyConsoleLineFn(ctx); ok {
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
