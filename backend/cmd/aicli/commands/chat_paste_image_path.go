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

// maxChatPastedImagePaths 限制一次粘贴最多转换几张图片：每张都要走压缩与落盘，不设上限时
// "把一大堆路径贴进来"会长时间卡住输入，也可能瞬间撑爆请求体。
const maxChatPastedImagePaths = 8

// splitPastedPathTokens 把粘一段粘贴文本切成"路径候选"标记：空白（含换行）分隔，成对引号
// 内的内容作为整体保留（Windows Terminal 与拖拽对含空格的路径会加引号）。
//
// 只有"整段文本恰好由这些标记与分隔空白组成"时才返回 ok=true——引号未闭合、引号后面紧跟
// 非空白字符、标记里混入引号，都会返回 false，交给调用方回落到普通文本粘贴。
func splitPastedPathTokens(text string) ([]string, bool) {
	var tokens []string
	runes := []rune(text)
	for i := 0; i < len(runes); {
		if isChatImageTokenSpace(runes[i]) {
			i++
			continue
		}
		if runes[i] == '"' || runes[i] == '\'' {
			quote := runes[i]
			i++
			start := i
			for i < len(runes) && runes[i] != quote {
				i++
			}
			if i >= len(runes) {
				return nil, false // 引号未闭合
			}
			token := string(runes[start:i])
			i++ // 跳过收尾引号
			if i < len(runes) && !isChatImageTokenSpace(runes[i]) {
				return nil, false // 引号后紧跟内容（如 "a.png"x）
			}
			tokens = append(tokens, token)
			continue
		}
		start := i
		for i < len(runes) && !isChatImageTokenSpace(runes[i]) {
			if runes[i] == '"' || runes[i] == '\'' {
				return nil, false // 未加引号的标记里不该出现引号
			}
			i++
		}
		tokens = append(tokens, string(runes[start:i]))
	}
	if len(tokens) == 0 {
		return nil, false
	}
	return tokens, true
}

// isChatImagePathCandidate 判断一个标记是否可能是一个图片文件路径（只看扩展名；存在性、
// 可读性与真实格式由后续校验负责）。
func isChatImagePathCandidate(token string) bool {
	if token == "" {
		return false
	}
	_, ok := chatImagePathExtensions[strings.ToLower(filepath.Ext(token))]
	return ok
}

// chatPastedImagePaths 从一段粘贴文本里识别"整段就是一个或多个图片文件路径"。
//
// 规则刻意保守——宁可漏判，也不改写正常粘贴的文本：
//   - 整段文本必须由路径标记与空白组成，出现任何非路径碎片（例如"请看"）就整体不命中；
//   - 未加引号的标记不含空白（含空白更可能是句子，不猜）；
//   - 每个标记的扩展名都要落在候选集合里；
//   - 完全相同（忽略大小写）的路径去重，避免同一次粘贴重复压缩同一张图。
func chatPastedImagePaths(text string) ([]string, bool) {
	tokens, ok := splitPastedPathTokens(text)
	if !ok {
		return nil, false
	}
	seen := make(map[string]struct{}, len(tokens))
	paths := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !isChatImagePathCandidate(token) {
			return nil, false
		}
		key := strings.ToLower(token)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		paths = append(paths, token)
	}
	if len(paths) == 0 {
		return nil, false
	}
	return paths, true
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

// chatImageTokenReplacement 为单个附件生成编辑器结果（剪贴板路径与单文件粘贴共用）。
func chatImageTokenReplacement(session *ChatSession, path string, snapshot ui.LineEditorSnapshot) ui.LineEditorActionResult {
	return chatImageTokensReplacement(session, []string{path}, snapshot)
}

// chatImageTokensReplacement 为一批"已经在附件列表里"的图片生成编辑器结果：逐个标记该附件
// 由令牌引入（删令牌即弃图），并按给定顺序把 [Image #N] 依次插到光标处。一个都插不进去时
// 返回零值，交由调用方回落到文本粘贴。
func chatImageTokensReplacement(session *ChatSession, paths []string, snapshot ui.LineEditorSnapshot) ui.LineEditorActionResult {
	text, cursor := snapshot.Text, snapshot.Cursor
	inserted := 0
	for _, path := range paths {
		index := chatImageAttachmentIndex(session, path)
		if index == 0 {
			continue
		}
		markChatImageTokenPath(session, path, index)
		text, cursor = insertChatImageToken(text, cursor, index)
		inserted++
	}
	if inserted == 0 {
		return ui.LineEditorActionResult{}
	}
	return ui.LineEditorActionResult{
		Claimed:     true,
		Replacement: &ui.LineEditorReplacement{Text: text, Cursor: cursor},
	}
}

