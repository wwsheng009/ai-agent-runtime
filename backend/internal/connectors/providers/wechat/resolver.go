// 远端出站媒体解析（resolver）：移植上游 codex-server
// wechat_outbound_media_resolver.go 的核心链路——把「页面型 URL」解析为可直接
// 下载的媒体文件：
//
//   - 结构化文档判定（HTML/XHTML，或含媒体 URL 标记的 JSON）；
//   - 候选提取：<meta og:image/og:video>、<source>/<video>/<iframe>/<a> 标签、
//     内嵌 JSON 的媒体 URL 键、正文里的裸媒体直链；
//   - 相对/协议相对/绝对 URL 归一，逐候选递归（深度 ≤3 + 去重防环）；
//   - 叶子节点：声明 video 且是流媒体清单（m3u8/mpd）时先转码为 mp4
//     （见 remote_video.go，工具缺失/失败即报错，由上层降级为文本提示）；
//     其余走声明类型校验（media.go validateOutboundRemoteMediaType）后落盘。
//
// 未移植：上游的 dailymotion 短链重写（站点特化，收益低）。
// 网络访问沿用出站媒体安全策略：仅 http(s)，宿主默认拒绝回环/私网等，重定向
// 逐跳校验（media.go validateMediaURL / outboundMediaFetchClient）。
package wechat

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// remoteMediaResolveMaxDepth 是递归解析页面的最大深度（与上游一致）。
	remoteMediaResolveMaxDepth = 3
)

var (
	remoteHTMLTagPattern          = regexp.MustCompile(`(?is)<(meta|source|video|a|iframe)\b[^>]*>`)
	remoteHTMLAttributePattern    = regexp.MustCompile("(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\\s*=\\s*(?:\"([^\"]*)\"|'([^']*)'|([^\\s\"'=<>`]+))")
	remoteStructuredURLPattern    = regexp.MustCompile(`(?is)"(contentUrl|embedUrl|videoUrl|video_url|streamUrl|stream_url|streamHlsUrl|stream_hls_url|playbackUrl|playback_url|playerUrl|player_url|iframeUrl|iframe_url|manifestUrl|manifest_url|url|src)"\s*:\s*"((?:\\.|[^"])*)"`)
	remoteDirectMediaURLPattern   = regexp.MustCompile(`(?i)https?:\/\/[^"'<>\s]+?\.(?:mp4|mov|m4v|webm|avi|mkv|jpg|jpeg|png|gif|webp|bmp|pdf)(?:\?[^"'<>\s]*)?`)
	remoteHTMLDocTypePattern      = regexp.MustCompile(`(?is)<!doctype\s+html`)
	remoteHTMLBodyMarkerPattern   = regexp.MustCompile(`(?is)<(?:html|head|body|meta|title|script|video|source|iframe)\b`)
	remoteStructuredJSONURLMarker = regexp.MustCompile(`(?is)"(?:contentUrl|embedUrl|videoUrl|video_url|streamUrl|stream_url|streamHlsUrl|stream_hls_url|playbackUrl|playback_url|playerUrl|player_url|iframeUrl|iframe_url|manifestUrl|manifest_url|url|src)"\s*:`)
)

// remoteFetchedResource 是一次远端抓取的结果（页面或媒体文件）。
type remoteFetchedResource struct {
	data        []byte
	contentType string
	finalURL    string
}

// remoteMediaCandidate 是从页面中提取出的候选媒体地址。
type remoteMediaCandidate struct {
	url          string
	kind         string
	fileNameHint string
}

// resolveOutboundRemoteMedia 把一条出站远端 URL 解析为可直接上传的本地文件：
// 直链媒体直接落盘；页面型 URL 提取候选媒体后递归解析。返回本地路径与 cleanup。
func (p *Provider) resolveOutboundRemoteMedia(ctx context.Context, cfg connectors.Config, rawURL, fileNameHint, declaredKind string) (string, string, func(), error) {
	visited := make(map[string]struct{})
	return p.resolveOutboundRemoteMediaRecursive(
		ctx,
		cfg,
		strings.TrimSpace(rawURL),
		strings.TrimSpace(fileNameHint),
		normalizeMediaKind(declaredKind),
		0,
		visited,
	)
}

