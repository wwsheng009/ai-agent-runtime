package connectors

// 出站媒体指令协议（跨通道共享；docs/bots/02 §3）。
//
// Agent 在回复文本里用以下任一形态声明要回发的媒体（位置可为本地绝对路径、
// file:// 前缀或 http(s) URL）：
//
//	```qq-attachments
//	image /abs/pic.png
//	video /abs/clip.mp4
//	file  /abs/doc.pdf
//	```
//
//	qq-attachments:
//	image /abs/pic.png
//
//	IMAGE:/abs/pic.png        （指令行：MEDIA:/IMAGE:/VIDEO:/FILE:/VOICE:）
//
// 解析结果为「可见文本 + 媒体列表」：围栏块/标题块/指令行都从可见文本剥离，
// 只把用户该看到的内容发给 IM。voice 是否降级由通道自行决定（微信出站语音
// 降级为文件；QQ 有独立语音类型）。
//
// 通道通过 fenceNames 声明自己接受的围栏块名（如 {"qq-attachments","attachments"}），
// 并在入站文本尾部注入 MediaDirectiveNote(fenceName) 提示 agent 该协议。

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var attachmentDirectivePrefixes = []string{"MEDIA:", "IMAGE:", "VIDEO:", "FILE:", "VOICE:"}

// ParseOutboundMediaDirectives 从回复文本解析出站媒体指令，返回可见文本与
// 媒体列表（fenceNames 为通道接受的围栏块名，大小写不敏感；可含通用
// "attachments"）。无任何指令时原样返回文本与 nil。
func ParseOutboundMediaDirectives(text string, fenceNames ...string) (string, []Media) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return "", nil
	}

	attachments := make([]Media, 0)
	blockPattern, headingBlockPattern := attachmentFencePatterns(fenceNames)

	text, fromBlocks := extractAttachmentBlocks(text, blockPattern)
	attachments = append(attachments, fromBlocks...)

	text, fromHeadingBlocks := extractAttachmentBlocks(text, headingBlockPattern)
	attachments = append(attachments, fromHeadingBlocks...)

	text, fromHeadingLines := extractAttachmentHeadingLines(text, fenceNames)
	attachments = append(attachments, fromHeadingLines...)

	lines := strings.Split(text, "\n")
	visibleLines := make([]string, 0, len(lines))
	for _, line := range lines {
		if media, ok := parseAttachmentDirectiveLine(line); ok {
			attachments = append(attachments, media)
			continue
		}
		visibleLines = append(visibleLines, line)
	}

	return collapseAttachmentBlankLines(strings.Join(visibleLines, "\n")), attachments
}

// MediaDirectiveNote 返回注入入站文本尾部的通道提示（告诉 agent 如何回发媒体）。
func MediaDirectiveNote(fenceName string) string {
	name := strings.TrimSpace(fenceName)
	if name == "" {
		name = "attachments"
	}
	return fmt.Sprintf(
		"[Channel note: to send media back to the user, append a final `%s` fenced block "+
			"with lines like `image <absolute-path-or-https-url>`, `video <absolute-path-or-https-url>`, "+
			"or `file <absolute-path-or-https-url>`. Use absolute local paths only.]", name)
}

// TruncateOpenAttachmentBlocks 截断流式文本中「尚未闭合」的媒体指令块：
//   - 未闭合的 ```<fence> 围栏（含其后全部内容）；
//   - 「<fence>:」标题行及其后的裸规格行块（其后只剩空行/注释/规格行）。
//
// 流式分片按前缀累积推送，闭合前的围栏内容属于出站协议而非用户正文，直接
// 下发会把指令原文泄漏给用户；完整文本仍以 Final 的 ParseOutboundMediaDirectives
// 解析为准（本函数只影响流式中间态）。
func TruncateOpenAttachmentBlocks(text string, fenceNames ...string) string {
	nameSet := attachmentFenceNameSet(fenceNames)
	if len(nameSet) == 0 || strings.TrimSpace(text) == "" {
		return text
	}
	text = truncateOpenAttachmentFence(text, fenceNames)
	return truncateOpenAttachmentHeading(text, nameSet)
}

