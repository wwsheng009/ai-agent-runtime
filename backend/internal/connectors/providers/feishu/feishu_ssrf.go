// 出站远端媒体 SSRF 防护（docs/bots/08 §7.3）。
//
// 出站媒体允许「远端 URL 直传」（先由 runtime 下载再上传飞书），必须防止
// agent 诱导 runtime 访问内网/本机地址：
//   - 仅接受 http(s)；
//   - 默认宿主校验 validatePublicMediaHost：解析域名并要求所有地址均为公网
//     单播（拒绝回环/私网/链路本地/未指定/组播/CGNAT）；
//   - 重定向逐跳复用同一校验（CheckRedirect），防止 302 绕过首跳校验。
//
// 与 wechat（validatePublicMediaHost）/qqbot（defaultMediaHostGuard）同口径。
package feishu

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// mediaRedirectLimit 是出站远端媒体下载的最大重定向次数。
const mediaRedirectLimit = 5

// mediaHostPolicyOrDefault 返回生效的宿主校验策略（nil 回退默认公网校验）。
func (p *Provider) mediaHostPolicyOrDefault() func(host string) error {
	if p.mediaHostPolicy != nil {
		return p.mediaHostPolicy
	}
	return validatePublicMediaHost
}

// validateMediaURL 校验出站远端媒体 URL：仅 http(s) + 宿主策略（首跳）。
func (p *Provider) validateMediaURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("feishu: media url is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("feishu: invalid media url %q: %w", raw, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("feishu: media url scheme must be http(s), got %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return nil, fmt.Errorf("feishu: media url host is required: %q", raw)
	}
	policy := p.mediaHostPolicyOrDefault()
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
		if len(via) >= mediaRedirectLimit {
			return errors.New("feishu: too many redirects while fetching media")
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
		return errors.New("feishu: media url host is required")
	}
	addresses, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("feishu: resolve media host %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("feishu: media host %q resolved to no addresses", host)
	}
	for _, address := range addresses {
		if isDisallowedMediaIP(address) {
			return fmt.Errorf("feishu: media host %q resolves to disallowed address %s", host, address)
		}
	}
	return nil
}

// isDisallowedMediaIP 报告地址是否属于禁止访问的类别（SSRF 防护）。
func isDisallowedMediaIP(address net.IP) bool {
	if address == nil {
		return true
	}
	if address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast() {
		return true
	}
	// CGNAT 100.64.0.0/10（运营商级 NAT，非公网；Go 的 IsPrivate 不含该段）。
	if ipv4 := address.To4(); ipv4 != nil && ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
		return true
	}
	return false
}
