// 媒体链路（P3 真媒体切片）：入站 CDN 引用下载 + AES-ECB 解密 + 落盘
// （语音 silk→wav 按解码器可用性降级），出站文件上传（getuploadurl →
// AES-ECB 加密 → CDN 上传 → x-encrypted-param）与媒体消息构造。
//
// 协议镜像上游 codex-server/backend/internal/bots/wechat_media.go 与
// wechat_voice_transcode.go；依赖外部工具（silk 解码器 / ffmpeg）时按
// "工具缺失即降级"惯例处理：入站语音转码失败保留 silk 原件，出站不依赖
// 转码。
package wechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// cdnBaseURLKey 是媒体 CDN 基址的 Settings 键（媒体上传/下载走 CDN，
	// 与 iLink API 基址相互独立）。
	cdnBaseURLKey = "cdn_base_url"
	// mediaLimitSetting 是媒体体量上限（MB）的 Settings 键（Schema
	// `media_limit_mb`；缺省/非法值回退 maxMediaTransferBytes）。
	mediaLimitSetting = "media_limit_mb"
	// defaultCDNBaseURL 是 iLink 官方媒体 CDN 默认基址（与上游一致）。
	defaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

	// 上传媒体类型（getuploadurl.media_type）。
	uploadMediaTypeImage = 1
	uploadMediaTypeVideo = 2
	uploadMediaTypeFile  = 3

	// maxMediaTransferBytes 是单文件下载/上传的默认体量上限（含远端 URL 中转；
	// 可被 WithInboundMediaLimit / Schema `media_limit_mb` 覆盖）。
	maxMediaTransferBytes = 100 << 20
	// mediaUploadAttempts 是 CDN 上传的瞬时失败（网络/5xx）重试次数。
	mediaUploadAttempts = 3

	// voiceTranscodeTimeout 是单条入站语音转码的超时上限。
	voiceTranscodeTimeout = 15 * time.Second

	// mediaTempRetention 是临时媒体目录的清理窗口；mediaTempSweepEvery 是
	// 「每 N 次落盘触发一次清扫」的节流（避免每条消息全目录扫描）。
	mediaTempRetention  = 24 * time.Hour
	mediaTempSweepEvery = 64
)

// mediaTempSweepCounter 是临时媒体清扫的节流计数（进程内，跨连接器共享）。
var mediaTempSweepCounter atomic.Uint64

// mediaLimitFor 返回生效的单文件体量上限（字节）：WithInboundMediaLimit
// （构造级）优先，其次连接器 `media_limit_mb`（运行时配置），最后默认值。
// 非法值（<=0）逐层回退。
func (p *Provider) mediaLimitFor(cfg connectors.Config) int64 {
	if p.mediaLimitBytes > 0 {
		return p.mediaLimitBytes
	}
	if mb, err := strconv.ParseInt(strings.TrimSpace(cfg.Setting(mediaLimitSetting, "")), 10, 64); err == nil && mb > 0 {
		return mb << 20
	}
	return maxMediaTransferBytes
}

// 媒体类别常量（与 connectors.Media.Kind 对应；voice 出站时降级为 file）。
const (
	mediaKindImage = "image"
	mediaKindVoice = "voice"
	mediaKindVideo = "video"
	mediaKindFile  = "file"
)

// wechatGetUploadURLResponse 是 getuploadurl 的响应体。
type wechatGetUploadURLResponse struct {
	apiEnvelope
	UploadParam      string `json:"upload_param"`
	ThumbUploadParam string `json:"thumb_upload_param"`
	UploadFullURL    string `json:"upload_full_url"`
}

// uploadedMedia 是一次 CDN 上传的结果（构造出站 item 所需）。
type uploadedMedia struct {
	Kind           string
	FileName       string
	ContentType    string
	PlaintextSize  int64
	CiphertextSize int64
	DownloadParam  string
	AESKey         []byte
}

// cdnBaseFor 解析媒体 CDN 基址：Settings["cdn_base_url"] > 官方默认地址。
func cdnBaseFor(cfg connectors.Config) string {
	if base := strings.TrimSpace(cfg.Setting(cdnBaseURLKey, "")); base != "" {
		return strings.TrimRight(base, "/")
	}
	return defaultCDNBaseURL
}

// mediaFileID 取媒体的可下载句柄：优先 full_url，退回加密查询参数。
func mediaFileID(media *wechatCDNMedia) string {
	if media == nil {
		return ""
	}
	if fullURL := strings.TrimSpace(media.FullURL); fullURL != "" {
		return fullURL
	}
	return strings.TrimSpace(media.EncryptQueryParam)
}

// ── 入站：下载 + 解密 + 落盘（+ 语音转码） ──────────────────────────────────

// inboundMediaRef 是一条入站媒体项的下载原料（元数据 + CDN 引用）。
type inboundMediaRef struct {
	media     connectors.Media
	ref       *wechatCDNMedia
	aesKeyHex string
	voice     bool
}

