// attachment 协议与入站摘要：把「媒体元数据/本地文件」渲染为给 agent 的
// 文本摘要，并解析 agent 回复里的媒体发送指令。
//
// 协议镜像上游 codex-server/backend/internal/bots/wechat_attachment_protocol.go
// 与 message_media.go 的微信部分：
//
//   - 入站：每条消息的媒体渲染为块（[Image attachment] / file_name /
//     content_type / local_path / source_url），并把"如何回发媒体"的通道
//     提示（wechat-attachments 围栏块）追加在文本尾部；
//   - 出站：从回复文本解析 ```wechat-attachments``` 围栏块、标题块、
//     `MEDIA:/IMAGE:/VIDEO:/FILE:/VOICE:` 指令行与裸 `image <路径>` 行；
//     voice 统一降级为 file（bot 侧的语音发送链路未开放）。
package wechat

import (
	"regexp"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

var (
	attachmentBlockPattern        = regexp.MustCompile("(?is)```wechat-attachments\\s*(.*?)```")
	attachmentHeadingBlockPattern = regexp.MustCompile(
		"(?is)(?:^|\\n)\\s*wechat-attachments\\s*:?\\s*\\n```(?:[\\w-]+)?\\s*\\n(.*?)```",
	)
)

// outboundMediaNote 是追加到入站文本的通道提示：告诉 agent 如何把媒体发回
// 微信（与上游文案一致；重复注入会被去重）。
const outboundMediaNote = "[Channel note: this conversation is on WeChat. To send media back to the user, append a final `wechat-attachments` fenced block with lines like `image <absolute-path-or-https-url>`, `video <absolute-path-or-https-url>`, or `file <absolute-path-or-https-url>`. Use absolute local paths only.]"

// ── 入站文本合成 ────────────────────────────────────────────────────────────

// composeInboundText 把正文与媒体摘要块拼成给 agent 的文本，并追加通道提示。
// 正文与媒体均为空时返回空串（调用方按忽略处理）。
func composeInboundText(text string, media []connectors.Media) string {
	parts := make([]string, 0, len(media)+2)
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, item := range media {
		if block := mediaSummaryBlock(item); block != "" {
			parts = append(parts, block)
		}
	}
	body := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if body == "" {
		return ""
	}
	return appendOutboundMediaNote(body)
}

// appendOutboundMediaNote 追加通道提示（已包含时不重复）。
func appendOutboundMediaNote(text string) string {
	text = strings.TrimSpace(text)
	if strings.Contains(text, outboundMediaNote) {
		return text
	}
	if text == "" {
		return outboundMediaNote
	}
	return text + "\n\n" + outboundMediaNote
}

// mediaSummaryBlock 渲染一条媒体的摘要块（与上游字段名一致）。
func mediaSummaryBlock(item connectors.Media) string {
	kind := normalizeMediaKind(item.Kind)
	if kind == "" {
		kind = mediaKindFile
	}
	lines := []string{"[" + humanizeMediaKind(kind) + " attachment]"}
	if name := strings.TrimSpace(item.FileName); name != "" {
		lines = append(lines, "file_name: "+name)
	}
	if contentType := strings.TrimSpace(item.ContentType); contentType != "" {
		lines = append(lines, "content_type: "+contentType)
	}
	if mediaPath := strings.TrimSpace(item.Path); mediaPath != "" {
		lines = append(lines, "local_path: "+mediaPath)
	}
	if mediaURL := strings.TrimSpace(item.URL); mediaURL != "" {
		lines = append(lines, "source_url: "+mediaURL)
	}
	return strings.Join(lines, "\n")
}

func humanizeMediaKind(kind string) string {
	trimmed := strings.TrimSpace(kind)
	if trimmed == "" {
		trimmed = mediaKindFile
	}
	if len(trimmed) == 1 {
		return strings.ToUpper(trimmed)
	}
	return strings.ToUpper(trimmed[:1]) + strings.ToLower(trimmed[1:])
}

// ── 出站：attachment 协议解析 ───────────────────────────────────────────────

// parseAttachmentProtocol 从回复文本解析媒体指令，返回可见文本与媒体列表。
func parseAttachmentProtocol(text string) (string, []connectors.Media) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	attachments := make([]connectors.Media, 0)

	text, blockAttachments := extractAttachmentBlocks(text, attachmentBlockPattern)
	attachments = append(attachments, blockAttachments...)

	text, headingBlockAttachments := extractAttachmentBlocks(text, attachmentHeadingBlockPattern)
	attachments = append(attachments, headingBlockAttachments...)

	text, headingLineAttachments := extractAttachmentHeadingLines(text)
	attachments = append(attachments, headingLineAttachments...)

	lines := strings.Split(text, "\n")
	visibleLines := make([]string, 0, len(lines))
	for _, line := range lines {
		if media, ok := parseAttachmentDirectiveLine(line); ok {
			attachments = append(attachments, media)
			continue
		}
		visibleLines = append(visibleLines, line)
	}

	visibleText := collapseBlankLines(strings.Join(visibleLines, "\n"))
	return visibleText, attachments
}

