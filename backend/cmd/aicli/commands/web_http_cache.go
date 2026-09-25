package commands

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Web API 传输层通用优化（gzip 协商 / ETag+304 / Server-Timing / 微缓存单飞）。
//
// 动机：/web/api/screen|sessions|status|statusbar 是 SSE 驱动的高频端点
// （前端秒级、事件级刷新，多 tab 并存）。此前这些端点既无压缩也无条件请求：
// 每次刷新都要完整序列化 + 传输同一份大响应（screen 实测单次 331KB），
// 多 tab / 多事件的重复请求还会在服务端各自重建一次。
//
// 这里的四件事各自独立、可单独关闭：
//  1. gzip：Accept-Encoding 协商，体积阈值以下不压缩（协商开销 > 收益）；
//  2. ETag/304：弱 ETag；命中 If-None-Match 时不写响应体（调用方可以据此
//     跳过昂贵的构建步骤，见各 handler 的注释）；
//  3. Server-Timing：把 db/render/encode 分量暴露到浏览器 DevTools 与
//     curl -w，便于继续定位瓶颈（项目已有 --pprof，这里是端点内的定点观测）；
//  4. 微缓存 + 单飞：TTL 窗口内相同 key 的并发请求只构建一次，其余复用。
//     只能用于「重复请求返回同一份内容」的只读列表端点，且 TTL 必须远小于
//     用户可感知的刷新周期。
// ---------------------------------------------------------------------------

const (
	// webHTTPGzipMinBytes 小于该体积的响应不做 gzip：小体量下协商与压缩开销
	// 大于传输收益。
	webHTTPGzipMinBytes = 1024

	// webHTTPResponseCacheTTL 是只读列表端点的进程内微缓存 TTL。取值依据：
	// 前端侧栏事件刷新节流是 200ms（MESH_REFRESH_THROTTLE_MS），多 tab / 多
	// 事件窗口合并用 500ms 覆盖一个刷新周期，同时保证窗口外的请求重建。
	webHTTPResponseCacheTTL = 500 * time.Millisecond

	// webHTTPServerTimingHeader 是 Server-Timing 的响应头名。
	webHTTPServerTimingHeader = "Server-Timing"
)

// webHTTPTiming 是 Server-Timing 的一个分量（名称 + 耗时）。
type webHTTPTiming struct {
	Name string
	Dur  time.Duration
}

// webHTTPServerTiming 写入 Server-Timing 头。已存在时追加而不是覆盖，便于
// 中间层与本层各自上报。名为空/耗时非正的分量忽略（0 值不该出现在上报里）。
func webHTTPServerTiming(w http.ResponseWriter, timings ...webHTTPTiming) {
	if w == nil || len(timings) == 0 {
		return
	}
	parts := make([]string, 0, len(timings))
	for _, timing := range timings {
		name := strings.TrimSpace(timing.Name)
		if name == "" {
			continue
		}
		// 负值（时钟回拨）按 0 处理；0 也照样上报——「分量存在但极快」是有用
		// 信息，因亚微秒耗时把它整条丢掉会让响应头出现与否依赖机器速度。
		dur := timing.Dur
		if dur < 0 {
			dur = 0
		}
		parts = append(parts, fmt.Sprintf("%s;dur=%.3f", name, float64(dur.Microseconds())/1000))
	}
	if len(parts) == 0 {
		return
	}
	existing := strings.TrimSpace(w.Header().Get(webHTTPServerTimingHeader))
	if existing != "" {
		parts = append([]string{existing}, parts...)
	}
	w.Header().Set(webHTTPServerTimingHeader, strings.Join(parts, ", "))
}

// webHTTPGzipAccepted 报告客户端是否接受 gzip（q=0 视为拒绝）。
func webHTTPGzipAccepted(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, value := range r.Header.Values("Accept-Encoding") {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			encoding := item
			quality := ""
			if idx := strings.Index(item, ";"); idx >= 0 {
				encoding = strings.TrimSpace(item[:idx])
				quality = strings.ToLower(strings.TrimSpace(item[idx+1:]))
			}
			if !strings.EqualFold(encoding, "gzip") {
				continue
			}
			if strings.HasPrefix(quality, "q=0") {
				// q=0.xxx 是正向质量值（0.5 仍接受）；仅 q=0 与 q=0.0* 表示拒绝。
				trimmed := strings.TrimPrefix(quality, "q=")
				trimmed = strings.TrimPrefix(trimmed, "0")
				trimmed = strings.TrimLeft(trimmed, ".")
				if strings.Trim(trimmed, "0") == "" {
					return false
				}
				return true
			}
			return true
		}
	}
	return false
}