// truncateOpenAttachmentFence 截断最后一个未闭合的 ```<fence> 开栏（含其后全部内容）。
func truncateOpenAttachmentFence(text string, fenceNames []string) string {
	if text == "" {
		return text
	}
	lower := strings.ToLower(text)
	cut := -1
	for _, name := range fenceNames {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		needle := "```" + name
		for from := 0; from <= len(lower); {
			index := strings.Index(lower[from:], needle)
			if index < 0 {
				break
			}
			open := from + index
			closing := strings.Index(lower[open+len(needle):], "```")
			if closing < 0 {
				if cut < 0 || open < cut {
					cut = open
				}
				break
			}
			from = open + len(needle) + closing + len("```")
		}
	}
	if cut >= 0 {
		text = strings.TrimRight(text[:cut], " \t\r\n")
	}
	return text
}

// truncateOpenAttachmentHeading 截断「<fence>:」标题行及其后未闭合的裸规格行块。
func truncateOpenAttachmentHeading(text string, nameSet map[string]struct{}) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if !isAttachmentHeadingLine(line, nameSet) {
			continue
		}
		// 标题行之后只剩空行/注释/裸规格行直到文本结束 = 块未闭合 → 截断。
		open := true
		for next := index + 1; next < len(lines); next++ {
			trimmed := strings.TrimSpace(lines[next])
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || isAttachmentBareSpecLine(trimmed) {
				continue
			}
			open = false
			break
		}
		if open {
			return strings.TrimRight(strings.Join(lines[:index], "\n"), " \t\r\n")
		}
	}
	return text
}

// InferMediaKindFromLocation 按扩展名推断媒体类别（image|video|file；voice 不推断）。
func InferMediaKindFromLocation(location string) string {
	lower := strings.ToLower(strings.TrimSpace(location))
	switch {
	case strings.HasSuffix(lower, ".png"),
		strings.HasSuffix(lower, ".jpg"),
		strings.HasSuffix(lower, ".jpeg"),
		strings.HasSuffix(lower, ".gif"),
		strings.HasSuffix(lower, ".webp"),
		strings.HasSuffix(lower, ".bmp"):
		return "image"
	case strings.HasSuffix(lower, ".mp4"),
		strings.HasSuffix(lower, ".mov"),
		strings.HasSuffix(lower, ".m4v"),
		strings.HasSuffix(lower, ".webm"),
		strings.HasSuffix(lower, ".mkv"),
		strings.HasSuffix(lower, ".avi"):
		return "video"
	default:
		return "file"
	}
}

// IsRemoteMediaLocation 报告位置是否为 http(s) URL（否则按本地路径处理）。
func IsRemoteMediaLocation(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// MediaFileNameFromLocation 从本地路径/URL 推断文件名（去 query/fragment）。
func MediaFileNameFromLocation(location string) string {
	value := strings.TrimSpace(location)
	value = strings.TrimPrefix(strings.TrimPrefix(value, "file://"), "FILE://")
	if value == "" {
		return ""
	}
	value = strings.Split(value, "?")[0]
	value = strings.Split(value, "#")[0]
	value = strings.ReplaceAll(value, "\\", "/")
	parts := strings.Split(value, "/")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

// ── 内部解析 ────────────────────────────────────────────────────────────────

func attachmentFencePatterns(fenceNames []string) (*regexp.Regexp, *regexp.Regexp) {
	names := make([]string, 0, len(fenceNames))
	seen := make(map[string]struct{}, len(fenceNames))
	for _, name := range fenceNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		names = append(names, regexp.QuoteMeta(name))
	}
	if len(names) == 0 {
		return nil, nil
	}
	alternation := strings.Join(names, "|")
	block := regexp.MustCompile("(?is)```(?:" + alternation + ")\\s*(.*?)```")
	headingBlock := regexp.MustCompile(
		"(?is)(?:^|\\n)\\s*(?:" + alternation + ")\\s*:?\\s*\\n```(?:[\\w-]+)?\\s*\\n(.*?)```")
	return block, headingBlock
}

func attachmentFenceNameSet(fenceNames []string) map[string]struct{} {
	set := make(map[string]struct{}, len(fenceNames))
	for _, name := range fenceNames {
		if trimmed := strings.ToLower(strings.TrimSpace(name)); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	return set
}

func extractAttachmentBlocks(text string, pattern *regexp.Regexp) (string, []Media) {
	if pattern == nil {
		return text, nil
	}
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return text, nil
	}
	attachments := make([]Media, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		attachments = append(attachments, parseAttachmentLines(match[1])...)
	}
	return pattern.ReplaceAllString(text, ""), attachments
}

// extractAttachmentHeadingLines 处理「<fence>:」标题行后跟若干裸规格行的形态
// （无围栏）。
func extractAttachmentHeadingLines(text string, fenceNames []string) (string, []Media) {
	nameSet := attachmentFenceNameSet(fenceNames)
	if len(nameSet) == 0 {
		return text, nil
	}
	lines := strings.Split(text, "\n")
	visibleLines := make([]string, 0, len(lines))
	attachments := make([]Media, 0)

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if !isAttachmentHeadingLine(line, nameSet) {
			visibleLines = append(visibleLines, line)
			continue
		}

		blockLines := make([]string, 0)
		next := index + 1
		for ; next < len(lines); next++ {
			trimmed := strings.TrimSpace(lines[next])
			switch {
			case trimmed == "":
				if len(blockLines) > 0 {
					next++
				}
				goto consumeHeadingBlock
			case strings.HasPrefix(trimmed, "#"):
				blockLines = append(blockLines, lines[next])
			case isAttachmentBareSpecLine(trimmed):
				blockLines = append(blockLines, lines[next])
			default:
				goto consumeHeadingBlock
			}
		}

	consumeHeadingBlock:
		if len(blockLines) == 0 {
			visibleLines = append(visibleLines, line)
			continue
		}
		attachments = append(attachments, parseAttachmentLines(strings.Join(blockLines, "\n"))...)
		index = next - 1
	}

	return strings.Join(visibleLines, "\n"), attachments
}

