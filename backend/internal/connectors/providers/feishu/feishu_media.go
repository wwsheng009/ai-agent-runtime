// 飞书媒体双向（图片/语音/视频/文件）：
//
//   - 入站：非文本消息在占位文本之外携带媒体元数据，FileID 为
//     「messageID|fileKey|type」复合引用；MediaFetcher 经
//     `GET /open-apis/im/v1/messages/{message_id}/resources/{file_key}?type=...`
//     下载落盘，由 Service 注入 local_path 摘要；
//   - 出站：OutboundMessage.Media 与文本媒体指令（```feishu-attachments``` 围栏块、
//     `IMAGE:/VIDEO:/FILE:/VOICE:` 指令行，解析器在 core）。图片走
//     `/open-apis/im/v1/images`（image_type=message），其它走 `/open-apis/im/v1/files`
//     （file_type=opus/mp4/stream）取得 key 后发送媒体消息（reply 原消息优先，
//     失败降级 chat 直发，与文本路径同口径）。
//     **视频统一按 file 消息投递**：飞书 media 类型需要封面 image_key，本切片不做
//     封面，按文件发送仍可下载/播放。
//   - 限制：单文件默认 30MB（飞书上传上限），WithInboundMediaLimit 可覆盖；
//     入站临时目录 24h 低频清扫（每 64 次下载触发一次，best-effort）。
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
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
	// feishuAttachmentFence 是出站媒体指令的围栏块名（仅通道专属名）。
	feishuAttachmentFence = "feishu-attachments"
	// defaultMediaLimitBytes 是单文件默认上限（飞书上传 30MB）。
	defaultMediaLimitBytes = 30 << 20
	// mediaDownloadTimeout 是单次资源下载超时。
	mediaDownloadTimeout = 60 * time.Second
	// mediaTempDirPrefix 是入站媒体临时目录名前缀。
	mediaTempDirPrefix = "feishu-media-"
	// mediaTempRetention / mediaSweepEvery 是临时目录清理窗口与节流。
	mediaTempRetention = 24 * time.Hour
	mediaSweepEvery    = 64

	imageUploadPath = "/open-apis/im/v1/images"
	fileUploadPath  = "/open-apis/im/v1/files"
	resourcePathFmt = "/open-apis/im/v1/messages/%s/resources/%s"
)

var (
	feishuAttachmentFenceNames = []string{feishuAttachmentFence}
	mediaSweepCounter          atomic.Uint64
)

// mediaUploadResponse 是图片/文件上传接口的响应体。
type mediaUploadResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ImageKey string `json:"image_key"`
		FileKey  string `json:"file_key"`
	} `json:"data"`
}

// ── 入站下载（connectors.MediaFetcher） ──────────────────────────────────────

// parseInboundMediaRef 解析「messageID|fileKey|type」复合引用。
func parseInboundMediaRef(fileID string) (messageID, fileKey, resourceType string, err error) {
	parts := strings.Split(strings.TrimSpace(fileID), "|")
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("feishu: invalid media reference %q（需要 messageID|fileKey|type）", fileID)
	}
	messageID = strings.TrimSpace(parts[0])
	fileKey = strings.TrimSpace(parts[1])
	resourceType = "file"
	if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
		resourceType = strings.TrimSpace(parts[2])
	}
	if messageID == "" || fileKey == "" {
		return "", "", "", fmt.Errorf("feishu: incomplete media reference %q", fileID)
	}
	return messageID, fileKey, resourceType, nil
}

// FetchInboundMedia 实现 connectors.MediaFetcher：下载消息资源并落盘。
func (p *Provider) FetchInboundMedia(ctx context.Context, cfg connectors.Config, media connectors.Media) (string, error) {
	messageID, fileKey, resourceType, err := parseInboundMediaRef(media.FileID)
	if err != nil {
		return "", err
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	token, err := p.tenantToken(ctx, cfg)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf(resourcePathFmt, url.PathEscape(messageID), url.PathEscape(fileKey)) +
		"?type=" + url.QueryEscape(resourceType)
	data, err := p.downloadResource(ctx, base, path, token, cfg.Secret(secretAppSecret), token)
	if err != nil {
		return "", err
	}
	p.maybeSweepMediaTempDirs()
	nameHint := firstNonEmpty(media.FileName, fileKey, "feishu-media")
	return persistMediaTempFile(data, nameHint)
}

// downloadResource 下载消息资源（二进制响应，非 JSON 信封）。
func (p *Provider) downloadResource(ctx context.Context, base, path, bearer string, secrets ...string) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, mediaDownloadTimeout)
	defer cancel()
	endpoint, err := buildURL(base, path)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build feishu %s request: %s", path, redact(err.Error(), append(secrets, bearer)...))
	}
	if strings.TrimSpace(bearer) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, &requestError{operation: path, cause: errors.New(redact(err.Error(), append(secrets, bearer)...))}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, &requestError{
			operation:  path,
			statusCode: response.StatusCode,
			status:     response.Status,
			apiMessage: redact(string(raw), append(secrets, bearer)...),
		}
	}
	limit := p.mediaLimit()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read feishu %s body: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("feishu: media too large (> %d bytes)", limit)
	}
	return data, nil
}

