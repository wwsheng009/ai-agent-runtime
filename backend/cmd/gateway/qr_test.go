package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 扫码登录端点：把任意文本编码为 PNG（<img> 直接展示），并拒绝空/超长输入。
func TestHandleQRCode(t *testing.T) {
	handler := handleQRCode()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/qr?text="+url.QueryEscape("https://liteapp.weixin.qq.com/q/abc123"), nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q, want image/png", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cc)
	}
	body := rec.Body.Bytes()
	if len(body) < 8 || !bytes.Equal(body[:8], []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("body is not a PNG (len=%d)", len(body))
	}

	for name, target := range map[string]string{
		"empty":   "/api/qr",
		"spaces":  "/api/qr?text=%20%20",
		"too-long": "/api/qr?text=" + url.QueryEscape(strings.Repeat("a", qrTextLimit+1)),
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}