// webHTTPWeakETag 由分量计算弱 ETag（sha256 前 8 字节 hex）。分量之间用 NUL
// 分隔，避免 ("ab","c") 与 ("a","bc") 撞成同一个值。
func webHTTPWeakETag(parts ...string) string {
	hasher := sha256.New()
	for _, part := range parts {
		_, _ = hasher.Write([]byte(part))
		_, _ = hasher.Write([]byte{0})
	}
	sum := hasher.Sum(nil)
	return `W/"` + hex.EncodeToString(sum[:8]) + `"`
}

// webHTTPETagMatches 判断请求的 If-None-Match 是否命中给定 ETag。支持
// 逗号分隔列表、W/ 前缀（弱比较）与通配 *。
func webHTTPETagMatches(r *http.Request, etag string) bool {
	if r == nil || strings.TrimSpace(etag) == "" {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if header == "" {
		return false
	}
	target := strings.TrimPrefix(strings.TrimSpace(etag), "W/")
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == target {
			return true
		}
	}
	return false
}

// webHTTPWriteNotModified 写 304：只带 ETag 与缓存指令，不带响应体。
func webHTTPWriteNotModified(w http.ResponseWriter, etag string) {
	if w == nil {
		return
	}
	if strings.TrimSpace(etag) != "" {
		w.Header().Set("ETag", etag)
	}
	// 必须重新验证：304 只表示「本次没变」，客户端不得据此长期免请求。
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept-Encoding")
	w.WriteHeader(http.StatusNotModified)
}