// mediaLimit 返回生效的单文件上限（下载/上传共用）。
func (p *Provider) mediaLimit() int64 {
	if p.mediaLimitBytes > 0 {
		return p.mediaLimitBytes
	}
	return defaultMediaLimitBytes
}

// ── 出站发送 ────────────────────────────────────────────────────────────────

// sendOutboundMedia 发送一条媒体：解析来源 → 上传取 key → 发媒体消息。
func (p *Provider) sendOutboundMedia(ctx context.Context, cfg connectors.Config, base, token, chatID, replyTo string, media connectors.Media) error {
	data, fileName, err := p.resolveOutboundMediaBytes(ctx, media)
	if err != nil {
		return err
	}
	if limit := p.mediaLimit(); int64(len(data)) > limit {
		return fmt.Errorf("feishu: media too large (%d bytes > %d)", len(data), limit)
	}

	kind := strings.ToLower(strings.TrimSpace(media.Kind))
	if kind == "" {
		kind = connectors.InferMediaKindFromLocation(firstNonEmpty(media.FileName, media.Path, media.URL))
	}

	var msgType string
	var content map[string]string
	switch kind {
	case "image":
		imageKey, err := p.uploadImage(ctx, cfg, base, token, fileName, data)
		if err != nil {
			return err
		}
		msgType = "image"
		content = map[string]string{"image_key": imageKey}
	case "voice", "audio":
		fileKey, err := p.uploadFile(ctx, cfg, base, token, "opus", fileName, data)
		if err != nil {
			return err
		}
		msgType = "audio"
		content = map[string]string{"file_key": fileKey}
	case "video":
		fileKey, err := p.uploadFile(ctx, cfg, base, token, "mp4", fileName, data)
		if err != nil {
			return err
		}
		// 飞书 media 类型需要封面 image_key；本切片统一按文件投递。
		msgType = "file"
		content = map[string]string{"file_key": fileKey}
	default:
		fileKey, err := p.uploadFile(ctx, cfg, base, token, "stream", fileName, data)
		if err != nil {
			return err
		}
		msgType = "file"
		content = map[string]string{"file_key": fileKey}
	}
	return p.sendMediaMessage(ctx, cfg, base, token, chatID, replyTo, msgType, content)
}

// resolveOutboundMediaBytes 解析出站媒体字节（本地路径 / 远端 URL 先下载）。
func (p *Provider) resolveOutboundMediaBytes(ctx context.Context, media connectors.Media) ([]byte, string, error) {
	if mediaURL := strings.TrimSpace(media.URL); mediaURL != "" {
		// SSRF 防护（docs/bots/08 §7.3）：仅 http(s) + 公网宿主校验，重定向
		// 逐跳复用同一策略（outboundMediaFetchClient）。
		if _, err := p.validateMediaURL(mediaURL); err != nil {
			return nil, "", err
		}
		requestCtx, cancel := context.WithTimeout(ctx, mediaDownloadTimeout)
		defer cancel()
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, mediaURL, nil)
		if err != nil {
			return nil, "", fmt.Errorf("feishu: invalid media url %q", mediaURL)
		}
		response, err := p.outboundMediaFetchClient().Do(request)
		if err != nil {
			return nil, "", fmt.Errorf("feishu: download media url failed: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, "", fmt.Errorf("feishu: media url returned %s", response.Status)
		}
		limit := p.mediaLimit()
		data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		if err != nil {
			return nil, "", fmt.Errorf("feishu: read media url failed: %w", err)
		}
		if int64(len(data)) > limit {
			return nil, "", fmt.Errorf("feishu: media too large (> %d bytes)", limit)
		}
		return data, firstNonEmpty(media.FileName, filepath.Base(mediaURL)), nil
	}

	mediaPath := strings.TrimSpace(media.Path)
	if mediaPath == "" {
		return nil, "", fmt.Errorf("feishu: media requires a local path or remote url (kind=%s)", media.Kind)
	}
	if strings.HasPrefix(strings.ToLower(mediaPath), "file://") {
		parsed, err := url.Parse(mediaPath)
		if err != nil {
			return nil, "", fmt.Errorf("feishu: invalid media file url %q", mediaPath)
		}
		mediaPath = filepath.FromSlash(parsed.Path)
		if len(mediaPath) >= 3 && (mediaPath[0] == '\\' || mediaPath[0] == '/') && mediaPath[2] == ':' {
			mediaPath = mediaPath[1:]
		}
	}
	if !filepath.IsAbs(mediaPath) {
		return nil, "", fmt.Errorf("feishu: media file path must be absolute: %s", mediaPath)
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		return nil, "", fmt.Errorf("feishu: stat media file %q failed: %w", mediaPath, err)
	}
	if info.IsDir() {
		return nil, "", fmt.Errorf("feishu: media file path must be a file: %s", mediaPath)
	}
	data, err := os.ReadFile(mediaPath)
	if err != nil {
		return nil, "", fmt.Errorf("feishu: read media file %q failed: %w", mediaPath, err)
	}
	return data, firstNonEmpty(media.FileName, filepath.Base(mediaPath)), nil
}