// extractInboundMediaRefs 把消息 item 列表转为媒体引用列表（无媒体的 item 不
// 产生条目；item 缺具体负载的畸形条目跳过，与上游一致）。语音恒产生条目
// （有转写时正文已由 extractText 并入，音频文件仍可供 agent 使用）。
//
// 当直接条目均无媒体时，回退到引用链（回复了一条纯媒体消息）里找媒体，
// 与上游 findReferencedInboundWeChatMediaItem 对齐。
func extractInboundMediaRefs(items []wechatMessageItem) []inboundMediaRef {
	refs := collectInboundMediaRefs(items)
	if len(refs) > 0 {
		return refs
	}
	if referenced, ok := findReferencedInboundMediaItem(items); ok {
		return collectInboundMediaRefs([]wechatMessageItem{referenced})
	}
	return nil
}

func collectInboundMediaRefs(items []wechatMessageItem) []inboundMediaRef {
	var refs []inboundMediaRef
	for _, item := range items {
		switch item.Type {
		case itemTypeImage:
			if item.ImageItem == nil {
				continue
			}
			ref := inboundMediaRef{media: connectors.Media{Kind: mediaKindImage, ContentType: "image/*"}}
			ref.media.FileID = mediaFileID(item.ImageItem.Media)
			ref.media.Size = item.ImageItem.MidSize
			// image_item.url 是图片的展示地址（上游摘要 source_url 取它）。
			ref.media.URL = strings.TrimSpace(item.ImageItem.URL)
			ref.ref = item.ImageItem.Media
			ref.aesKeyHex = item.ImageItem.AESKeyHex
			refs = append(refs, ref)
		case itemTypeVoice:
			if item.VoiceItem == nil {
				continue
			}
			ref := inboundMediaRef{
				media: connectors.Media{Kind: mediaKindVoice, ContentType: "audio/silk"},
				voice: true,
			}
			ref.media.FileID = mediaFileID(item.VoiceItem.Media)
			ref.ref = item.VoiceItem.Media
			refs = append(refs, ref)
		case itemTypeFile:
			if item.FileItem == nil {
				continue
			}
			ref := inboundMediaRef{media: connectors.Media{Kind: mediaKindFile}}
			ref.media.FileID = mediaFileID(item.FileItem.Media)
			ref.media.FileName = strings.TrimSpace(item.FileItem.FileName)
			ref.media.Size = parseSize(item.FileItem.Len)
			ref.ref = item.FileItem.Media
			refs = append(refs, ref)
		case itemTypeVideo:
			if item.VideoItem == nil {
				continue
			}
			ref := inboundMediaRef{media: connectors.Media{Kind: mediaKindVideo, ContentType: "video/mp4"}}
			ref.media.FileID = mediaFileID(item.VideoItem.Media)
			ref.media.Size = item.VideoItem.VideoSize
			ref.ref = item.VideoItem.Media
			refs = append(refs, ref)
		}
	}
	return refs
}

// findReferencedInboundMediaItem 在引用链里找一条媒体消息（与上游同序：
// image → video → file → voice）。
func findReferencedInboundMediaItem(items []wechatMessageItem) (wechatMessageItem, bool) {
	for _, itemType := range []int{itemTypeImage, itemTypeVideo, itemTypeFile, itemTypeVoice} {
		for _, item := range items {
			candidates := []*wechatReferenceMsg{item.RefMsg}
			if item.TextItem != nil {
				candidates = append(candidates, item.TextItem.RefMsg)
			}
			for _, reference := range candidates {
				if reference == nil || reference.MessageItem == nil {
					continue
				}
				if reference.MessageItem.Type == itemType {
					return *reference.MessageItem, true
				}
			}
		}
	}
	return wechatMessageItem{}, false
}

// resolveInboundMedia 逐条尝试下载/解密/落盘；失败时保留元数据（文本摘要
// 仍含附件块），不阻塞整条消息。语音成功转码为 wav 后删除 silk 中间件。
func (p *Provider) resolveInboundMedia(ctx context.Context, cfg connectors.Config, refs []inboundMediaRef) []connectors.Media {
	if len(refs) == 0 {
		return nil
	}
	cdnBase := cdnBaseFor(cfg)
	out := make([]connectors.Media, 0, len(refs))
	for _, item := range refs {
		entry := item.media
		if item.ref != nil {
			if entry.URL == "" {
				if sourceURL := strings.TrimSpace(item.ref.FullURL); sourceURL != "" {
					entry.URL = sourceURL
				}
			}
			key, keyErr := parseInboundAESKey(item.aesKeyHex, item.ref)
			if keyErr != nil {
				p.logf("wechat: inbound media aes key invalid (%s): %v", entry.Kind, keyErr)
			}
			data, contentType, err := p.downloadInboundMedia(ctx, cfg, cdnBase, item.ref, key)
			if err != nil {
				p.logf("wechat: inbound media download failed (%s): %v", entry.Kind, err)
			} else {
				nameHint := entry.FileName
				if nameHint == "" {
					nameHint = defaultInboundMediaName(entry.Kind)
				}
				path, persistErr := p.persistTempMedia(data, contentType, nameHint, "inbound")
				if persistErr != nil {
					p.logf("wechat: persist inbound media failed (%s): %v", entry.Kind, persistErr)
				} else {
					entry.Path = path
					entry.ContentType = firstNonEmpty(contentType, entry.ContentType)
					if entry.FileName == "" {
						entry.FileName = filepath.Base(path)
					}
					if item.voice {
						if wavPath, transcodeErr := p.tryTranscodeInboundVoice(ctx, path); transcodeErr == nil {
							_ = os.Remove(path)
							entry.Path = wavPath
							entry.ContentType = "audio/wav"
							entry.FileName = filepath.Base(wavPath)
						} else {
							p.logf("wechat: voice transcode skipped（保留 silk 原件）: %v", transcodeErr)
						}
					}
				}
			}
		}
		out = append(out, entry)
	}
	return out
}

