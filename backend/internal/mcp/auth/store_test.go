package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTokenStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}

	token := &Token{
		ServerName:   "docs",
		ServerURL:    "https://example.com/mcp",
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := store.Put("docs", token); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := store.Get("docs")
	if !ok || got.AccessToken != "access-1" || got.RefreshToken != "refresh-1" {
		t.Fatalf("Get = %#v, %v", got, ok)
	}
	if got.ServerName != "docs" {
		t.Fatalf("Put 应回填 ServerName，got %q", got.ServerName)
	}
	if list := store.List(); len(list) != 1 || list[0].ServerName != "docs" {
		t.Fatalf("List = %#v", list)
	}

	// 重新打开文件应能读回（验证确实落盘）。
	reopened, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, ok := reopened.Get("docs"); !ok || got.AccessToken != "access-1" {
		t.Fatalf("reopened Get = %#v, %v", got, ok)
	}

	deleted, err := reopened.Delete("docs")
	if err != nil || !deleted {
		t.Fatalf("Delete = %v, %v", deleted, err)
	}
	if _, ok := reopened.Get("docs"); ok {
		t.Fatal("删除后不应还能读到")
	}
	deleted, err = reopened.Delete("docs")
	if err != nil || deleted {
		t.Fatalf("重复删除应为 false,nil: %v, %v", deleted, err)
	}
}

func TestTokenStorePermissionsAndAtomicWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不提供 POSIX 文件权限语义")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "tokens.json")
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	if err := store.Put("docs", &Token{ServerURL: "https://example.com/mcp", AccessToken: "a"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("令牌文件权限 = %o, want 600", perm)
	}
	if dirInfo, err := os.Stat(filepath.Dir(path)); err == nil {
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Fatalf("令牌目录权限 = %o, want 700", perm)
		}
	}
	// 目录里不应残留临时文件。
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".mcp-tokens-") {
			t.Fatalf("存在未清理的临时文件: %s", entry.Name())
		}
	}
}

func TestTokenStoreMalformedFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := NewTokenStore(path); err == nil {
		t.Fatal("损坏的令牌文件应报错而不是静默丢弃")
	}
}

func TestTokenExpired(t *testing.T) {
	now := time.Now()
	if (&Token{}).Expired(now, time.Minute) {
		t.Fatal("无过期时间的令牌应视为未过期（由 401 触发刷新）")
	}
	if !(&Token{ExpiresAt: now.Add(30 * time.Second)}).Expired(now, time.Minute) {
		t.Fatal("落在 skew 窗口内的令牌应视为过期")
	}
	if (&Token{ExpiresAt: now.Add(2 * time.Minute)}).Expired(now, time.Minute) {
		t.Fatal("未进入 skew 窗口的令牌不应过期")
	}
}

func TestSameServerURL(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://example.com/mcp", "https://example.com/mcp/", true},
		{"https://example.com/mcp", "https://EXAMPLE.com/mcp", true},
		{"https://example.com/mcp", "https://example.com/other", false},
	}
	for _, tc := range cases {
		if got := SameServerURL(tc.a, tc.b); got != tc.want {
			t.Fatalf("SameServerURL(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSessionRejectsTokenFromDifferentURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	if err := store.Put("docs", &Token{
		ServerURL:   "https://old.example.com/mcp",
		AccessToken: "old-token",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	session, err := NewSession("docs", "https://new.example.com/mcp", testAuthConfig(), SessionOptions{Store: store})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if session.Ready() {
		t.Fatal("同名但 URL 变化时必须视为未登录，避免把旧站令牌发给新站")
	}
}