func (p *Provider) resolveOutboundRemoteMediaRecursive(
	ctx context.Context,
	cfg connectors.Config,
	rawURL string,
	fileNameHint string,
	declaredKind string,
	depth int,
	visited map[string]struct{},
) (string, string, func(), error) {
	if depth > remoteMediaResolveMaxDepth {
		return "", "", nil, fmt.Errorf("wechat: exceeded remote media resolution depth for %q", rawURL)
	}
	normalizedURL := strings.TrimSpace(rawURL)
	if normalizedURL == "" {
		return "", "", nil, fmt.Errorf("wechat: remote media url is required")
	}
	if _, ok := visited[normalizedURL]; ok {
		return "", "", nil, fmt.Errorf("wechat: detected recursive remote media url %q", normalizedURL)
	}
	visited[normalizedURL] = struct{}{}

	resource, err := p.fetchRemoteResource(ctx, cfg, normalizedURL)
	if err != nil {
		return "", "", nil, err
	}

	if !isRemoteStructuredDocument(resource.contentType, resource.data) {
		if shouldAttemptRemoteVideoTranscode(resource.finalURL, resource.contentType, declaredKind) {
			// ffmpeg 直连 URL 拉流前再校验一次宿主（首跳已校验，此处兜底）。
			if _, err := p.validateMediaURL(resource.finalURL); err != nil {
				return "", "", nil, err
			}
			nameHint := firstNonEmpty(
				fileNameHint,
				fileNameFromURL(resource.finalURL),
				fileNameFromURL(normalizedURL),
				"video.mp4",
			)
			filePath, contentType, cleanup, err := p.transcodeRemoteVideoToMP4(ctx, resource.finalURL, nameHint)
			if err != nil {
				return "", "", nil, fmt.Errorf(
					"wechat: remote video %q requires transcoding before upload, but transcoding failed: %w",
					resource.finalURL, err)
			}
			return filePath, contentType, cleanup, nil
		}
		if err := validateOutboundRemoteMediaType(resource.finalURL, resource.contentType, declaredKind); err != nil {
			return "", "", nil, err
		}
		nameHint := firstNonEmpty(
			fileNameHint,
			fileNameFromURL(resource.finalURL),
			fileNameFromURL(normalizedURL),
		)
		filePath, err := p.persistTempMedia(resource.data, resource.contentType, nameHint, "outbound")
		if err != nil {
			return "", "", nil, err
		}
		contentType := resource.contentType
		if contentType == "" {
			contentType = detectContentType(filePath, "")
		}
		cleanup := func() { _ = os.Remove(filePath) }
		return filePath, contentType, cleanup, nil
	}

	candidates := extractRemoteMediaCandidates(resource.data, resource.finalURL, declaredKind)
	if len(candidates) == 0 {
		return "", "", nil, fmt.Errorf(
			"wechat: remote media url %q resolved to a structured page but no downloadable media url was found",
			normalizedURL)
	}

	var lastErr error
	for _, candidate := range candidates {
		candidateKind := normalizeMediaKind(candidate.kind)
		if candidateKind == "" {
			candidateKind = declaredKind
		}
		filePath, contentType, cleanup, err := p.resolveOutboundRemoteMediaRecursive(
			ctx,
			cfg,
			candidate.url,
			firstNonEmpty(candidate.fileNameHint, fileNameHint),
			candidateKind,
			depth+1,
			visited,
		)
		if err == nil {
			return filePath, contentType, cleanup, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("wechat: unable to resolve remote media url %q", normalizedURL)
	}
	return "", "", nil, fmt.Errorf(
		"wechat: remote media url %q resolved to a structured page, but all extracted media candidates failed: %w",
		normalizedURL, lastErr)
}

// fetchRemoteResource 抓取一个远端资源（页面或媒体文件）：宿主校验（含重定向
// 逐跳校验）+ 体量上限。
func (p *Provider) fetchRemoteResource(ctx context.Context, cfg connectors.Config, rawURL string) (remoteFetchedResource, error) {
	if _, err := p.validateMediaURL(rawURL); err != nil {
		return remoteFetchedResource{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return remoteFetchedResource{}, fmt.Errorf("build remote media request: %w", err)
	}
	response, err := p.outboundMediaFetchClient().Do(request)
	if err != nil {
		return remoteFetchedResource{}, fmt.Errorf("download remote media %q: %w", rawURL, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return remoteFetchedResource{}, fmt.Errorf("download remote media returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	limit := p.mediaLimitFor(cfg)
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return remoteFetchedResource{}, fmt.Errorf("read remote media body: %w", err)
	}
	if int64(len(data)) > limit {
		return remoteFetchedResource{}, fmt.Errorf("remote media exceeds %d bytes", limit)
	}
	finalURL := strings.TrimSpace(rawURL)
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	return remoteFetchedResource{
		data:        data,
		contentType: strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type"))),
		finalURL:    finalURL,
	}, nil
}

// isRemoteStructuredDocument 判定抓取结果是否为「页面型文档」（HTML/XHTML，
// 或含媒体 URL 键的 JSON），这类文档需要提取候选媒体而不是直接上传。
func isRemoteStructuredDocument(contentType string, data []byte) bool {
	lowerContentType := strings.ToLower(strings.TrimSpace(contentType))
	snippet := strings.TrimSpace(string(data))
	if len(snippet) > 4096 {
		snippet = snippet[:4096]
	}
	lowerSnippet := strings.ToLower(snippet)

	switch {
	case strings.Contains(lowerContentType, "text/html"),
		strings.Contains(lowerContentType, "application/xhtml+xml"):
		return true
	case strings.Contains(lowerContentType, "application/json"):
		return remoteStructuredJSONURLMarker.MatchString(snippet)
	}
	if remoteHTMLDocTypePattern.MatchString(lowerSnippet) || remoteHTMLBodyMarkerPattern.MatchString(lowerSnippet) {
		return true
	}
	if strings.HasPrefix(lowerSnippet, "{") || strings.HasPrefix(lowerSnippet, "[") {
		return remoteStructuredJSONURLMarker.MatchString(snippet)
	}
	return false
}

// extractRemoteMediaCandidates 从页面内容里按「标签 → 内嵌 JSON → 裸直链」的
// 顺序提取候选媒体 URL，并按声明类型过滤（ported from 上游）。
func extractRemoteMediaCandidates(body []byte, pageURL string, declaredKind string) []remoteMediaCandidate {
	htmlText := string(body)
	normalizedDeclaredKind := normalizeMediaKind(declaredKind)
	candidates := make([]remoteMediaCandidate, 0, 8)
	seen := make(map[string]struct{})

	appendCandidate := func(rawURL string, candidateKind string, structured bool) {
		normalizedCandidateKind := normalizeMediaKind(candidateKind)
		for _, resolvedURL := range expandRemoteMediaCandidateURLs(pageURL, rawURL) {
			resolvedKind := normalizedCandidateKind
			if resolvedKind == "" {
				resolvedKind = inferMediaKindFromLocation(resolvedURL)
			}
			if !shouldUseRemoteMediaCandidate(normalizedDeclaredKind, resolvedKind, resolvedURL, structured) {
				continue
			}
			if _, ok := seen[resolvedURL]; ok {
				continue
			}
			seen[resolvedURL] = struct{}{}
			candidates = append(candidates, remoteMediaCandidate{
				url:          resolvedURL,
				kind:         resolvedKind,
				fileNameHint: fileNameFromURL(resolvedURL),
			})
		}
	}

	for _, match := range remoteHTMLTagPattern.FindAllStringSubmatch(htmlText, -1) {
		if len(match) < 2 {
			continue
		}
		tagName := strings.ToLower(strings.TrimSpace(match[1]))
		attributes := parseRemoteHTMLAttributes(match[0])

		switch tagName {
		case "meta":
			key := strings.ToLower(firstNonEmpty(
				attributes["property"],
				attributes["name"],
				attributes["itemprop"],
			))
			content := firstNonEmpty(attributes["content"], attributes["value"])
			switch {
			case isRemoteVideoMetaKey(key):
				appendCandidate(content, mediaKindVideo, true)
			case isRemoteImageMetaKey(key):
				appendCandidate(content, mediaKindImage, true)
			case isRemoteStructuredMediaKey(key):
				appendCandidate(content, normalizedDeclaredKind, true)
			}
		case "source", "video":
			appendCandidate(
				firstNonEmpty(attributes["src"], attributes["data-src"], attributes["href"]),
				normalizedDeclaredKind,
				true,
			)
		case "a":
			href := firstNonEmpty(attributes["href"], attributes["data-href"])
			if looksLikeDirectMediaURL(href) {
				appendCandidate(href, inferMediaKindFromLocation(href), false)
			}
		case "iframe":
			appendCandidate(
				firstNonEmpty(attributes["src"], attributes["data-src"], attributes["data-href"]),
				normalizedDeclaredKind,
				true,
			)
		}
	}

	for _, match := range remoteStructuredURLPattern.FindAllStringSubmatch(htmlText, -1) {
		if len(match) < 3 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(match[1]))
		value := decodeRemoteJSONLikeString(match[2])
		switch {
		case strings.Contains(key, "video"),
			strings.Contains(key, "stream"),
			strings.Contains(key, "playback"),
			strings.Contains(key, "manifest"):
			appendCandidate(value, mediaKindVideo, true)
		case strings.Contains(key, "contenturl"),
			strings.Contains(key, "player"),
			strings.Contains(key, "iframe"),
			strings.Contains(key, "embedurl"):
			appendCandidate(value, normalizedDeclaredKind, true)
		default:
			appendCandidate(value, normalizedDeclaredKind, true)
		}
	}

	for _, match := range remoteDirectMediaURLPattern.FindAllString(htmlText, -1) {
		appendCandidate(match, inferMediaKindFromLocation(match), false)
	}

	return candidates
}

func parseRemoteHTMLAttributes(tag string) map[string]string {
	matches := remoteHTMLAttributePattern.FindAllStringSubmatch(tag, -1)
	if len(matches) == 0 {
		return nil
	}
	attributes := make(map[string]string, len(matches))
	for _, match := range matches {
		if len(match) < 5 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(match[1]))
		value := firstNonEmpty(match[2], match[3], match[4])
		value = html.UnescapeString(strings.Trim(strings.TrimSpace(value), "\"'"))
		if key != "" && value != "" {
			attributes[key] = value
		}
	}
	return attributes
}

func decodeRemoteJSONLikeString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if decoded, err := strconv.Unquote(`"` + value + `"`); err == nil {
		value = decoded
	}
	return html.UnescapeString(strings.Trim(strings.TrimSpace(value), "\"'"))
}

// resolveRemoteMediaURL 把候选地址归一为绝对 URL：协议相对（//）、绝对地址
// 原样返回，其余相对页面地址解析。
func resolveRemoteMediaURL(baseURL string, rawURL string) string {
	rawURL = html.UnescapeString(strings.Trim(strings.TrimSpace(rawURL), "\"'"))
	if rawURL == "" {
		return ""
	}
	if strings.HasPrefix(rawURL, "//") {
		if base, err := url.Parse(strings.TrimSpace(baseURL)); err == nil && strings.TrimSpace(base.Scheme) != "" {
			return base.Scheme + ":" + rawURL
		}
		return "https:" + rawURL
	}
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.String()
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base == nil {
		return ""
	}
	ref, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// expandRemoteMediaCandidateURLs 返回候选地址的归一形态（当前仅一种；保留
// 扩展点：上游在此做了站点特化重写，本仓未移植）。
func expandRemoteMediaCandidateURLs(baseURL string, rawURL string) []string {
	resolvedURL := resolveRemoteMediaURL(baseURL, rawURL)
	if resolvedURL == "" {
		return nil
	}
	return []string{resolvedURL}
}

func shouldUseRemoteMediaCandidate(declaredKind string, candidateKind string, resolvedURL string, structured bool) bool {
	declaredKind = normalizeMediaKind(declaredKind)
	candidateKind = normalizeMediaKind(candidateKind)
	if candidateKind == "" {
		candidateKind = inferMediaKindFromLocation(resolvedURL)
	}
	switch declaredKind {
	case mediaKindVideo:
		return candidateKind == mediaKindVideo || structured
	case mediaKindImage:
		return candidateKind == mediaKindImage || structured
	case mediaKindFile, "":
		return true
	default:
		return candidateKind == declaredKind || structured
	}
}

func looksLikeDirectMediaURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return remoteDirectMediaURLPattern.MatchString(value)
}

func isRemoteVideoMetaKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "og:video",
		"og:video:url",
		"og:video:secure_url",
		"twitter:player:stream",
		"video",
		"video:url",
		"video:secure_url":
		return true
	default:
		return false
	}
}

func isRemoteImageMetaKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "og:image",
		"og:image:url",
		"og:image:secure_url",
		"twitter:image",
		"image",
		"image:url":
		return true
	default:
		return false
	}
}

func isRemoteStructuredMediaKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "contenturl", "embedurl", "url", "src":
		return true
	default:
		return false
	}
}

// shouldAttemptRemoteVideoTranscode 报告视频候选是否必须先转码才能上传：
// 声明为 video 且来源是流媒体清单（HLS/DASH 的 content-type 或 URL 扩展名）。
func shouldAttemptRemoteVideoTranscode(rawURL string, contentType string, declaredKind string) bool {
	if normalizeMediaKind(declaredKind) != mediaKindVideo {
		return false
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.Contains(contentType, "mpegurl"),
		strings.Contains(contentType, "dash+xml"),
		isStreamingPlaylistURL(rawURL):
		return true
	default:
		return false
	}
}

func isStreamingPlaylistURL(value string) bool {
	return hasMediaExtension(value, ".m3u8", ".mpd")
}