// onPasteText 是粘贴文本的宿主入口（编辑器唯一粘贴入口）：当整段粘贴内容就是一个或多个图片
// 文件路径时，改走既有附件管线（校验 → 压缩/上限 → 去重 → 落附件）并把 [Image #N] 令牌
// 替换进输入行，而不是把路径当文本贴进去。
//
// 这正是 Windows Terminal 场景需要的：WT 自己处理 Ctrl+V/右键，把复制的图片**文件**转成
// 路径文本注入进来，我们的按键与剪贴板钩子都看不到，只能在这里识别。
//
// 一致性规则（刻意保守）：
//   - 校验阶段**全有或全无**：任一标记不是可用的图片文件，就整体回落为文本粘贴，避免出现
//     "一半转了附件、一半还是路径"的半吊子状态；
//   - 压缩/上限这类"按规则跳过"不阻塞其它图片：能加的照加，跳过的在状态行如实写明原因；
//   - 所有失败路径都**不改写用户文本**，非图片内容完全静默，不打扰正常粘贴。
func (c *chatComposerController) onPasteText(text string, snapshot ui.LineEditorSnapshot) ui.LineEditorActionResult {
	if c == nil || c.session == nil {
		return ui.LineEditorActionResult{}
	}
	paths, ok := chatPastedImagePaths(text)
	if !ok {
		return ui.LineEditorActionResult{}
	}
	if len(paths) > maxChatPastedImagePaths {
		c.setStatusLine(fmt.Sprintf(
			"一次粘贴的图片路径过多（%d 个，上限 %d 个）；已按文本粘贴，可分批粘贴或用 /attach 逐个添加",
			len(paths), maxChatPastedImagePaths,
		))
		return ui.LineEditorActionResult{}
	}
	for _, path := range paths {
		if warnings := llm.ValidateLocalInputImagePaths([]string{path}); len(warnings) > 0 {
			c.setStatusLine(fmt.Sprintf("粘贴的图片路径未加成附件（%s）；已按文本粘贴", warnings[0]))
			return ui.LineEditorActionResult{}
		}
	}

	attached := make([]string, 0, len(paths))
	reused := 0
	var notes []string
	var skipped []string
	for _, path := range paths {
		prepared, err := prepareChatImageAttachment(c.session, path)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s（%v）", path, err))
			continue
		}
		if prepared.Path == "" {
			// 体积超限等"按规则跳过"：只提示，不加附件（绝不静默发送原图）。
			skipped = append(skipped, fmt.Sprintf("%s（%s）", path, prepared.Note))
			continue
		}
		if chatImageAttachmentIndex(c.session, prepared.Path) == 0 {
			c.session.ImagePaths = append(c.session.ImagePaths, prepared.Path)
		} else {
			reused++
		}
		if prepared.Note != "" {
			notes = append(notes, prepared.Note)
		}
		attached = append(attached, prepared.Path)
	}

	result := chatImageTokensReplacement(c.session, attached, snapshot)
	if result.Replacement == nil {
		reasons := append(append([]string(nil), skipped...), notes...)
		detail := "没有可用的图片"
		if len(reasons) > 0 {
			detail = strings.Join(reasons, "；")
		}
		c.setStatusLine(fmt.Sprintf("粘贴的图片路径未加成附件（%s）；已按文本粘贴", detail))
		return ui.LineEditorActionResult{}
	}

	indices := make([]string, 0, len(attached))
	for _, path := range attached {
		indices = append(indices, fmt.Sprintf("[Image #%d]", chatImageAttachmentIndex(c.session, path)))
	}
	var message string
	if len(attached) == 1 && reused == 0 {
		message = fmt.Sprintf("已从粘贴的路径加入图片附件 %s", indices[0])
	} else {
		message = fmt.Sprintf("已从粘贴的路径加入 %d 个图片附件：%s", len(attached), strings.Join(indices, " "))
	}
	if reused > 0 && len(attached) > reused {
		message += fmt.Sprintf("（其中 %d 个已在附件中）", reused)
	}
	if len(notes) > 0 {
		message += "；" + strings.Join(notes, "；")
	}
	if len(skipped) > 0 {
		message += fmt.Sprintf("；跳过 %d 个：%s", len(skipped), strings.Join(skipped, "；"))
	}
	c.setStatusLine(message)
	return result
}
