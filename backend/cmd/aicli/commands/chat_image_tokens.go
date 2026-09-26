package commands

import (
	"fmt"
	"regexp"
	"strings"
)

// chatImageTokenPattern 匹配输入框里的图片令牌，例如 [Image #2]。
var chatImageTokenPattern = regexp.MustCompile(`\[Image #([0-9]+)\]`)

// chatImageToken 渲染令牌文本。
func chatImageToken(index int) string {
	return fmt.Sprintf("[Image #%d]", index)
}

// parseChatImageTokenIndexes 返回文本中出现的令牌序号：按出现顺序去重，
// 忽略 0 与非法值（0 不是合法序号，形如 [Image #0] 会被忽略）。
func parseChatImageTokenIndexes(text string) []int {
	matches := chatImageTokenPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(matches))
	seen := make(map[int]struct{}, len(matches))
	for _, match := range matches {
		var index int
		if _, err := fmt.Sscanf(match[1], "%d", &index); err != nil || index < 1 {
			continue
		}
		if _, ok := seen[index]; ok {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
	}
	return indexes
}

// insertChatImageToken 在光标处插入令牌，必要时补一个前导空格，并在令牌后留一个
// 空格便于继续输入；返回新文本与新光标位置。已存在同序号令牌时不重复插入。
func insertChatImageToken(text string, cursor, index int) (string, int) {
	if index < 1 {
		return text, cursor
	}
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	for _, existing := range parseChatImageTokenIndexes(text) {
		if existing == index {
			return text, cursor
		}
	}
	token := chatImageToken(index)
	prefix := ""
	if cursor > 0 && !isChatImageTokenSpace(runes[cursor-1]) {
		prefix = " "
	}
	suffix := " "
	inserted := []rune(prefix + token + suffix)
	result := make([]rune, 0, len(runes)+len(inserted))
	result = append(result, runes[:cursor]...)
	result = append(result, inserted...)
	result = append(result, runes[cursor:]...)
	return string(result), cursor + len(inserted)
}

func isChatImageTokenSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// markChatImageTokenPath 记录某个附件由令牌引入（值为令牌序号）。
func markChatImageTokenPath(session *ChatSession, path string, index int) {
	if session == nil {
		return
	}
	path = strings.TrimSpace(path)
	if path == "" || index < 1 {
		return
	}
	if session.imageTokenPaths == nil {
		session.imageTokenPaths = make(map[string]int, 1)
	}
	session.imageTokenPaths[path] = index
}

// clearChatImageTokenMark 清掉某个附件的令牌标记（移除附件时同步）。
func clearChatImageTokenMark(session *ChatSession, path string) {
	if session == nil || session.imageTokenPaths == nil {
		return
	}
	delete(session.imageTokenPaths, strings.TrimSpace(path))
}

// clearChatImageTokenMarks 清空全部令牌标记（附件整体清空时同步）。
func clearChatImageTokenMarks(session *ChatSession) {
	if session == nil {
		return
	}
	session.imageTokenPaths = nil
}

// appendChatImageTokenForNewAttachments 在命令执行新增附件后，把对应令牌写回下一次
// 输入框草稿并打上标记。键入的 /attach <path> 在编辑器之外执行，只能靠草稿状态回写，
// 这样用户同样可以"删令牌即弃图"。返回是否写入了令牌。
func appendChatImageTokenForNewAttachments(session *ChatSession, before int) bool {
	if session == nil || session.Interaction == nil || len(session.ImagePaths) <= before {
		return false
	}
	text := session.Interaction.PromptInputSnapshot().Text
	cursor := len([]rune(text))
	for index := before + 1; index <= len(session.ImagePaths); index++ {
		markChatImageTokenPath(session, session.ImagePaths[index-1], index)
		text, cursor = insertChatImageToken(text, cursor, index)
	}
	session.Interaction.SetPromptInput(text)
	return true
}

// filterChatImagePathsByDraft 按草稿里存活的令牌裁剪附件（删令牌即弃图）：
//   - 只约束"由令牌引入"的附件；其它来源的附件一律保留，绝不误删；
//   - 草稿里一个令牌都没有时不做裁剪（保守：不静默丢弃用户已加的附件）；
//   - 同时清理已失效的令牌标记，避免标记表无限增长。
func filterChatImagePathsByDraft(session *ChatSession, text string) []string {
	if session == nil || len(session.ImagePaths) == 0 {
		return nil
	}
	indexes := parseChatImageTokenIndexes(text)
	if len(indexes) == 0 {
		return session.ImagePaths
	}
	alive := make(map[int]struct{}, len(indexes))
	for _, index := range indexes {
		alive[index] = struct{}{}
	}
	survivors := make([]string, 0, len(session.ImagePaths))
	prunedMarks := make(map[string]int, len(session.ImagePaths))
	for _, path := range session.ImagePaths {
		index, tokenized := session.imageTokenPaths[path]
		if !tokenized {
			survivors = append(survivors, path)
			continue
		}
		if _, ok := alive[index]; !ok {
			continue
		}
		prunedMarks[path] = index
		survivors = append(survivors, path)
	}
	if len(prunedMarks) == 0 {
		session.imageTokenPaths = nil
	} else {
		session.imageTokenPaths = prunedMarks
	}
	return survivors
}