// downloadInboundMedia 下载一条入站 CDN 引用（可带 AES 密钥）并返回明文与
// CDN 返回的 Content-Type。入站 URL 来自平台下发/运维配置的 CDN（非模型
// 输出），不做 SSRF 宿主校验；出站远端 URL 的校验见 downloadRemoteOutboundMedia。
func (p *Provider) downloadInboundMedia(ctx context.Context, cfg connectors.Config, cdnBase string, ref *wechatCDNMedia, key []byte) ([]byte, string, error) {
	if ref == nil {
		return nil, "", errors.New("wechat: media reference is missing")
	}
	targetURL := strings.TrimSpace(ref.FullURL)
	if targetURL == "" {
		queryParam := strings.TrimSpace(ref.EncryptQueryParam)
		if queryParam == "" {
			return nil, "", errors.New("wechat: media download reference is incomplete")
		}
		targetURL = strings.TrimRight(strings.TrimSpace(cdnBase), "/") +
			"/download?encrypted_query_param=" + url.QueryEscape(queryParam)
	}

	requestCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build wechat media download request: %w", err)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("wechat media download failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, "", fmt.Errorf("wechat media download returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	limit := p.mediaLimitFor(cfg)
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("read wechat media download body: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, "", fmt.Errorf("wechat media download exceeds %d bytes", limit)
	}
	contentType := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type")))
	if len(key) == 0 {
		return data, contentType, nil
	}
	plaintext, err := decryptAESECB(data, key)
	if err != nil {
		return nil, "", err
	}
	return plaintext, contentType, nil
}

// parseInboundAESKey 解析入站媒体密钥：优先 item 上的 aeskey（hex），否则取
// media.aes_key（base64 包裹的 16 字节原始密钥或 32 字节 hex 文本）。
func parseInboundAESKey(hexValue string, ref *wechatCDNMedia) ([]byte, error) {
	if trimmed := strings.TrimSpace(hexValue); trimmed != "" {
		return hex.DecodeString(trimmed)
	}
	if ref == nil || strings.TrimSpace(ref.AESKey) == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ref.AESKey))
	if err != nil {
		return nil, fmt.Errorf("decode wechat media aes key: %w", err)
	}
	switch {
	case len(decoded) == 16:
		return decoded, nil
	case len(decoded) == 32 && isASCIIHex(decoded):
		return hex.DecodeString(string(decoded))
	default:
		return nil, fmt.Errorf("unexpected wechat media aes key length %d", len(decoded))
	}
}

func isASCIIHex(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	for _, ch := range value {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		case ch >= 'A' && ch <= 'F':
		default:
			return false
		}
	}
	return true
}

// defaultInboundMediaName 给无文件名的媒体一个可读的落盘名。
func defaultInboundMediaName(kind string) string {
	switch kind {
	case mediaKindVoice:
		return "voice.silk"
	case mediaKindVideo:
		return "video.mp4"
	case mediaKindImage:
		return "image"
	default:
		return "file"
	}
}

// ── 出站：本地/远端文件解析 ────────────────────────────────────────────────

// resolveOutboundMediaFile 解析一条出站媒体的本地文件：Path（含 file://）
// 直接使用；URL 先下载到临时目录（带 cleanup）。返回绝对路径、文件名与
// Content-Type。
func (p *Provider) resolveOutboundMediaFile(ctx context.Context, cfg connectors.Config, media connectors.Media) (string, string, string, func(), error) {
	if rawURL := strings.TrimSpace(media.URL); rawURL != "" {
		return p.downloadRemoteOutboundMedia(ctx, cfg, rawURL, media.FileName, media.Kind)
	}
	mediaPath := strings.TrimSpace(media.Path)
	if mediaPath == "" {
		return "", "", "", nil, fmt.Errorf("wechat: media requires a local path or remote url (kind=%s)", media.Kind)
	}
	if strings.HasPrefix(strings.ToLower(mediaPath), "file://") {
		parsed, err := url.Parse(mediaPath)
		if err != nil {
			return "", "", "", nil, fmt.Errorf("wechat: invalid media file url %q", mediaPath)
		}
		mediaPath = filepath.FromSlash(parsed.Path)
	}
	if !filepath.IsAbs(mediaPath) {
		return "", "", "", nil, fmt.Errorf("wechat: media file path must be absolute: %s", mediaPath)
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("stat wechat media file %q: %w", mediaPath, err)
	}
	if info.IsDir() {
		return "", "", "", nil, fmt.Errorf("wechat: media file path must be a file: %s", mediaPath)
	}
	contentType := detectContentType(mediaPath, media.ContentType)
	return mediaPath, firstNonEmpty(media.FileName, filepath.Base(mediaPath)), contentType, nil, nil
}

