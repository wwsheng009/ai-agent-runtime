// Telegram 媒体双向（图片/视频/语音/音频/文件）：
//
//   - 入站：解析 message 的 photo/document/voice/audio/video → InboundMessage.Media
//     （file_id/文件名/类型/大小），由 Service 经 MediaFetcher 物化：getFile 换
//     file_path 后从 /file/bot<token>/<file_path> 下载落盘，注入 local_path 摘要；
//   - 出站：OutboundMessage.Media 与文本媒体指令（```telegram-attachments``` 围栏
//     块、`IMAGE:/VIDEO:/FILE:/VOICE:` 指令行，解析器在 core）；本地路径 multipart
//     上传，远端 URL 交给 Telegram 拉取，已有 file_id 直接复用；
//   - 限制：单文件默认 20MB（官方 getFile 上限，WithInboundMediaLimit 可覆盖）；
//     入站临时目录 24h 低频清扫（每 64 次下载触发一次，best-effort）。
package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// telegramAttachmentFence 是出站媒体指令的围栏块名（仅通道专属名，避免与
	// 普通代码块冲突）。
	telegramAttachmentFence = "telegram-attachments"
	// defaultMediaLimitBytes 是单文件下载/上传默认上限（官方 getFile 20MB）。
	defaultMediaLimitBytes = 20 << 20
	// mediaDownloadTimeout 是单次媒体下载超时。
	mediaDownloadTimeout = 60 * time.Second
	// mediaTempDirPrefix 是入站媒体临时目录名前缀。
	mediaTempDirPrefix = "telegram-media-"
	// mediaTempRetention / mediaSweepEvery 是临时目录清理窗口与节流。
	mediaTempRetention = 24 * time.Hour
	mediaSweepEvery    = 64
)

var (
	telegramAttachmentFenceNames = []string{telegramAttachmentFence}
	mediaSweepCounter            atomic.Uint64
)

// extractInboundMedia 抽取入站媒体元数据与文本占位行（无媒体时两者均为空）。
func extractInboundMedia(message *telegramMessage) ([]connectors.Media, []string) {
	if message == nil {
		return nil, nil
	}
	media := make([]connectors.Media, 0, 4)
	placeholders := make([]string, 0, 4)

	if len(message.Photo) > 0 {
		largest := message.Photo[len(message.Photo)-1]
		media = append(media, connectors.Media{
			Kind:        "image",
			FileID:      strings.TrimSpace(largest.FileID),
			ContentType: "image/*",
			Size:        largest.FileSize,
		})
		placeholders = append(placeholders, "[Photo]")
	}
	if doc := message.Document; doc != nil && strings.TrimSpace(doc.FileID) != "" {
		media = append(media, connectors.Media{
			Kind:        "file",
			FileID:      strings.TrimSpace(doc.FileID),
			FileName:    strings.TrimSpace(doc.FileName),
			ContentType: strings.TrimSpace(doc.MimeType),
			Size:        doc.FileSize,
		})
		placeholders = append(placeholders, mediaPlaceholder("Document", strings.TrimSpace(doc.FileName)))
	}
	if voice := message.Voice; voice != nil && strings.TrimSpace(voice.FileID) != "" {
		media = append(media, connectors.Media{
			Kind:        "voice",
			FileID:      strings.TrimSpace(voice.FileID),
			ContentType: strings.TrimSpace(voice.MimeType),
			Size:        voice.FileSize,
		})
		placeholders = append(placeholders, "[Voice]")
	}
	if audio := message.Audio; audio != nil && strings.TrimSpace(audio.FileID) != "" {
		media = append(media, connectors.Media{
			Kind:        "audio",
			FileID:      strings.TrimSpace(audio.FileID),
			FileName:    strings.TrimSpace(audio.FileName),
			ContentType: strings.TrimSpace(audio.MimeType),
			Size:        audio.FileSize,
		})
		placeholders = append(placeholders, mediaPlaceholder("Audio", strings.TrimSpace(audio.FileName)))
	}
	if video := message.Video; video != nil && strings.TrimSpace(video.FileID) != "" {
		media = append(media, connectors.Media{
			Kind:        "video",
			FileID:      strings.TrimSpace(video.FileID),
			FileName:    strings.TrimSpace(video.FileName),
			ContentType: strings.TrimSpace(video.MimeType),
			Size:        video.FileSize,
		})
		placeholders = append(placeholders, "[Video]")
	}
	if len(media) == 0 {
		return nil, nil
	}
	return media, placeholders
}

