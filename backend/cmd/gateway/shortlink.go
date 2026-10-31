package main

// shortlink.go — 通知正文里的会话深链短址（GET /s/{code} → 302 控制台深链）。
//
// 动机（2026-10-31 真机）：IM 通知要点进控制台会话，必须带完整深链
//
//	https://aicli.ttai.online/#sessions?node=node-9df82a73f680352a&session=session_20261030153213_PhTLvD97
//
// 这串本身就有 ~95 字符，而正文预算同时受 provider 上限（默认 2000）与
// 「已完成」附带内容上限（默认 200）挤压——长链把正文挤没了。短址把它压到
// ~35 字符（https://aicli.ttai.online/s/ABCDEFGHIJ）。
//
// 形态与边界：
//   - 短码 = base32(HMAC-SHA256(key, "v1\nnode\nsession"))[:10]，**确定性**：
//     同一会话永远得到同一个码（通知重复投递不会堆积条目），也不暴露自增序号，
//     无法按序枚举猜测；
//   - 映射落 <data-dir>/short-links.json（0600，含随机 key）：重启后旧链接仍可解析；
//   - /s/{code} 是 **public** 端点（手机/微信点开时没有 owner cookie）：命中即 302。
//     控制台自身仍要求登录，因此短链没有放宽权限——拿到链接的人看到的仍旧是
//     node/session 标识，与旧行为（把完整深链发到 IM）等价；短码不可枚举让
//     「批量猜别人的会话」不可行；
//   - 条目上限 shortLinkMaxLinks，超出淘汰最旧：确定性的码意味着老链接可能失效，
//     这是容量与「短链永久有效」之间的取舍（文档写明）。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

const (
	// shortLinkVersion 是落盘格式版本。
	shortLinkVersion = 1
	// shortLinkCodeLen 是短码长度（base32，10 字符 ≈ 50 bit，足够抗枚举）。
	shortLinkCodeLen = 10
	// shortLinkMaxLinks 是映射条目上限（超出淘汰最旧）。
	shortLinkMaxLinks = 4096
	// shortLinkKeyBytes 是 HMAC 密钥长度。
	shortLinkKeyBytes = 32
	// shortLinkPathPrefix 是短址路径前缀。
	shortLinkPathPrefix = "/s/"
)

type shortLinkEntry struct {
	NodeID    string `json:"node_id"`
	SessionID string `json:"session_id"`
	CreatedAt string `json:"created_at,omitempty"`
}

type shortLinkFile struct {
	Version int                       `json:"version,omitempty"`
	Key     string                    `json:"key"`
	Links   map[string]shortLinkEntry `json:"links"`
}

// shortLinker 是进程内的短址表（落盘 JSON + 确定性短码）。
type shortLinker struct {
	mu      sync.Mutex
	path    string
	baseURL string
	key     []byte
	links   map[string]shortLinkEntry
}

// newShortLinker 构造并装载短址表；baseURL 为空时仍可用（但 URL() 返回空，
// 调用方回退到直链）。密钥缺失/损坏时重新生成并落盘。
func newShortLinker(path, baseURL string) *shortLinker {
	s := &shortLinker{
		path:    strings.TrimSpace(path),
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		links:   make(map[string]shortLinkEntry),
	}
	if s.path == "" {
		return s
	}
	s.load()
	if len(s.key) != shortLinkKeyBytes {
		if err := s.rotateKeyLocked(); err != nil {
			log.Printf("shortlink: rotate key: %v", err)
		}
	}
	return s
}

func (s *shortLinker) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("shortlink: read %s: %v", s.path, err)
		}
		return
	}
	var file shortLinkFile
	if err := json.Unmarshal(raw, &file); err != nil {
		log.Printf("shortlink: parse %s: %v", s.path, err)
		return
	}
	if key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(file.Key)); err == nil {
		s.key = key
	}
	for code, entry := range file.Links {
		if strings.TrimSpace(code) == "" || strings.TrimSpace(entry.SessionID) == "" {
			continue
		}
		s.links[code] = entry
	}
}