// downloadRemoteOutboundMedia 解析一条出站远端媒体为可上传的本地文件（带
// cleanup）：直链媒体直接落盘；页面型 URL 由 resolver 提取候选并递归解析
// （resolver.go）；流媒体清单先转码（remote_video.go）。宿主安全校验（SSRF）
// 与声明类型校验在解析链路内逐跳执行。
func (p *Provider) downloadRemoteOutboundMedia(ctx context.Context, cfg connectors.Config, rawURL, fileNameHint, declaredKind string) (string, string, string, func(), error) {
	filePath, contentType, cleanup, err := p.resolveOutboundRemoteMedia(ctx, cfg, rawURL, fileNameHint, declaredKind)
	if err != nil {
		return "", "", "", nil, err
	}
	return filePath, filepath.Base(filePath), contentType, cleanup, nil
}

// fileNameFromURL 从 URL 路径取文件名（去 query/fragment）。
func fileNameFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(parsed.Path))
}

// ── 出站远端媒体安全（SSRF / 声明类型校验） ─────────────────────────────────

// validateMediaURL 校验远端媒体 URL 的 scheme 与宿主：HTTP(S) 之外一律拒绝；
// 宿主校验默认走 validatePublicMediaHost（SSRF 防护），可用 WithMediaHostPolicy
// 覆盖（测试/内网部署逃生口）。
func (p *Provider) validateMediaURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("wechat: media url must be absolute: %q", rawURL)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("wechat: media url scheme %q is not allowed", parsed.Scheme)
	}
	policy := p.mediaHostPolicy
	if policy == nil {
		policy = validatePublicMediaHost
	}
	if err := policy(parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

// outboundMediaFetchClient 返回用于出站远端媒体下载的客户端：复制基客户端并
// 逐跳校验重定向目标宿主（防止 302 绕过首跳校验）。
func (p *Provider) outboundMediaFetchClient() *http.Client {
	base := p.client
	if base == nil {
		base = &http.Client{}
	}
	clone := *base
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("wechat: too many redirects while fetching media")
		}
		_, err := p.validateMediaURL(req.URL.String())
		return err
	}
	return &clone
}

// validatePublicMediaHost 是默认宿主校验：解析域名并要求所有地址均为公网
// 单播（拒绝回环/私网/链路本地/未指定/组播/CGNAT，docs/bots/08 §7.3）。
func validatePublicMediaHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("wechat: media url host is required")
	}
	addresses, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("wechat: resolve media host %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("wechat: media host %q resolved to no addresses", host)
	}
	for _, address := range addresses {
		if isDisallowedMediaIP(address) {
			return fmt.Errorf("wechat: media host %q resolves to disallowed address %s", host, address)
		}
	}
	return nil
}

// isDisallowedMediaIP 报告地址是否属于禁止访问的类别（SSRF 防护）。
func isDisallowedMediaIP(address net.IP) bool {
	if address == nil {
		return true
	}
	if !address.IsGlobalUnicast() || address.IsPrivate() {
		return true
	}
	return isSharedAddressSpace(address)
}

// isSharedAddressSpace 报告是否为 RFC 6598 运营商级 NAT 共享地址段
// （100.64.0.0/10；Go 的 IsPrivate 不含该段，需显式拦截）。
func isSharedAddressSpace(address net.IP) bool {
	ipv4 := address.To4()
	return ipv4 != nil && ipv4[0] == 100 && ipv4[1]&0xc0 == 64
}

// validateOutboundRemoteMediaType 校验下载结果与声明类型一致：HTML/JSON 一律
// 拒绝（比上游更严：即使 URL 扩展名伪装成图片）；其余按 Content-Type 或 URL
// 扩展名判定 image/video；file/空声明不做额外限制。
func validateOutboundRemoteMediaType(finalURL, contentType, declaredKind string) error {
	kind := normalizeMediaKind(declaredKind)
	if kind != mediaKindImage && kind != mediaKindVideo {
		return nil
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(contentType, "text/html"),
		strings.HasPrefix(contentType, "application/xhtml"),
		strings.HasPrefix(contentType, "application/json"):
		return fmt.Errorf("wechat: remote %s %q resolved to %s, not a directly uploadable file",
			kind, finalURL, firstNonEmpty(contentType, "unknown content type"))
	}
	switch kind {
	case mediaKindImage:
		if strings.HasPrefix(contentType, "image/") || looksLikeDirectImageURL(finalURL) {
			return nil
		}
	default:
		if strings.HasPrefix(contentType, "video/") || looksLikeDirectVideoURL(finalURL) {
			return nil
		}
	}
	return fmt.Errorf("wechat: remote %s %q is not a directly uploadable file (content-type %q)",
		kind, finalURL, firstNonEmpty(contentType, "unknown"))
}