func extractAttachmentBlocks(text string, pattern *regexp.Regexp) (string, []connectors.Media) {
	if pattern == nil {
		return text, nil
	}
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return text, nil
	}
	attachments := make([]connectors.Media, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		attachments = append(attachments, parseAttachmentLines(match[1])...)
	}
	return pattern.ReplaceAllString(text, ""), attachments
}

// extractAttachmentHeadingLines 处理「wechat-attachments:」标题行后跟若干
// 裸规格行的形态（无围栏）。
func extractAttachmentHeadingLines(text string) (string, []connectors.Media) {
	lines := strings.Split(text, "\n")
	visibleLines := make([]string, 0, len(lines))
	attachments := make([]connectors.Media, 0)

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if !isAttachmentHeadingLine(line) {
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

func parseAttachmentLines(block string) []connectors.Media {
	lines := strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n")
	attachments := make([]connectors.Media, 0, len(lines))
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
func parseAttachmentDirectiveLine(line string) (connectors.Media, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return connectors.Media{}, false
	}
	for _, prefix := range []string{"MEDIA:", "IMAGE:", "VIDEO:", "FILE:", "VOICE:"} {
		if strings.HasPrefix(strings.ToUpper(trimmed), prefix) {
			kind := strings.ToLower(strings.TrimSuffix(prefix, ":"))
			if kind == "media" {
				kind = ""
			}
			return buildAttachmentMedia(kind, strings.TrimSpace(trimmed[len(prefix):]))
		}
	}
	return connectors.Media{}, false
}

func isAttachmentHeadingLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	switch strings.ToLower(trimmed) {
	case "wechat-attachments", "wechat-attachments:":
		return true
	default:
		return false
	}
}

func isAttachmentBareSpecLine(line string) bool {
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) == 0 {
		return false
	}
	return isAttachmentKind(parts[0])
}

// parseAttachmentSpec 解析一行规格："image <location>" 或裸 "<location>"。
func parseAttachmentSpec(line string) (connectors.Media, bool) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return connectors.Media{}, false
	}
	kind := strings.ToLower(strings.TrimSpace(parts[0]))
	if isAttachmentKind(kind) {
		location := strings.TrimSpace(line[len(parts[0]):])
		return buildAttachmentMedia(kind, location)
	}
	return buildAttachmentMedia("", line)
}

func buildAttachmentMedia(kind, location string) (connectors.Media, bool) {
	location = strings.Trim(strings.TrimSpace(location), "\"'")
	if location == "" {
		return connectors.Media{}, false
	}
	kind = normalizeMediaKind(kind)
	if kind == mediaKindVoice {
		// bot 侧语音发送链路未开放：统一按文件发送（上游同策略）。
		kind = mediaKindFile
	}
	if kind == "" {
		kind = inferMediaKindFromLocation(location)
	}
	media := connectors.Media{Kind: kind}
	if isRemoteMediaURL(location) {
		media.URL = location
	} else {
		media.Path = location
		media.FileName = fileNameFromLocation(location)
	}
	return media, true
}

func inferMediaKindFromLocation(location string) string {
	lower := strings.ToLower(strings.TrimSpace(location))
	switch {
	case strings.HasSuffix(lower, ".png"),
		strings.HasSuffix(lower, ".jpg"),
		strings.HasSuffix(lower, ".jpeg"),
		strings.HasSuffix(lower, ".gif"),
		strings.HasSuffix(lower, ".webp"),
		strings.HasSuffix(lower, ".bmp"):
		return mediaKindImage
	case strings.HasSuffix(lower, ".mp4"),
		strings.HasSuffix(lower, ".mov"),
		strings.HasSuffix(lower, ".webm"),
		strings.HasSuffix(lower, ".mkv"),
		strings.HasSuffix(lower, ".avi"):
		return mediaKindVideo
	default:
		return mediaKindFile
	}
}

func isRemoteMediaURL(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

func isAttachmentKind(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "media", mediaKindImage, mediaKindVideo, mediaKindFile, mediaKindVoice:
		return true
	default:
		return false
	}
}

// fileNameFromLocation 从本地路径/URL 推断文件名（去 query 与 fragment）。
func fileNameFromLocation(location string) string {
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
