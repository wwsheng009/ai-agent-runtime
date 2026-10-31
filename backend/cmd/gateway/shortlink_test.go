package main

// shortlink_test.go — 通知短址（docs/gateway/08 §5.2）。
//
// 真机动机（2026-10-31）：完整会话深链有 ~95 字符，把 IM 正文预算挤掉大半；
// 短址把它压到 ~35 字符，同一会话必须永远得到同一个码（否则重复通知会堆条目）。

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestShortLinkCodeDeterministicAndStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short-links.json")
	s := newShortLinker(path, "https://aicli.ttai.online")

	url1 := s.URL("node-a", "session-1")
	url2 := s.URL("node-a", "session-1")
	if url1 == "" || url1 != url2 {
		t.Fatalf("同一会话必须得到稳定短链：%q vs %q", url1, url2)
	}
	if !strings.HasPrefix(url1, "https://aicli.ttai.online"+shortLinkPathPrefix) {
		t.Fatalf("短链形式不对：%q", url1)
	}
	code := strings.TrimPrefix(url1, "https://aicli.ttai.online"+shortLinkPathPrefix)
	if len(code) != shortLinkCodeLen {
		t.Fatalf("短码长度 = %d, want %d", len(code), shortLinkCodeLen)
	}
	for _, r := range code {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", r) {
			t.Fatalf("短码含非 base32 字符 %q：%s", r, code)
		}
	}

	if other := s.URL("node-a", "session-2"); other == url1 {
		t.Fatalf("不同会话不应共用短码：%q", other)
	}

	// 重启（新实例读同一文件）后旧码必须仍可解析，且新码与旧码一致。
	restarted := newShortLinker(path, "https://aicli.ttai.online")
	if got := restarted.URL("node-a", "session-1"); got != url1 {
		t.Fatalf("重启后短码应保持不变：%q vs %q", got, url1)
	}
	nodeID, sessionID, ok := restarted.Resolve(code)
	if !ok || nodeID != "node-a" || sessionID != "session-1" {
		t.Fatalf("重启后解析失败：%v %q %q", ok, nodeID, sessionID)
	}

	// 幂等：重复取码不新增条目。
	_ = restarted.URL("node-a", "session-1")
	links := 0
	restarted.mu.Lock()
	links = len(restarted.links)
	restarted.mu.Unlock()
	if links != 2 {
		t.Fatalf("条目数 = %d, want 2（同会话不重复登记）", links)
	}
}

func TestShortLinkRedirectAndNotFound(t *testing.T) {
	dir := t.TempDir()
	s := newShortLinker(filepath.Join(dir, "short-links.json"), "https://aicli.ttai.online")
	code := s.Code("node-a", "session-1")
	if code == "" {
		t.Fatal("短码为空")
	}

	router := mux.NewRouter()
	router.Handle("/s/{code}", s.Handler()).Methods(http.MethodGet, http.MethodHead)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s/"+code, nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("命中应 302，got %d", rec.Code)
	}
	want := consoleSessionURL("https://aicli.ttai.online", "node-a", "session-1")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s/UNKNOWNXYZ", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知短码应 404，got %d", rec.Code)
	}
}

func TestSessionURLPrefersShortLinkAndFallsBack(t *testing.T) {
	full := "https://aicli.ttai.online/#sessions?node=node-a&session=session-1"

	n := newPushNotifier(nil, "https://aicli.ttai.online", nil)
	if got := n.sessionURL("node-a", "session-1"); got != full {
		t.Fatalf("未装配短址时应回退完整深链：%q", got)
	}
	shortLen := len([]rune(n.sessionURL("node-a", "session-1")))

	n.shortLinks = newShortLinker(filepath.Join(t.TempDir(), "short-links.json"), "https://aicli.ttai.online")
	got := n.sessionURL("node-a", "session-1")
	if !strings.HasPrefix(got, "https://aicli.ttai.online/s/") {
		t.Fatalf("装配短址后应返回短链：%q", got)
	}
	if len([]rune(got)) >= shortLen {
		t.Fatalf("短链应显著更短：%d ≥ %d", len([]rune(got)), shortLen)
	}

	// baseURL 为空：短址与直链都返回空（行为与历史一致）。
	empty := newPushNotifier(nil, "", nil)
	empty.shortLinks = newShortLinker(filepath.Join(t.TempDir(), "short-links.json"), "")
	if got := empty.sessionURL("node-a", "session-1"); got != "" {
		t.Fatalf("baseURL 为空应返回空串：%q", got)
	}
}

func TestShortLinkEvictsOldestBeyondCap(t *testing.T) {
	s := newShortLinker(filepath.Join(t.TempDir(), "short-links.json"), "https://gw.example.com")
	// 直接塞满表（不产生磁盘写入压力：path 仍有效但这里只验证淘汰逻辑）。
	s.mu.Lock()
	for i := 0; i < shortLinkMaxLinks+5; i++ {
		s.links[string(rune('A'+i%26))+strings.Repeat("x", shortLinkCodeLen-1)] = shortLinkEntry{
			SessionID: "s",
			CreatedAt: "2020-01-01T00:00:00Z",
		}
	}
	s.mu.Unlock()
	if code := s.Code("node-a", "session-1"); code == "" {
		t.Fatal("登记失败")
	}
	s.mu.Lock()
	got := len(s.links)
	s.mu.Unlock()
	if got > shortLinkMaxLinks {
		t.Fatalf("条目数 %d 超过上限 %d", got, shortLinkMaxLinks)
	}
}