func looksLikeDirectImageURL(rawURL string) bool {
	return hasMediaExtension(rawURL, ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp")
}

func looksLikeDirectVideoURL(rawURL string) bool {
	return hasMediaExtension(rawURL, ".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi")
}

func hasMediaExtension(rawURL string, extensions ...string) bool {
	trimmed := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		// 与上游一致：解析失败时退回对整串（小写）做后缀匹配。
		lowerRaw := strings.ToLower(trimmed)
		for _, extension := range extensions {
			if strings.HasSuffix(lowerRaw, extension) {
				return true
			}
		}
		return false
	}
	lowerPath := strings.ToLower(parsed.Path)
	for _, extension := range extensions {
		if strings.HasSuffix(lowerPath, extension) {
			return true
		}
	}
	return false
}

// ── 出站：上传（getuploadurl + AES-ECB + CDN） ──────────────────────────────

// uploadOutboundMedia 上传一个本地文件到 iLink CDN：明文 md5 校验、随机
// aeskey 加密（filesize 为密文长度）、getuploadurl 换上传地址、上传后从
// x-encrypted-param 响应头取 download_param（发送 item 时回填）。
func (p *Provider) uploadOutboundMedia(
	ctx context.Context,
	cfg connectors.Config,
	base, cdnBase, token, routeTag, toUserID string,
	filePath, fileName, contentType, declaredKind string,
) (uploadedMedia, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return uploadedMedia{}, fmt.Errorf("read wechat media file %q: %w", filePath, err)
	}
	limit := p.mediaLimitFor(cfg)
	if int64(len(data)) > limit {
		return uploadedMedia{}, fmt.Errorf("wechat media file %q exceeds %d bytes", filePath, limit)
	}

	kind, uploadMediaType, resolvedContentType := classifyOutboundMedia(filePath, contentType, declaredKind)
	fileKey, err := randomFileKey()
	if err != nil {
		return uploadedMedia{}, fmt.Errorf("generate wechat media file key: %w", err)
	}
	aesKey := make([]byte, 16)
	if _, err := rand.Read(aesKey); err != nil {
		return uploadedMedia{}, fmt.Errorf("generate wechat media aes key: %w", err)
	}
	ciphertext, err := encryptAESECB(data, aesKey)
	if err != nil {
		return uploadedMedia{}, err
	}
	checksum := md5.Sum(data)

	var uploadResponse wechatGetUploadURLResponse
	if err := p.callJSON(ctx, defaultHTTPTimeout, base, token, routeTag, http.MethodPost, "/ilink/bot/getuploadurl", map[string]any{
		"filekey":       fileKey,
		"media_type":    uploadMediaType,
		"to_user_id":    strings.TrimSpace(toUserID),
		"rawsize":       len(data),
		"rawfilemd5":    hex.EncodeToString(checksum[:]),
		"filesize":      len(ciphertext),
		"no_need_thumb": true,
		"aeskey":        hex.EncodeToString(aesKey),
		"base_info":     wechatBaseInfo{ChannelVersion: channelVersion},
	}, &uploadResponse); err != nil {
		return uploadedMedia{}, fmt.Errorf("wechat media upload prepare failed (%s): %w", kind, err)
	}

	downloadParam, err := p.uploadCiphertext(ctx, cdnBase, uploadResponse, fileKey, ciphertext)
	if err != nil {
		return uploadedMedia{}, err
	}

	return uploadedMedia{
		Kind:           kind,
		FileName:       fileName,
		ContentType:    resolvedContentType,
		PlaintextSize:  int64(len(data)),
		CiphertextSize: int64(len(ciphertext)),
		DownloadParam:  downloadParam,
		AESKey:         aesKey,
	}, nil
}

// uploadCiphertext 把密文 POST 到 CDN 上传地址；网络错误/5xx 重试，4xx 直接
// 失败。成功后返回 download 侧需要的 x-encrypted-param。
func (p *Provider) uploadCiphertext(ctx context.Context, cdnBase string, uploadInfo wechatGetUploadURLResponse, fileKey string, ciphertext []byte) (string, error) {
	targetURL := strings.TrimSpace(uploadInfo.UploadFullURL)
	if targetURL == "" {
		uploadParam := strings.TrimSpace(uploadInfo.UploadParam)
		if uploadParam == "" {
			return "", errors.New("wechat media upload url response was incomplete")
		}
		targetURL = strings.TrimRight(strings.TrimSpace(cdnBase), "/") +
			"/upload?encrypted_query_param=" + url.QueryEscape(uploadParam) +
			"&filekey=" + url.QueryEscape(strings.TrimSpace(fileKey))
	}

	var lastErr error
	for attempt := 0; attempt < mediaUploadAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, time.Duration(attempt)*500*time.Millisecond); err != nil {
				return "", err
			}
		}
		requestCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, targetURL, bytes.NewReader(ciphertext))
		if err != nil {
			cancel()
			return "", fmt.Errorf("build wechat media upload request: %w", err)
		}
		request.Header.Set("Content-Type", "application/octet-stream")

		response, err := p.client.Do(request)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("wechat media upload request failed: %w", err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		response.Body.Close()
		cancel()

		if response.StatusCode >= 400 && response.StatusCode < 500 {
			return "", fmt.Errorf("wechat media upload returned %s: %s", response.Status, strings.TrimSpace(string(body)))
		}
		if response.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("wechat media upload returned %s: %s", response.Status, strings.TrimSpace(string(body)))
			continue
		}
		downloadParam := strings.TrimSpace(response.Header.Get("x-encrypted-param"))
		if downloadParam == "" {
			lastErr = errors.New("wechat media upload response missing x-encrypted-param header")
			continue
		}
		return downloadParam, nil
	}
	if lastErr == nil {
		lastErr = errors.New("wechat media upload failed")
	}
	return "", lastErr
}