func mediaPlaceholder(label, name string) string {
	if name == "" {
		return "[" + label + "]"
	}
	return "[" + label + ": " + name + "]"
}

// ── 入站下载（connectors.MediaFetcher） ──────────────────────────────────────

// FetchInboundMedia 实现 connectors.MediaFetcher：getFile 换 file_path 后下载
// 落盘，返回本地绝对路径。
func (p *Provider) FetchInboundMedia(ctx context.Context, cfg connectors.Config, media connectors.Media) (string, error) {
	token := cfg.Secret("bot_token")
	if token == "" {
		return "", errors.New("telegram: bot_token is required")
	}
	fileID := strings.TrimSpace(media.FileID)
	if fileID == "" {
		return "", errors.New("telegram: media file_id is required")
	}

	var fileInfo struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := p.call(ctx, p.baseFor(cfg), token, "getFile", map[string]any{"file_id": fileID}, &fileInfo); err != nil {
		return "", err
	}
	filePath := strings.TrimSpace(fileInfo.FilePath)
	if filePath == "" {
		return "", errors.New("telegram: getFile returned empty file_path")
	}

	limit := p.mediaLimit()
	if fileInfo.FileSize > limit {
		return "", fmt.Errorf("telegram: media too large (%d bytes > %d)", fileInfo.FileSize, limit)
	}

	downloadURL := strings.TrimRight(p.baseFor(cfg), "/") + "/file/bot" + token + "/" + filePath
	requestCtx, cancel := context.WithTimeout(ctx, mediaDownloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("telegram: build media download request failed: %s", redactToken(err.Error(), token))
	}
	response, err := p.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("telegram: media download failed: %s", redactToken(err.Error(), token))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("telegram: media download returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("telegram: read media body failed: %w", err)
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("telegram: media too large (> %d bytes)", limit)
	}

	p.maybeSweepMediaTempDirs()
	nameHint := firstNonEmpty(media.FileName, filepath.Base(filePath), "telegram-media")
	return persistMediaTempFile(data, nameHint)
}

// mediaLimit 返回生效的单文件上限（入站下载/出站读取共用）。
func (p *Provider) mediaLimit() int64 {
	if p.mediaLimitBytes > 0 {
		return p.mediaLimitBytes
	}
	return defaultMediaLimitBytes
}

// ── 出站发送 ────────────────────────────────────────────────────────────────

// sendOutboundMedia 发送一条媒体：本地路径 multipart 上传、远端 URL 交给
// Telegram 拉取、已有 file_id 直接复用。
func (p *Provider) sendOutboundMedia(ctx context.Context, base, token, chat string, media connectors.Media) error {
	method, field, err := telegramMediaMethod(media)
	if err != nil {
		return err
	}

	if fileID := strings.TrimSpace(media.FileID); fileID != "" {
		return p.call(ctx, base, token, method, map[string]any{"chat_id": chat, field: fileID}, nil)
	}
	if mediaURL := strings.TrimSpace(media.URL); mediaURL != "" {
		return p.call(ctx, base, token, method, map[string]any{"chat_id": chat, field: mediaURL}, nil)
	}

	mediaPath := strings.TrimSpace(media.Path)
	if mediaPath == "" {
		return fmt.Errorf("telegram: media requires a local path, remote url or file_id (kind=%s)", media.Kind)
	}
	if strings.HasPrefix(strings.ToLower(mediaPath), "file://") {
		parsed, parseErr := urlParseFileURL(mediaPath)
		if parseErr != nil {
			return parseErr
		}
		mediaPath = parsed
	}
	if !filepath.IsAbs(mediaPath) {
		return fmt.Errorf("telegram: media file path must be absolute: %s", mediaPath)
	}
	info, statErr := os.Stat(mediaPath)
	if statErr != nil {
		return fmt.Errorf("telegram: stat media file %q failed: %w", mediaPath, statErr)
	}
	if info.IsDir() {
		return fmt.Errorf("telegram: media file path must be a file: %s", mediaPath)
	}
	if limit := p.mediaLimit(); info.Size() > limit {
		return fmt.Errorf("telegram: media too large (%d bytes > %d)", info.Size(), limit)
	}
	data, readErr := os.ReadFile(mediaPath)
	if readErr != nil {
		return fmt.Errorf("telegram: read media file %q failed: %w", mediaPath, readErr)
	}
	fileName := firstNonEmpty(media.FileName, filepath.Base(mediaPath))
	return p.uploadMediaMultipart(ctx, base, token, method, chat, field, fileName, data)
}

