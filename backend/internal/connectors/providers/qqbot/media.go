package qqbot

// 入站媒体下载（connectors.MediaFetcher）：
//   - 官方 attachments.url 是带签名的直链（https），下载为只读 GET；
//   - 出口安全：默认拒绝回环/私网/链路本地/未指定/组播/CGNAT 目标
//     （重定向逐跳校验，最多 mediaRedirectLimit 跳）；测试/内网部署可用
//     WithMediaHostGuard 覆盖；
//   - 体量上限：maxInboundMediaBytes（默认 100MB，可用 WithInboundMediaLimit
//     覆盖，测试用）；超限拒绝并保留元数据；
//   - 落盘：系统临时目录（文件名净化 + kind 兜底扩展名），入站文件保留供
//     agent 读取；过期目录按 24h 窗口低频清扫（每 64 次下载触发一次，
//     best-effort，与微信媒体实现同口径）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// maxInboundMediaBytes 是单条入站媒体的默认下载上限（官方附件硬限
	// 200MB；与微信媒体实现同取 100MB 作为本仓统一上限）。
	maxInboundMediaBytes = 100 << 20
	// mediaDownloadTimeout 是单次下载的 HTTP 超时（Service 侧另有 60s ctx）。
	mediaDownloadTimeout = 60 * time.Second
	// mediaRedirectLimit 是允许的最大重定向跳数。
	mediaRedirectLimit = 5
	// mediaTempDirPrefix 是入站媒体临时目录名前缀（os.MkdirTemp 使用）。
	mediaTempDirPrefix = "qqbot-media-"
	// mediaTempRetention 是入站媒体临时目录的清理窗口（与微信同口径）。
	mediaTempRetention = 24 * time.Hour
	// mediaSweepEvery 是「每 N 次下载触发一次清扫」的节流（避免每次下载都扫描）。
	mediaSweepEvery = 64
)

// mediaSweepCounter 是临时目录清扫的节流计数（进程内，跨连接器共享）。
var mediaSweepCounter atomic.Uint64

// mediaHostGuardFunc 校验单个主机名是否允许作为媒体目标（nil = 默认公网守卫）。
type mediaHostGuardFunc func(host string) error

// FetchInboundMedia 实现 connectors.MediaFetcher：下载 media.URL 并落盘。
func (p *Provider) FetchInboundMedia(ctx context.Context, cfg connectors.Config, media connectors.Media) (string, error) {
	raw := strings.TrimSpace(media.URL)
	if raw == "" {
		return "", errors.New("qqbot: media url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("qqbot: invalid media url %q", raw)
	}
	guard := p.mediaHostGuard
	if guard == nil {
		guard = defaultMediaHostGuard
	}
	if err := guard(parsed.Hostname()); err != nil {
		return "", err
	}

	client := p.mediaDownloadClient(guard)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", fmt.Errorf("qqbot: build media request failed: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("qqbot: download media failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("qqbot: media download returned %s", response.Status)
	}
	limit := p.mediaTransferLimit()
	if response.ContentLength > limit {
		return "", fmt.Errorf("qqbot: media too large (%d bytes > %d)", response.ContentLength, limit)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("qqbot: read media body failed: %w", err)
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("qqbot: media too large (> %d bytes)", limit)
	}

	p.maybeSweepMediaTempDirs()
	dir, err := os.MkdirTemp("", mediaTempDirPrefix+"*")
	if err != nil {
		return "", fmt.Errorf("qqbot: create media dir failed: %w", err)
	}
	path := filepath.Join(dir, sanitizeMediaFileName(media.FileName, media.Kind, raw))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("qqbot: write media file failed: %w", err)
	}
	return path, nil
}

// maybeSweepMediaTempDirs 低频清扫过期的入站媒体临时目录：每次下载落盘前按
// 节流触发，删除超过 mediaTempRetention 未更新的 qqbot-media-* 目录；
// best-effort（失败只跳过，不影响下载）。
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

// mediaTransferLimit 返回生效的单条媒体体量上限（入站下载 / 出站读取共用；
// 0/负值回退默认 maxInboundMediaBytes）。
func (p *Provider) mediaTransferLimit() int64 {
	if p.mediaLimitBytes > 0 {
		return p.mediaLimitBytes
	}
	return maxInboundMediaBytes
}

// mediaDownloadClient 基于基础客户端派生下载客户端：超时收紧 +
// CheckRedirect 逐跳执行出口守卫。
func (p *Provider) mediaDownloadClient(guard mediaHostGuardFunc) *http.Client {
	client := *p.client
	client.Timeout = mediaDownloadTimeout
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= mediaRedirectLimit {
			return errors.New("qqbot: too many media redirects")
		}
		return guard(req.URL.Hostname())
	}
	return &client
}

// defaultMediaHostGuard 拒绝非公网目标：回环/私网/链路本地/未指定/组播/CGNAT。
func defaultMediaHostGuard(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("qqbot: media host is empty")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("qqbot: resolve media host %q failed: %w", host, err)
	}
	for _, ip := range ips {
		if !isPublicMediaIP(ip) {
			return fmt.Errorf("qqbot: media host %q resolves to non-public address %s", host, ip)
		}
	}
	return nil
}

// isPublicMediaIP 报告地址是否可作为公网媒体目标。
func isPublicMediaIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	// CGNAT 100.64.0.0/10（运营商级 NAT，非公网）。
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

// mediaFileNameUnsafe 是文件名净化字符集（保留字母数字与 . _ -）。
var mediaFileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// sanitizeMediaFileName 净化落盘文件名：去除目录成分与危险字符，空名按
// 媒体类型兜底扩展名，超长截断。
func sanitizeMediaFileName(name, kind, rawURL string) string {
	base := strings.TrimSpace(name)
	if base != "" {
		base = filepath.Base(base)
	}
	if base == "" || base == "." {
		if parsed, err := url.Parse(rawURL); err == nil {
			if pathBase := filepath.Base(parsed.Path); pathBase != "" && pathBase != "." && pathBase != "/" {
				base = pathBase
			}
		}
	}
	base = mediaFileNameUnsafe.ReplaceAllString(base, "_")
	base = strings.Trim(base, "._")
	if base == "" {
		base = "qqbot-media" + extensionForMediaKind(kind)
	}
	if len(base) > 120 {
		base = base[:120]
	}
	return base
}

// extensionForMediaKind 按媒体类别给出兜底扩展名。
func extensionForMediaKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "image":
		return ".jpg"
	case "voice":
		return ".wav"
	case "video":
		return ".mp4"
	default:
		return ".bin"
	}
}