func parseAttachmentLines(block string) []Media {
	lines := strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n")
	attachments := make([]Media, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if media, ok := parseAttachmentSpec(trimmed); ok {
			attachments = append(attachments, media)
		}
	}
	return attachments
}

// parseAttachmentDirectiveLine 解析显式指令行：MEDIA:/IMAGE:/VIDEO:/FILE:/
// VOICE: 前缀（大小写不敏感）。
func parseAttachmentDirectiveLine(line string) (Media, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return Media{}, false
	}
	upper := strings.ToUpper(trimmed)
	for _, prefix := range attachmentDirectivePrefixes {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}
		kind := strings.ToLower(strings.TrimSuffix(prefix, ":"))
		if kind == "media" {
			kind = ""
		}
		return buildAttachmentMedia(kind, strings.TrimSpace(trimmed[len(prefix):]))
	}
	return Media{}, false
}

// parseAttachmentSpec 解析一行规格："image <location>" 或裸 "<location>"。
func parseAttachmentSpec(line string) (Media, bool) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return Media{}, false
	}
	kind := strings.ToLower(strings.TrimSpace(parts[0]))
	if isAttachmentKind(kind) {
		return buildAttachmentMedia(kind, strings.TrimSpace(line[len(parts[0]):]))
	}
	return buildAttachmentMedia("", line)
}

func isAttachmentHeadingLine(line string, nameSet map[string]struct{}) bool {
	trimmed := strings.ToLower(strings.TrimSpace(line))
	trimmed = strings.TrimSuffix(trimmed, ":")
	if trimmed == "" {
		return false
	}
	_, ok := nameSet[trimmed]
	return ok
}

func isAttachmentBareSpecLine(line string) bool {
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) == 0 {
		return false
	}
	return isAttachmentKind(parts[0])
}

func isAttachmentKind(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "media", "image", "video", "file", "voice", "audio":
		return true
	default:
		return false
	}
}

func normalizeAttachmentKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image":
		return "image"
	case "video":
		return "video"
	case "voice", "audio":
		return "voice"
	case "file":
		return "file"
	default:
		return ""
	}
}

func buildAttachmentMedia(kind string, location string) (Media, bool) {
	location = strings.Trim(strings.TrimSpace(location), "\"'")
	if location == "" {
		return Media{}, false
	}
	kind = normalizeAttachmentKind(kind)
	if kind == "" {
		kind = InferMediaKindFromLocation(location)
	}
	media := Media{Kind: kind}
	if IsRemoteMediaLocation(location) {
		media.URL = location
	} else {
		media.Path = location
		media.FileName = MediaFileNameFromLocation(location)
		if media.FileName == "" {
			media.FileName = filepath.Base(location)
		}
	}
	return media, true
}

func collapseAttachmentBlankLines(value string) string {
	lines := strings.Split(value, "\n")
	collapsed := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if blank || len(collapsed) == 0 {
				continue
			}
			blank = true
			collapsed = append(collapsed, "")
			continue
		}
		blank = false
		collapsed = append(collapsed, strings.TrimRight(line, " \t"))
	}
	for len(collapsed) > 0 && strings.TrimSpace(collapsed[len(collapsed)-1]) == "" {
		collapsed = collapsed[:len(collapsed)-1]
	}
	return strings.TrimSpace(strings.Join(collapsed, "\n"))
}
