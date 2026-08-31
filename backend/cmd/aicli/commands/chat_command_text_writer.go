package commands

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// CommandTextWriter 是 structured command 协议输出的显式 writer 边界
// （设计计划 Phase 3 交付物：把 JSON/plain 协议分离从分支约定升级为类型
// 契约）。构造即声明路由意图，禁止渲染代码隐式解析 os.Stdout：
//
//   - Plain/JSON/noninteractive 兼容投影：NewCommandTextWriter(os.Stdout, mode)
//   - 嵌入方/测试注入：NewCommandTextWriter(customWriter, mode)
//
// interactive owned 输出不走此类型——它经 RenderCommandDocument 提交到
// interaction coordinator（最终进入 render output gateway），与本 writer
// 是互斥路由。ACP/stdio 协议由 internal/acp 独立管理，不在此边界内。
type CommandTextWriter struct {
	writer io.Writer
	mode   CommandOutputMode
}

// CommandOutputMode 声明协议输出形态。
type CommandOutputMode int

const (
	// CommandOutputPlain 输出人类可读文本（RenderDocumentPlain 等）。
	CommandOutputPlain CommandOutputMode = iota
	// CommandOutputJSON 输出结构化 JSON（marshalIndentedJSON 等）。
	CommandOutputJSON
)

// NewCommandTextWriter 构造协议 writer。writer 为 nil 时 fail-fast——
// 协议输出不允许隐式回落 os.Stdout，调用方必须显式声明目标。
func NewCommandTextWriter(writer io.Writer, mode CommandOutputMode) (*CommandTextWriter, error) {
	if writer == nil {
		return nil, fmt.Errorf("command text writer: nil writer (declare the protocol target explicitly)")
	}
	return &CommandTextWriter{writer: writer, mode: mode}, nil
}

// NewStdoutCommandTextWriter 构造 process stdout 协议 writer。这是
// Plain/JSON/noninteractive 兼容投影的显式 allowlist 入口——os.Stdout
// 只允许经此函数进入协议输出路径，渲染代码不得直接解析。
func NewStdoutCommandTextWriter(mode CommandOutputMode) *CommandTextWriter {
	return &CommandTextWriter{writer: os.Stdout, mode: mode}
}

// Writer 返回底层协议 writer。
func (w *CommandTextWriter) Writer() io.Writer {
	if w == nil {
		return nil
	}
	return w.writer
}

// Mode 返回协议输出形态。
func (w *CommandTextWriter) Mode() CommandOutputMode {
	if w == nil {
		return CommandOutputPlain
	}
	return w.mode
}

// WriteText 写入一段已按协议形态渲染的文本；自动补齐末尾换行。
func (w *CommandTextWriter) WriteText(text string) error {
	if w == nil || w.writer == nil {
		return fmt.Errorf("command text writer: nil receiver or writer")
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, err := io.WriteString(w.writer, text)
	return err
}

// WriteJSON 以缩进 JSON 写入 value。
func (w *CommandTextWriter) WriteJSON(value interface{}) error {
	if w == nil || w.writer == nil {
		return fmt.Errorf("command text writer: nil receiver or writer")
	}
	if w.mode != CommandOutputJSON {
		return fmt.Errorf("command text writer: JSON write on plain-mode writer")
	}
	data := marshalIndentedJSON(value)
	if !strings.HasSuffix(data, "\n") {
		data += "\n"
	}
	_, err := io.WriteString(w.writer, data)
	return err
}
