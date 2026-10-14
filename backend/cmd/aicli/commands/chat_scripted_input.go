package commands

import (
	"fmt"
	"os"
	"strings"
)

// chatScriptedInput 是 --script 提供的按行输入源，用于 ConPTY 多轮 E2E 夹具：
// 单进程内串行提交多条输入，每条等待上一轮结束后再提交，读完走正常退出路径
// （终端还原、会话落盘）。替代 stdin/TUI 读入，不改变交互语义（会话仍为
// interactive，goal 自动续跑等路径照常生效）。
//
// 只在交互主循环（单 goroutine）访问，无需锁。
type chatScriptedInput struct {
	lines  []string
	cursor int
}

func newChatScriptedInput(lines []string) *chatScriptedInput {
	if len(lines) == 0 {
		return nil
	}
	return &chatScriptedInput{lines: append([]string(nil), lines...)}
}

func (s *chatScriptedInput) exhausted() bool {
	return s == nil || s.cursor >= len(s.lines)
}

// next 返回下一行并前进游标；调用方必须先确认 !exhausted()。
func (s *chatScriptedInput) next() string {
	if s == nil || s.cursor >= len(s.lines) {
		return ""
	}
	line := s.lines[s.cursor]
	s.cursor++
	return line
}

// loadChatScriptLines 读取 --script 文件并拆成逐行输入。空行跳过；
// 与 --no-interactive 互斥（单轮模式不会消费多行脚本）。
func loadChatScriptLines(path string, noInteractive bool) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if noInteractive {
		return nil, fmt.Errorf("--script 与 --no-interactive 不能同时使用：脚本按行驱动交互主循环")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 --script 文件失败: %w", err)
	}
	lines := splitChatScriptLines(string(raw))
	if len(lines) == 0 {
		return nil, fmt.Errorf("--script 文件没有可提交的输入行: %s", path)
	}
	return lines, nil
}

// splitChatScriptLines 规范化脚本文本：去除 BOM、统一 CRLF/CR、跳过空行
// （仅含空白的行不提交）。
func splitChatScriptLines(raw string) []string {
	raw = strings.TrimPrefix(raw, "\ufeff")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}
