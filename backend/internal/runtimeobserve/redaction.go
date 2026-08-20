package runtimeobserve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// 指纹域名（domain separation），避免不同数据域可比对。
const (
	FingerprintDomainPrompt        = "prompt"
	FingerprintDomainToolSurface   = "tool_surface"
	FingerprintDomainContent       = "content"
	FingerprintDomainRendererSource = "renderer_source"
)

// HMAC scheme 常量；指纹形如 hmac:v1:<keyVersion>:<hex>。
const hmacScheme = "v1"

// omittedFieldMarker 是敏感字段被替换后的占位标记，绝不保留原值。
const omittedFieldMarker = "omitted"

// sensitiveFieldTokens 是 deny-first 匹配的敏感 key 片段（大小写不敏感）。
// 只要 key 规范化后包含任一 token，该字段即被剔除。
var sensitiveFieldTokens = []string{
	"authorization",
	"api_key",
	"apikey",
	"api-key",
	"token",
	"secret",
	"password",
	"passwd",
	"cookie",
	"set-cookie",
	"private_key",
	"privatekey",
	"request_body",
	"response_body",
	"requestbody",
	"responsebody",
	"credential",
	"jwt",
	"access_key",
	"accesskey",
	"bearer",
	"session_key",
	"signature",
	"signing_key",
	"tls_cert",
	"ssh_key",
	// 内容类字段在观察平面默认不导出（方案 §1.5）；作为 deny-first 兜底，
	// redactor 对任意 payload 直接剔除，主要防线仍是 projector allowlist。
	"prompt",
	"system_instruction",
	"system_instructions",
	"tool_arguments",
	"tool_result",
	"reasoning",
	"assistant_output",
	"user_message",
	"provider_http_body",
	"response_body_text",
	"request_body_text",
}

// maxRedactDepth 限制递归深度，防止恶意深层结构击穿栈。
const maxRedactDepth = 16

// maxRedactKeyLen 限制单字段名长度，超出直接标记为隐藏。
const maxRedactKeyLen = 256

// Redactor 递归执行 deny-first 脱敏，并为低敏内容生成部署级 HMAC 指纹。
type Redactor struct {
	hmacKey    []byte
	keyVersion string
	profile    string
}

// NewRedactor 创建红actor。hmacKey 为空时指纹退化为不可用的伪值，
// 编译 still 安全，但 fingerprint 会有明确标记。
func NewRedactor(hmacKey []byte, keyVersion, profile string) *Redactor {
	if profile == "" {
		profile = RedactionProfileSafeDefault
	}
	return &Redactor{
		hmacKey:    append([]byte(nil), hmacKey...),
		keyVersion: keyVersion,
		profile:    profile,
	}
}

// Profile 返回当前 redaction profile 名。
func (r *Redactor) Profile() string {
	if r == nil {
		return RedactionProfileSafeDefault
	}
	return r.profile
}

// KeySet 返回是否配置了部署级指纹密钥。
func (r *Redactor) KeySet() bool {
	return r != nil && len(r.hmacKey) > 0
}

// KeyVersion 返回指纹密钥版本。
func (r *Redactor) KeyVersion() string {
	if r == nil {
		return ""
	}
	return r.keyVersion
}

// OmittedFields 返回 safe_default profile 所省略的代表性字段域。
func (r *Redactor) OmittedFields() []string {
	return []string{"prompt", "system_instruction", "tool_arguments", "tool_result", "provider_http_body", "reasoning", "authorization", "api_key"}
}

// HMACFingerprint 计算 domain-separated 的 HMAC-SHA256 指纹。
// 返回形如 "hmac:v1:<keyVersion>:<hex>"；无密钥时返回 "hmac:v1:unset:<sha256hex>"。
func (r *Redactor) HMACFingerprint(domain, data string) string {
	payload := domain + "\x00" + data
	key := r.hmacKey
	if len(key) == 0 {
		sum := sha256.Sum256([]byte(payload))
		return "hmac:" + hmacScheme + ":unset:" + hex.EncodeToString(sum[:8])
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	sum := mac.Sum(nil)
	return "hmac:" + hmacScheme + ":" + r.keyVersion + ":" + hex.EncodeToString(sum)
}

// Redact 递归执行 deny-first 脱敏，返回从输入值安全投影出的结构。
// 返回值为 nil 时代表输入被整体隐藏。
func (r *Redactor) Redact(value interface{}) interface{} {
	if r == nil {
		return value
	}
	return redactValue(value, 0)
}

// RedactMap 脱敏一层 map，并返回被隐藏的字段名列表。
func (r *Redactor) RedactMap(input map[string]interface{}) (map[string]interface{}, []string) {
	omitted := []string{}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		if isSensitiveKey(key) {
			omitted = append(omitted, key)
			continue
		}
		if value != nil {
			out[key] = r.Redact(value)
		} else {
			out[key] = nil
		}
	}
	sort.Strings(omitted)
	return out, omitted
}

// redactValue 是内部递归实现。
func redactValue(value interface{}, depth int) interface{} {
	if depth > maxRedactDepth {
		return omittedFieldMarker
	}
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return boundUTF8String(typed, maxRedactedStringBytes)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, val := range typed {
			if isSensitiveKey(key) {
				continue
			}
			if val != nil {
				out[key] = redactValue(val, depth+1)
			} else {
				out[key] = nil
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			if item != nil {
				out = append(out, redactValue(item, depth+1))
			}
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, val := range typed {
			if isSensitiveKey(key) {
				continue
			}
			out[key] = boundUTF8String(val, maxRedactedStringBytes)
		}
		return out
	default:
		// 数字/布尔/时间等标量由 JSON 编码范围保证不携带内容；直接保留。
		return value
	}
}

const maxRedactedStringBytes = 4 * 1024

// boundUTF8String 截断字符串并做 UTF-8 安全剪裁，避免把半字节吐出。
func boundUTF8String(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// isSensitiveKey 判断字段名是否命中 deny-first 规则。
func isSensitiveKey(key string) bool {
	if len(key) == 0 || len(key) > maxRedactKeyLen {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	for _, token := range sensitiveFieldTokens {
		token = strings.ReplaceAll(token, "-", "_")
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

// ScrubURL 只保留受控逻辑信息：scheme + host；丢弃 query、fragment、凭证、
// 完整 API path。解析失败返回空字符串。
func ScrubURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	port := u.Port()
	if port != "" {
		host = host + ":" + port
	}
	switch {
	case u.Scheme == "":
		// 相对/裸路径：不含可信边界信息，返回逻辑 host 或空。
		if host != "" {
			return host
		}
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

// allowedHeaderNames 是允许保留 presence 的低敏 header 名（仅用于判断是否存在）。
var allowedHeaderNames = map[string]bool{
	"content-type":        true,
	"accept":              true,
	"content-length":      true,
	"x-request-id":        true,
	"x-request-id-header": true,
}

// HeaderPresence 返回给定 HTTP header 集合的低敏 presence 摘要。
// 返回的 map 只会包含 named field -> "present"，不返回值。
func HeaderPresence(headers map[string]string) map[string]interface{} {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(headers))
	seen := map[string]bool{}
	for name := range headers {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "" || seen[lower] {
			continue
		}
		seen[lower] = true
		if allowedHeaderNames[lower] {
			out[lower] = "present"
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