// classifyOutboundMedia 归一化出站媒体类别与上传类型；declaredKind 缺失时按
// Content-Type 推断。
func classifyOutboundMedia(filePath, contentType, declaredKind string) (string, int, string) {
	kind := normalizeMediaKind(declaredKind)
	contentType = detectContentType(filePath, contentType)
	if kind == "" {
		switch {
		case strings.HasPrefix(contentType, "image/"):
			kind = mediaKindImage
		case strings.HasPrefix(contentType, "video/"):
			kind = mediaKindVideo
		default:
			kind = mediaKindFile
		}
	}
	switch kind {
	case mediaKindImage:
		return kind, uploadMediaTypeImage, contentType
	case mediaKindVideo:
		return kind, uploadMediaTypeVideo, contentType
	default:
		return mediaKindFile, uploadMediaTypeFile, contentType
	}
}

// normalizeMediaKind 归一化媒体类别（未知返回空串）。
func normalizeMediaKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case mediaKindImage:
		return mediaKindImage
	case mediaKindVideo:
		return mediaKindVideo
	case mediaKindVoice:
		return mediaKindVoice
	case mediaKindFile:
		return mediaKindFile
	default:
		return ""
	}
}

// detectContentType 解析 Content-Type：显式声明 > 扩展名 > 512 字节嗅探。
func detectContentType(filePath, declared string) string {
	if declared = strings.TrimSpace(declared); declared != "" {
		if index := strings.Index(declared, ";"); index >= 0 {
			declared = declared[:index]
		}
		return strings.ToLower(strings.TrimSpace(declared))
	}
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath))); byExt != "" {
		if index := strings.Index(byExt, ";"); index >= 0 {
			byExt = byExt[:index]
		}
		return strings.ToLower(strings.TrimSpace(byExt))
	}
	handle, err := os.Open(filePath)
	if err != nil {
		return "application/octet-stream"
	}
	defer handle.Close()
	buffer := make([]byte, 512)
	size, _ := io.ReadFull(handle, buffer)
	return strings.ToLower(strings.TrimSpace(http.DetectContentType(buffer[:size])))
}

// buildOutboundMediaItem 把上传结果构造为 sendmessage item。
//
// aes_key 线上格式与上游一致：base64(ascii-hex(aeskey))，而非 base64(原始
// 16 字节)——编码不匹配时接收端会下载失败。
func buildOutboundMediaItem(uploaded uploadedMedia) wechatMessageItem {
	encodedKey := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(uploaded.AESKey)))
	media := &wechatCDNMedia{
		EncryptQueryParam: uploaded.DownloadParam,
		AESKey:            encodedKey,
		EncryptType:       1,
	}
	switch uploaded.Kind {
	case mediaKindImage:
		return wechatMessageItem{
			Type: itemTypeImage,
			ImageItem: &wechatImageItem{
				Media:   media,
				MidSize: uploaded.CiphertextSize,
			},
		}
	case mediaKindVideo:
		return wechatMessageItem{
			Type: itemTypeVideo,
			VideoItem: &wechatVideoItem{
				Media:     media,
				VideoSize: uploaded.CiphertextSize,
			},
		}
	default:
		return wechatMessageItem{
			Type: itemTypeFile,
			FileItem: &wechatFileItem{
				Media:    media,
				FileName: uploaded.FileName,
				Len:      strconv.FormatInt(uploaded.PlaintextSize, 10),
			},
		}
	}
}

// sendItemMessage 发送单条 item 消息（媒体消息与文本共用 sendmessage）。
func (p *Provider) sendItemMessage(ctx context.Context, base, token, routeTag, toUserID, contextToken string, item wechatMessageItem) error {
	var response apiEnvelope
	payload := wechatSendMessageRequest{
		Msg: wechatOutboundMessage{
			ToUserID:     strings.TrimSpace(toUserID),
			ClientID:     randomClientID(),
			MessageType:  messageTypeBot,
			MessageState: messageStateComplete,
			ItemList:     []wechatMessageItem{item},
			ContextToken: strings.TrimSpace(contextToken),
		},
		BaseInfo: wechatBaseInfo{ChannelVersion: channelVersion},
	}
	if err := p.callJSON(ctx, defaultHTTPTimeout, base, token, routeTag, http.MethodPost, "/ilink/bot/sendmessage", payload, &response); err != nil {
		return wrapSendError("item", itemSendSummary(item), err)
	}
	return nil
}