// webHTTPWriteBody 按协商写响应体（可选 gzip）并带上 ETag。
//
// 调用方负责「条件请求短路」：命中 If-None-Match 时用 webHTTPWriteNotModified
// 提前返回，可以跳过响应体构建（本函数不代替该判断，因为构建成本往往才是
// 需要跳过的部分）。
func webHTTPWriteBody(w http.ResponseWriter, r *http.Request, contentType string, body []byte, etag string) {
	if w == nil {
		return
	}
	if strings.TrimSpace(contentType) != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if strings.TrimSpace(etag) != "" {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Vary", "Accept-Encoding")
	if !webHTTPGzipAccepted(r) || len(body) < webHTTPGzipMinBytes {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
		return
	}
	// 先压缩到内存再写：这些端点的响应本来就在内存里构建，避免为未知长度
	// 打开 chunked 流式压缩（保持 Content-Length 可预测）。
	var builder strings.Builder
	gz := gzip.NewWriter(&builder)
	if _, err := gz.Write(body); err != nil {
		_ = gz.Close()
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
		return
	}
	if err := gz.Close(); err != nil {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
		return
	}
	compressed := []byte(builder.String())
	if len(compressed) >= len(body) {
		// 已压缩内容占优不明显时回退明文（小响应/高熵内容）。
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
	_, _ = w.Write(compressed)
}

// webHTTPResponseCache 是只读端点的进程内微缓存 + 单飞。
//
// 语义：get(key) 在 TTL 内直接复用上次构建的字节；TTL 外重新构建，但同一
// key 的并发请求共享同一次构建（后到者等待先到者完成，不重复计算）。
// 只存最近的若干 key（列表端点 key 数量 = 用户/过滤/会话组合，天然很小）。
type webHTTPResponseCache struct {
	mu       sync.Mutex
	entries  map[string]webHTTPResponseCacheEntry
	inflight map[string]*webHTTPResponseCacheCall
	ttl      time.Duration
	now      func() time.Time
	limit    int
}

type webHTTPResponseCacheEntry struct {
	body []byte
	at   time.Time
}

type webHTTPResponseCacheCall struct {
	done chan struct{}
	body []byte
	err  error
}

func newWebHTTPResponseCache(ttl time.Duration) *webHTTPResponseCache {
	if ttl <= 0 {
		ttl = webHTTPResponseCacheTTL
	}
	return &webHTTPResponseCache{
		entries:  map[string]webHTTPResponseCacheEntry{},
		inflight: map[string]*webHTTPResponseCacheCall{},
		ttl:      ttl,
		now:      time.Now,
		limit:    32,
	}
}

// get 返回 key 对应的响应字节。cached=true 表示命中 TTL 内缓存或与并发请求
// 共享了同一次构建（调用方可据此区分「本请求是否真的做了构建」）。
func (c *webHTTPResponseCache) get(key string, build func() ([]byte, error)) ([]byte, bool, error) {
	if c == nil || strings.TrimSpace(key) == "" {
		if build == nil {
			return nil, false, fmt.Errorf("web response cache: nil build")
		}
		body, err := build()
		return body, false, err
	}
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && c.now().Sub(entry.at) < c.ttl {
		body := entry.body
		c.mu.Unlock()
		return body, true, nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.body, true, call.err
	}
	call := &webHTTPResponseCacheCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	body, err := build()

	c.mu.Lock()
	delete(c.inflight, key)
	if err == nil {
		if len(c.entries) >= c.limit {
			c.evictOldestLocked()
		}
		c.entries[key] = webHTTPResponseCacheEntry{body: body, at: c.now()}
	}
	c.mu.Unlock()

	call.body = body
	call.err = err
	close(call.done)
	return body, false, err
}

// invalidate 丢弃全部缓存（会话切换 / 新建 / 删除等写路径调用）。
func (c *webHTTPResponseCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = map[string]webHTTPResponseCacheEntry{}
	c.mu.Unlock()
}

func (c *webHTTPResponseCache) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range c.entries {
		if oldestKey == "" || entry.at.Before(oldest) {
			oldestKey, oldest = key, entry.at
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// webAPIJSONContentType 是 Web API 的 JSON 响应 Content-Type（与 writeWebAPIJSON 一致）。
const webAPIJSONContentType = "application/json; charset=utf-8"

// marshalWebAPIJSON 序列化 JSON 响应体，保留 json.Encoder 的尾随换行以维持
// 既有响应体逐字节兼容。
func marshalWebAPIJSON(data any) ([]byte, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// writeWebAPIJSONBody 与 writeWebAPIJSON 同形，但走传输层协商（gzip + 可选 ETag）。
// etag 为空时只做压缩；命中 If-None-Match 时写 304（调用方若想跳过构建，应
// 更早地用 webHTTPETagMatches + webHTTPWriteNotModified 短路）。
func writeWebAPIJSONBody(w http.ResponseWriter, r *http.Request, status int, data any, etag string) {
	if status == http.StatusNotModified {
		webHTTPWriteNotModified(w, etag)
		return
	}
	body, err := marshalWebAPIJSON(data)
	if err != nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "error",
			"reason": err.Error(),
		})
		return
	}
	if strings.TrimSpace(etag) != "" && webHTTPETagMatches(r, etag) {
		webHTTPWriteNotModified(w, etag)
		return
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	webHTTPWriteBody(w, r, webAPIJSONContentType, body, etag)
}

// writeWebAPITextBody 写纯文本响应（gzip 协商）。
func writeWebAPITextBody(w http.ResponseWriter, r *http.Request, body []byte) {
	webHTTPWriteBody(w, r, "text/plain; charset=utf-8", body, "")
}

// webHTTPBodyETag 由响应体内容（内容寻址）与附加分量计算弱 ETag ：只有响应体
// 逐字节不同才会换 ETag ，因此不存在「指纹漏了某个输入导致错误 304」的可能。
// 代价是必须先构建响应体，只省下序列化后的传输 / 编码成本。
func webHTTPBodyETag(body []byte, parts ...string) string {
	hasher := sha256.New()
	_, _ = hasher.Write(body)
	for _, part := range parts {
		_, _ = hasher.Write([]byte(part))
		_, _ = hasher.Write([]byte{0})
	}
	sum := hasher.Sum(nil)
	return `W/"` + hex.EncodeToString(sum[:8]) + `"`
}
