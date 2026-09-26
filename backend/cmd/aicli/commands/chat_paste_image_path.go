package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// chatImagePathExtensions 是"看起来像图片"的候选扩展名集合，只用来判断**是否值得**按
// 图片路径处理；真正的存在性/格式判定仍由 llm.ValidateLocalInputImagePaths 与 imageprep
// 负责。只收 http.DetectContentType 认得的位图格式，避免给 heic/avif 这类认不出的扩展名
// 刷出误导性提示。
var chatImagePathExtensions = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".jpe": {}, ".jfif": {},
	".gif": {}, ".webp": {}, ".bmp": {}, ".dib": {}, ".tif": {}, ".tiff": {},
}

// chatPastedImagePath 从一段粘贴文本里识别"整段就是一个图片文件路径"的候选。
//
// 规则刻意保守——宁可漏判，也不改写正常粘贴的文本：
//   - 必须单行（多行粘贴按文本处理）；
//   - 允许整体被成对引号包住（Windows Terminal 与拖拽对含空格的路径会加引号）；
//   - 没加引号时不允许含空白：含空白更可能是句子，不猜；
//   - 去掉外层引号后不能再出现引号（`"a.png" "b.png"` 这种多文件粘贴本期不处理）；
//   - 扩展名必须落在候选集合里。
func chatPastedImagePath(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n") {
		return "", false
	}
	path := trimmed
	quoted := false
	if len(path) >= 2 && (path[0] == '"' || path[0] == '\'') && path[len(path)-1] == path[0] {
		path = strings.TrimSpace(path[1 : len(path)-1])
		quoted = true
	}
	if path == "" || strings.ContainsAny(path, "\"'") {
		return "", false
	}
	if !quoted && strings.ContainsAny(path, " \t") {
		return "", false
	}
	if _, ok := chatImagePathExtensions[strings.ToLower(filepath.Ext(path))]; !ok {
		return "", false
	}
	return path, true
}

// chatImageAttachmentIndex 返回路径在附件列表里的 1-based 序号；不在列表里返回 0。
func chatImageAttachmentIndex(session *ChatSession, path string) int {
	if session == nil {
		return 0
	}
	target := strings.TrimSpace(path)
	for index, existing := range session.ImagePaths {
		if strings.EqualFold(strings.TrimSpace(existing), target) {
			return index + 1
		}
	}
	return 0
}

// chatImageTokenReplacement 为"已经在附件列表里"的图片生成编辑器结果：标记该附件由令牌
// 引入（删令牌即弃图），并把 [Image #N] 插到光标处。路径不在列表里时返回零值。
func chatImageTokenReplacement(session *ChatSession, path string, snapshot ui.LineEditorSnapshot) ui.LineEditorActionResult {
	index := chatImageAttachmentIndex(session, path)
	if index == 0 {
		return ui.LineEditorActionResult{}
	}
	markChatImageTokenPath(session, path, index)
	nextText, nextCursor := insertChatImageToken(snapshot.Text, snapshot.Cursor, index)
	return ui.LineEditorActionResult{
		Claimed:     true,
		Replacement: &ui.LineEditorReplacement{Text: nextText, Cursor: nextCursor},
	}
}

// onPasteText 是粘贴文本的宿主入口（编辑器唯一粘贴入口）：当整段粘贴内容就是一个图片文件
// 路径时，改走既有附件管线（校验 → 压缩/上限 → 去重 → 落附件）并把 [Image #N] 令牌替换进
// 输入行，而不是把路径当文本贴进去。
//
// 这正是 Windows Terminal 场景需要的：WT 自己处理 Ctrl+V/右键，把复制的图片**文件**转成
// 路径文本注入进来，我们的按键与剪贴板钩子都看不到，只能在这里识别。
//
// 所有失败路径都**不改写用户文本**（回落到原样粘贴），只在"确实像图片路径但没能加成附件"
// 时给一行状态提示；非图片路径完全静默，不打扰正常粘贴。
func (c *chatComposerController) onPasteText(text string, snapshot ui.LineEditorSnapshot) ui.LineEditorActionResult {
	if c == nil || c.session == nil {
		return ui.LineEditorActionResult{}
	}
	path, ok := chatPastedImagePath(text)
	if !ok {
		return ui.LineEditorActionResult{}
	}
	if warnings := llm.ValidateLocalInputImagePaths([]string{path}); len(warnings) > 0 {
		c.setStatusLine(fmt.Sprintf("粘贴的图片路径未加成附件（%s）；已按文本粘贴", warnings[0]))
		return ui.LineEditorActionResult{}
	}
	prepared, err := prepareChatImageAttachment(c.session, path)
	if err != nil {
		c.setStatusLine(fmt.Sprintf("粘贴的图片路径预处理失败（%v）；已按文本粘贴", err))
		return ui.LineEditorActionResult{}
	}
	if prepared.Path == "" {
		// 体积超限等"按规则跳过"：只提示，不加附件（绝不静默发送原图）。
		c.setStatusLine(prepared.Note + "；已按文本粘贴")
		return ui.LineEditorActionResult{}
	}
	if existing := chatImageAttachmentIndex(c.session, prepared.Path); existing > 0 {
		c.setStatusLine(fmt.Sprintf("该图片已在附件中：[Image #%d]", existing))
		return chatImageTokenReplacement(c.session, prepared.Path, snapshot)
	}
	c.session.ImagePaths = append(c.session.ImagePaths, prepared.Path)
	result := chatImageTokenReplacement(c.session, prepared.Path, snapshot)
	if result.Replacement == nil {
		// 理论不可达（刚刚 append 过）：不吞掉用户的粘贴内容，回落到文本。
		c.setStatusLine("已加入图片附件，但未能插入令牌；请用 /attach 重试或直接发送")
		return ui.LineEditorActionResult{}
	}
	note := prepared.Note
	message := fmt.Sprintf("已从粘贴的路径加入图片附件 [Image #%d]", chatImageAttachmentIndex(c.session, prepared.Path))
	if note != "" {
		message += "；" + note
	}
	c.setStatusLine(message)
	return result
}