// itemSendSummary 生成 item 的错误摘要（不含 context_token）。
func itemSendSummary(item wechatMessageItem) string {
	switch item.Type {
	case itemTypeImage:
		return "type=image"
	case itemTypeVoice:
		return "type=voice"
	case itemTypeFile:
		name := ""
		if item.FileItem != nil {
			name = item.FileItem.FileName
		}
		return fmt.Sprintf("type=file name=%q", truncateRunes(name, 40))
	case itemTypeVideo:
		return "type=video"
	default:
		return fmt.Sprintf("type=%d", item.Type)
	}
}

// ── 出站编排入口 ────────────────────────────────────────────────────────────

// sendOutboundMedia 解析并发送一条出站媒体：本地路径/file:// 直接读，远端
// URL 先下载；随后上传并发送媒体消息（失败由调用方降级）。
func (p *Provider) sendOutboundMedia(ctx context.Context, cfg connectors.Config, base, cdnBase, token, routeTag, toUserID, contextToken string, media connectors.Media) error {
	filePath, fileName, contentType, cleanup, err := p.resolveOutboundMediaFile(ctx, cfg, media)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	uploaded, err := p.uploadOutboundMedia(ctx, cfg, base, cdnBase, token, routeTag, toUserID, filePath, fileName, contentType, media.Kind)
	if err != nil {
		return err
	}
	return p.sendItemMessage(ctx, base, token, routeTag, toUserID, contextToken, buildOutboundMediaItem(uploaded))
}

// ── 临时文件与密钥工具 ──────────────────────────────────────────────────────

// persistTempMedia 把媒体写入宿主临时目录（0600），返回落盘路径。
// 入站文件保留给 agent 读取；出站远端下载由调用方负责清理。落盘前按低频
// 节流顺带清扫过期文件（见 maybeSweepTempMedia）。
func (p *Provider) persistTempMedia(data []byte, contentType, fileNameHint, direction string) (string, error) {
	direction = strings.TrimSpace(direction)
	if direction == "" {
		direction = "inbound"
	}
	dir := filepath.Join(os.TempDir(), "aicli", "wechat", "media", direction)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create wechat media temp dir: %w", err)
	}
	p.maybeSweepTempMedia(dir)

	ext := filepath.Ext(strings.TrimSpace(fileNameHint))
	if ext == "" && contentType != "" {
		if extensions, err := mime.ExtensionsByType(contentType); err == nil && len(extensions) > 0 {
			ext = extensions[0]
		}
	}
	if ext == "" {
		ext = ".bin"
	}

	baseName := strings.TrimSpace(fileNameHint)
	if baseName == "" {
		randomPart, err := randomFileKey()
		if err != nil {
			return "", err
		}
		baseName = "wechat-media-" + randomPart + ext
	} else {
		baseName = filepath.Base(baseName)
		if baseName == "" || baseName == "." || baseName == string(filepath.Separator) {
			baseName = "wechat-media" + ext
		} else if filepath.Ext(baseName) == "" {
			baseName += ext
		}
	}

	filePath := filepath.Join(dir, baseName)
	if _, err := os.Stat(filePath); err == nil {
		randomPart, randomErr := randomFileKey()
		if randomErr != nil {
			return "", randomErr
		}
		filePath = filepath.Join(dir, strings.TrimSuffix(baseName, filepath.Ext(baseName))+"-"+randomPart+filepath.Ext(baseName))
	}
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		return "", fmt.Errorf("write wechat media temp file: %w", err)
	}
	return filePath, nil
}