// telegramMediaMethod 解析发送方法与表单字段名（kind 缺失时按扩展名推断）。
func telegramMediaMethod(media connectors.Media) (string, string, error) {
	kind := strings.ToLower(strings.TrimSpace(media.Kind))
	if kind == "" {
		kind = connectors.InferMediaKindFromLocation(firstNonEmpty(media.FileName, media.Path, media.URL))
	}
	switch kind {
	case "image":
		return "sendPhoto", "photo", nil
	case "video":
		return "sendVideo", "video", nil
	case "audio":
		return "sendAudio", "audio", nil
	case "voice":
		if telegramVoiceCompatible(media) {
			return "sendVoice", "voice", nil
		}
		// Telegram sendVoice 仅接受 OGG/OPUS：其它音频降级为文件。
		return "sendDocument", "document", nil
	case "file":
		return "sendDocument", "document", nil
	default:
		return "", "", fmt.Errorf("telegram: unsupported media kind %q", media.Kind)
	}
}

func telegramVoiceCompatible(media connectors.Media) bool {
	contentType := strings.ToLower(strings.TrimSpace(media.ContentType))
	if strings.Contains(contentType, "ogg") || strings.Contains(contentType, "opus") {
		return true
	}
	lower := strings.ToLower(firstNonEmpty(media.FileName, media.Path, media.URL))
	return strings.HasSuffix(lower, ".ogg") || strings.HasSuffix(lower, ".opus")
}

// uploadMediaMultipart 以 multipart/form-data 上传本地文件。
func (p *Provider) uploadMediaMultipart(ctx context.Context, base, token, method, chat, field, fileName string, data []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", chat); err != nil {
		return fmt.Errorf("telegram: build multipart field failed: %w", err)
	}
	part, err := writer.CreateFormFile(field, filepath.Base(fileName))
	if err != nil {
		return fmt.Errorf("telegram: build multipart file failed: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("telegram: write multipart file failed: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("telegram: close multipart body failed: %w", err)
	}

	endpoint := strings.TrimRight(base, "/") + "/bot" + token + "/" + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return fmt.Errorf("telegram: %s request build failed: %s", method, redactToken(err.Error(), token))
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return p.doAPIRequest(request, token, method, nil)
}

func telegramMediaLabel(media connectors.Media) string {
	label := firstNonEmpty(media.FileName, media.Path, media.URL, media.FileID, media.Kind)
	runes := []rune(label)
	if len(runes) > 60 {
		return string(runes[:60]) + "…"
	}
	return label
}

// ── 临时文件与清扫 ──────────────────────────────────────────────────────────

// persistMediaTempFile 把媒体写入临时目录（0600），返回落盘路径。
func persistMediaTempFile(data []byte, nameHint string) (string, error) {
	dir, err := os.MkdirTemp("", mediaTempDirPrefix+"*")
	if err != nil {
		return "", fmt.Errorf("telegram: create media dir failed: %w", err)
	}
	name := sanitizeMediaFileName(nameHint)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("telegram: write media file failed: %w", err)
	}
	return path, nil
}

// sanitizeMediaFileName 净化落盘文件名（去掉路径分隔与控制字符；空则兜底）。
func sanitizeMediaFileName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "telegram-media.bin"
	}
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return -1
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			return '_'
		default:
			return r
		}
	}, base)
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "telegram-media.bin"
	}
	return cleaned
}

// maybeSweepMediaTempDirs 低频清扫过期入站媒体临时目录（best-effort）。
func (p *Provider) maybeSweepMediaTempDirs() {
	if mediaSweepCounter.Add(1)%mediaSweepEvery != 1 {
		return
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	cutoff := now().Add(-mediaTempRetention)
	tempRoot := os.TempDir()
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), mediaTempDirPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(tempRoot, entry.Name()))
		}
	}
}

// urlParseFileURL 解析 file:// 前缀的本地路径。
func urlParseFileURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("telegram: invalid media file url %q", raw)
	}
	path := filepath.FromSlash(parsed.Path)
	// Windows：file:///C:/x → /C:/x → 去掉多余前导分隔符。
	if len(path) >= 3 && (path[0] == '\\' || path[0] == '/') && path[2] == ':' {
		path = path[1:]
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("telegram: invalid media file url %q", raw)
	}
	return path, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