// rotateKeyLocked 生成新密钥并落盘（新码与旧码不同：旧链接失效，日志可见）。
func (s *shortLinker) rotateKeyLocked() error {
	key := make([]byte, shortLinkKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	s.key = key
	return s.persistLocked()
}

func (s *shortLinker) persistLocked() error {
	if s == nil || s.path == "" {
		return nil
	}
	file := shortLinkFile{
		Version: shortLinkVersion,
		Key:     base64.StdEncoding.EncodeToString(s.key),
		Links:   s.links,
	}
	raw, err := json.Marshal(file)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Printf("shortlink: mkdir %s: %v", dir, err)
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("shortlink: write %s: %v", tmp, err)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		// Windows 不允许 rename 覆盖：先删目标再重试（与 notify-im 同口径）。
		_ = os.Remove(s.path)
		if err2 := os.Rename(tmp, s.path); err2 != nil {
			_ = os.Remove(tmp)
			log.Printf("shortlink: rename %s: %v", s.path, err2)
			return err2
		}
	}
	return nil
}

// codeLocked 计算确定性短码；长度按需延长以避开哈希碰撞（极罕见）。
func (s *shortLinker) codeLocked(nodeID, sessionID string) string {
	for length := shortLinkCodeLen; length <= 32; length += 2 {
		mac := hmac.New(sha256.New, s.key)
		mac.Write([]byte("v1\n" + strings.TrimSpace(nodeID) + "\n" + strings.TrimSpace(sessionID)))
		sum := mac.Sum(nil)
		code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum)
		code = code[:length]
		if existing, ok := s.links[code]; !ok ||
			(existing.NodeID == strings.TrimSpace(nodeID) && existing.SessionID == strings.TrimSpace(sessionID)) {
			return code
		}
	}
	return ""
}

// Code 返回该会话的短码，并按需登记映射（幂等：同会话不会新增条目）。
func (s *shortLinker) Code(nodeID, sessionID string) string {
	if s == nil {
		return ""
	}
	nodeID = strings.TrimSpace(nodeID)
	sessionID = strings.TrimSpace(sessionID)
	if s.key == nil || sessionID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	code := s.codeLocked(nodeID, sessionID)
	if code == "" {
		return ""
	}
	if _, ok := s.links[code]; !ok {
		s.evictLocked()
		s.links[code] = shortLinkEntry{
			NodeID:    nodeID,
			SessionID: sessionID,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := s.persistLocked(); err != nil {
			log.Printf("shortlink: persist new link %s: %v", code, err)
		}
	}
	return code
}

// evictLocked 在超出上限时淘汰最旧条目（保持表有界）。
func (s *shortLinker) evictLocked() {
	if len(s.links) < shortLinkMaxLinks {
		return
	}
	type item struct {
		code string
		at   string
	}
	items := make([]item, 0, len(s.links))
	for code, entry := range s.links {
		items = append(items, item{code: code, at: entry.CreatedAt})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at < items[j].at })
	drop := len(items) - shortLinkMaxLinks + 1
	for i := 0; i < drop && i < len(items); i++ {
		delete(s.links, items[i].code)
	}
	log.Printf("shortlink: evicted %d oldest link(s) (cap %d)", drop, shortLinkMaxLinks)
}

// URL 返回可直接放进通知正文的短链；baseURL 为空时返回 ""（调用方回退直链）。
func (s *shortLinker) URL(nodeID, sessionID string) string {
	if s == nil || s.baseURL == "" {
		return ""
	}
	code := s.Code(nodeID, sessionID)
	if code == "" {
		return ""
	}
	return s.baseURL + shortLinkPathPrefix + code
}

// Resolve 解析短码 → (nodeID, sessionID)：命中返回 ok=true。
func (s *shortLinker) Resolve(code string) (string, string, bool) {
	if s == nil {
		return "", "", false
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.links[code]
	if !ok {
		return "", "", false
	}
	return entry.NodeID, entry.SessionID, true
}

// Handler 是 GET /s/{code}：命中即 302 到控制台会话深链，否则 404。
// 该端点**不**走 owner 鉴权（手机/微信点开时没有 cookie）；控制台自身仍要求登录。
func (s *shortLinker) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		code := strings.TrimSpace(mux.Vars(r)["code"])
		nodeID, sessionID, ok := s.Resolve(code)
		if !ok {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "short link not found", http.StatusNotFound)
			return
		}
		target := consoleSessionURL(s.baseURL, nodeID, sessionID)
		if target == "" {
			http.Error(w, "gateway base url is not configured", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	}
}