// maybeSweepTempMedia 低频清扫该目录下超过保留期的常规文件（每
// mediaTempSweepEvery 次落盘触发一次，避免每条消息全目录扫描）；best-effort，
// 失败不影响落盘。
func (p *Provider) maybeSweepTempMedia(dir string) {
	if mediaTempSweepCounter.Add(1)%mediaTempSweepEvery != 1 {
		return
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	cutoff := now().Add(-mediaTempRetention)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// randomFileKey 生成 16 字节随机 hex 的 filekey（与上游一致）。
func randomFileKey() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// encryptAESECB 按 PKCS#7 填充后 AES-ECB 加密。
func encryptAESECB(plaintext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create wechat aes cipher: %w", err)
	}
	blockSize := block.BlockSize()
	padding := blockSize - (len(plaintext) % blockSize)
	if padding == 0 {
		padding = blockSize
	}
	padded := append(append([]byte(nil), plaintext...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	ciphertext := make([]byte, len(padded))
	for offset := 0; offset < len(padded); offset += blockSize {
		block.Encrypt(ciphertext[offset:offset+blockSize], padded[offset:offset+blockSize])
	}
	return ciphertext, nil
}

// decryptAESECB 解密 AES-ECB 并校验 PKCS#7 填充。
func decryptAESECB(ciphertext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create wechat aes cipher: %w", err)
	}
	blockSize := block.BlockSize()
	if len(ciphertext) == 0 || len(ciphertext)%blockSize != 0 {
		return nil, errors.New("wechat ciphertext size is invalid for AES-ECB")
	}
	plaintext := make([]byte, len(ciphertext))
	for offset := 0; offset < len(ciphertext); offset += blockSize {
		block.Decrypt(plaintext[offset:offset+blockSize], ciphertext[offset:offset+blockSize])
	}
	padding := int(plaintext[len(plaintext)-1])
	if padding <= 0 || padding > blockSize || padding > len(plaintext) {
		return nil, errors.New("wechat ciphertext padding is invalid")
	}
	for _, value := range plaintext[len(plaintext)-padding:] {
		if int(value) != padding {
			return nil, errors.New("wechat ciphertext padding is invalid")
		}
	}
	return plaintext[:len(plaintext)-padding], nil
}

// ── 入站语音转码（silk → wav，按工具可用性降级） ────────────────────────────

type voiceTranscodeCandidate struct {
	command string
	args    func(inputPath, outputPath string) []string
}

var (
	// lookPathVoiceCommand / runVoiceTranscode 可在测试中替换（转码工具替身）。
	lookPathVoiceCommand = exec.LookPath
	runVoiceTranscode    = runVoiceTranscodeCommand
)

// voiceTranscodeCandidates 返回按优先级排列的解码器候选：
// AICLI_WECHAT_SILK_DECODER / AICLI_WECHAT_VOICE_DECODER（自定义，参数
// <in> <out>）> silk_v3_decoder > ffmpeg（AICLI_WECHAT_FFMPEG 可覆盖路径）。
func voiceTranscodeCandidates() []voiceTranscodeCandidate {
	candidates := make([]voiceTranscodeCandidate, 0, 3)
	if decoder := strings.TrimSpace(firstNonEmpty(
		os.Getenv("AICLI_WECHAT_SILK_DECODER"),
		os.Getenv("AICLI_WECHAT_VOICE_DECODER"),
	)); decoder != "" {
		candidates = append(candidates, voiceTranscodeCandidate{
			command: decoder,
			args:    func(inputPath, outputPath string) []string { return []string{inputPath, outputPath} },
		})
	}
	ffmpegCommand := strings.TrimSpace(firstNonEmpty(os.Getenv("AICLI_WECHAT_FFMPEG"), "ffmpeg"))
	candidates = append(candidates,
		voiceTranscodeCandidate{
			command: "silk_v3_decoder",
			args:    func(inputPath, outputPath string) []string { return []string{inputPath, outputPath} },
		},
		voiceTranscodeCandidate{
			command: ffmpegCommand,
			args: func(inputPath, outputPath string) []string {
				return []string{"-y", "-nostdin", "-loglevel", "error", "-i", inputPath, outputPath}
			},
		},
	)
	return candidates
}

// transcodeVoiceToWAV 依次尝试解码器候选，返回产出的 wav 路径。
func transcodeVoiceToWAV(ctx context.Context, silkPath string) (string, error) {
	silkPath = strings.TrimSpace(silkPath)
	if silkPath == "" {
		return "", errors.New("wechat voice path is required")
	}
	if _, err := os.Stat(silkPath); err != nil {
		return "", fmt.Errorf("stat wechat voice file %q: %w", silkPath, err)
	}
	outputPath := strings.TrimSuffix(silkPath, filepath.Ext(silkPath)) + ".wav"
	candidates := voiceTranscodeCandidates()
	if len(candidates) == 0 {
		return "", errors.New("no wechat voice decoder command was configured")
	}
	errs := make([]error, 0, len(candidates))
	for _, candidate := range candidates {
		commandPath, err := lookPathVoiceCommand(candidate.command)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s was not found: %w", candidate.command, err))
			continue
		}
		if err := runVoiceTranscode(ctx, commandPath, candidate.args(silkPath, outputPath), outputPath); err != nil {
			errs = append(errs, err)
			continue
		}
		return outputPath, nil
	}
	return "", errors.Join(errs...)
}

func runVoiceTranscodeCommand(ctx context.Context, commandPath string, args []string, outputPath string) error {
	_ = os.Remove(outputPath)
	command := exec.CommandContext(ctx, commandPath, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		_ = os.Remove(outputPath)
		return fmt.Errorf("%s failed: %w: %s", filepath.Base(commandPath), err, compactCommandOutput(output))
	}
	info, statErr := os.Stat(outputPath)
	if statErr != nil {
		return fmt.Errorf("%s did not produce %q: %w", filepath.Base(commandPath), outputPath, statErr)
	}
	if info.Size() == 0 {
		_ = os.Remove(outputPath)
		return fmt.Errorf("%s produced an empty wav file", filepath.Base(commandPath))
	}
	return nil
}

// tryTranscodeInboundVoice 带上限超时调用转码（ctx 已有 deadline 时不叠加）。
func (p *Provider) tryTranscodeInboundVoice(ctx context.Context, silkPath string) (string, error) {
	transcodeCtx := ctx
	cancel := func() {}
	if transcodeCtx == nil {
		transcodeCtx = context.Background()
	}
	if _, hasDeadline := transcodeCtx.Deadline(); !hasDeadline {
		transcodeCtx, cancel = context.WithTimeout(transcodeCtx, voiceTranscodeTimeout)
	}
	defer cancel()
	return transcodeVoiceToWAV(transcodeCtx, silkPath)
}

func compactCommandOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return "no stderr output"
	}
	runes := []rune(text)
	if len(runes) > 240 {
		return string(runes[:240]) + "..."
	}
	return text
}
