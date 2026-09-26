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

// normalizeChatImageTokenPrompt 把用户文本里的 [Image #N] 令牌与待发送附件对齐：
//   - 按令牌在文本里的出现顺序重排附件，并把令牌重编号为 1..k（文本顺序 == 发送顺序）；
//   - 丢弃没有对应附件的悬空令牌，避免模型看到不存在的图片编号；
//   - 非令牌来源的附件（ACP/Web/resume）保持原顺序追加在末尾，且**不**替用户补写令牌；
//   - 文本里一个令牌都没有时原样返回（不猜意图、不重排）。
func normalizeChatImageTokenPrompt(text string, paths []string, marks map[string]int) (string, []string) {
	if len(paths) == 0 {
		return text, nil
	}
	matches := chatImageTokenPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, paths
	}
	byToken := make(map[int]string, len(paths))
	for _, path := range paths {
		if index, ok := marks[path]; ok && index > 0 {
			byToken[index] = path
		}
	}
	order := make([]int, 0, len(matches))
	seen := make(map[int]struct{}, len(matches))
	for _, match := range matches {
		index, ok := chatImageTokenIndexAt(text, match[2], match[3])
		if !ok {
			continue
		}
		if _, dup := seen[index]; dup {
			continue
		}
		seen[index] = struct{}{}
		order = append(order, index)
	}
	survivors := make([]string, 0, len(paths))
	renumber := make(map[int]int, len(order))
	for _, index := range order {
		path, ok := byToken[index]
		if !ok {
			continue
		}
		renumber[index] = len(survivors) + 1
		survivors = append(survivors, path)
	}
	if len(renumber) == 0 {
		// 文本里的令牌都不对应附件：不做改写（保守，避免误删用户文字）。
		return text, paths
	}
	for _, path := range paths {
		if _, tokenized := marks[path]; !tokenized {
			survivors = append(survivors, path)
		}
	}
	var builder strings.Builder
	last := 0
	dropped := false
	for _, match := range matches {
		index, ok := chatImageTokenIndexAt(text, match[2], match[3])
		builder.WriteString(text[last:match[0]])
		newIndex, referenced := 0, false
		if ok {
			newIndex, referenced = renumber[index]
		}
		if referenced {
			builder.WriteString(chatImageToken(newIndex))
		} else {
			// 悬空令牌（没有对应附件）直接丢弃，避免模型看到不存在的图片编号。
			dropped = true
		}
		last = match[1]
	}
	builder.WriteString(text[last:])
	result := builder.String()
	if dropped {
		result = collapseChatImageTokenSpaces(result)
	}
	return result, survivors
}

// normalizeChatTurnImagePrompt 在 turn 入口对会话做一次对齐：重排/重编号附件并
// 同步令牌标记，返回改写后的用户文本。之后记录的用户消息、模型 prompt 与附件列表
// 都基于同一份对齐结果，避免"文本编号与图片顺序不一致"。
func normalizeChatTurnImagePrompt(session *ChatSession, text string) string {
	if session == nil || len(session.ImagePaths) == 0 {
		return text
	}
	normalizedText, survivors := normalizeChatImageTokenPrompt(text, session.ImagePaths, session.imageTokenPaths)
	if len(survivors) == 0 {
		// 理论不可达（对齐函数只在有命中时裁剪）；保守起见不改会话状态。
		return normalizedText
	}
	session.ImagePaths = survivors
	if len(session.imageTokenPaths) > 0 {
		marks := make(map[string]int, len(survivors))
		for position, path := range survivors {
			if _, tokenized := session.imageTokenPaths[path]; tokenized {
				marks[path] = position + 1
			}
		}
		if len(marks) == 0 {
			marks = nil
		}
		session.imageTokenPaths = marks
	}
	return normalizedText
}

// chatImageTokenIndexAt 解析令牌捕获组的字节区间。
func chatImageTokenIndexAt(text string, start, end int) (int, bool) {
	if start < 0 || end > len(text) || start >= end {
		return 0, false
	}
	var index int
	if _, err := fmt.Sscanf(text[start:end], "%d", &index); err != nil || index < 1 {
		return 0, false
	}
	return index, true
}

// collapseChatImageTokenSpaces 在丢弃悬空令牌后收拢遗留的连续空格（只处理空格/制表符，
// 不动换行，避免破坏用户排版）。
func collapseChatImageTokenSpaces(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	pendingSpace := false
	for _, r := range text {
		if r == ' ' || r == '\t' {
			if pendingSpace {
				continue
			}
			pendingSpace = true
			builder.WriteRune(r)
			continue
		}
		pendingSpace = false
		builder.WriteRune(r)
	}
	return builder.String()
}