func (p *Provider) uploadImage(ctx context.Context, cfg connectors.Config, base, token, fileName string, data []byte) (string, error) {
	var response mediaUploadResponse
	if err := p.doMultipart(ctx, base, imageUploadPath, token,
		map[string]string{"image_type": "message"}, "image", fileName, data, &response,
		cfg.Secret(secretAppSecret), token); err != nil {
		return "", err
	}
	if response.Code != 0 {
		return "", newAPIError("im/v1/images", response.Code, response.Msg, cfg.Secret(secretAppSecret), token)
	}
	if strings.TrimSpace(response.Data.ImageKey) == "" {
		return "", errors.New("feishu: upload image returned empty image_key")
	}
	return strings.TrimSpace(response.Data.ImageKey), nil
}

func (p *Provider) uploadFile(ctx context.Context, cfg connectors.Config, base, token, fileType, fileName string, data []byte) (string, error) {
	var response mediaUploadResponse
	if err := p.doMultipart(ctx, base, fileUploadPath, token,
		map[string]string{"file_type": fileType, "file_name": fileName}, "file", fileName, data, &response,
		cfg.Secret(secretAppSecret), token); err != nil {
		return "", err
	}
	if response.Code != 0 {
		return "", newAPIError("im/v1/files", response.Code, response.Msg, cfg.Secret(secretAppSecret), token)
	}
	if strings.TrimSpace(response.Data.FileKey) == "" {
		return "", errors.New("feishu: upload file returned empty file_key")
	}
	return strings.TrimSpace(response.Data.FileKey), nil
}

// sendMediaMessage 发送媒体消息：reply 原消息优先，失败降级 chat 直发（同文本路径）。
func (p *Provider) sendMediaMessage(ctx context.Context, cfg connectors.Config, base, token, chatID, replyTo, msgType string, content map[string]string) error {
	encoded, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("encode feishu %s content: %w", msgType, err)
	}
	appSecret := cfg.Secret(secretAppSecret)

	if replyTo != "" {
		var response sendMessageResponse
		sendErr := p.doJSON(ctx, http.MethodPost, base, fmt.Sprintf(messageReplyPathFmt, url.PathEscape(replyTo)), token, map[string]any{
			"content":  string(encoded),
			"msg_type": msgType,
		}, &response, appSecret, token)
		if sendErr == nil {
			if response.Code != 0 {
				return newAPIError("im/v1/messages/reply", response.Code, response.Msg, appSecret, token)
			}
			return nil
		}
		if !shouldFallbackToCreate(sendErr) {
			return sendErr
		}
		p.logf("feishu: reply 媒体到消息 %s 失败（%v），降级为 chat 直发", replyTo, sendErr)
	}

	var response sendMessageResponse
	if err := p.doJSON(ctx, http.MethodPost, base, messageCreatePath+"?receive_id_type=chat_id", token, map[string]any{
		"receive_id": chatID,
		"content":    string(encoded),
		"msg_type":   msgType,
	}, &response, appSecret, token); err != nil {
		return err
	}
	if response.Code != 0 {
		return newAPIError("im/v1/messages", response.Code, response.Msg, appSecret, token)
	}
	return nil
}

// doMultipart 以 multipart/form-data 调用飞书接口并解包响应。
func (p *Provider) doMultipart(ctx context.Context, base, path, bearer string, fields map[string]string, fileField, fileName string, data []byte, out any, secrets ...string) error {
	ctx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	endpoint, err := buildURL(base, path)
	if err != nil {
		return err
	}
	redactAll := func(message string) string {
		return redact(message, append(append([]string{}, secrets...), bearer)...)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return fmt.Errorf("encode feishu %s field: %w", path, err)
		}
	}
	part, err := writer.CreateFormFile(fileField, filepath.Base(fileName))
	if err != nil {
		return fmt.Errorf("encode feishu %s file: %w", path, err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("encode feishu %s file: %w", path, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("encode feishu %s body: %w", path, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return fmt.Errorf("build feishu %s request: %s", path, redactAll(err.Error()))
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if strings.TrimSpace(bearer) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	}
	return p.doRequest(request, path, bearer, out, secrets...)
}

func feishuMediaLabel(media connectors.Media) string {
	label := firstNonEmpty(media.FileName, media.Path, media.URL, media.Kind)
	runes := []rune(label)
	if len(runes) > 60 {
		return string(runes[:60]) + "…"
	}
	return label
}

// ── 临时文件与清扫 ──────────────────────────────────────────────────────────

func persistMediaTempFile(data []byte, nameHint string) (string, error) {
	dir, err := os.MkdirTemp("", mediaTempDirPrefix+"*")
	if err != nil {
		return "", fmt.Errorf("feishu: create media dir failed: %w", err)
	}
	path := filepath.Join(dir, sanitizeMediaFileName(nameHint))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("feishu: write media file failed: %w", err)
	}
	return path, nil
}

func sanitizeMediaFileName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "feishu-media.bin"
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
		return "feishu-media.bin"
	}
	return cleaned
}

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
