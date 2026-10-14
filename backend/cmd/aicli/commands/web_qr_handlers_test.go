package commands

// 「机器人」页签二维码端点回归测试：GET /web/api/qr 把文本编码为 PNG，
// 供扫码登录对话框在浏览器直接 <img> 展示（iLink 返回的 qr_code_content
// 多为登录链接文本）。

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHandleChatWebAPIQRRendersPNG(t *testing.T) {
	query := url.Values{"text": {"https://ilink.example/login?token=abc"}}
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIQRPath+"?"+query.Encode(), nil)
	recorder := httptest.NewRecorder()
	HandleChatWebAPIQR(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q, want image/png", ct)
	}
	if cc := recorder.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cc)
	}
	decoded, err := png.Decode(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("body is not a valid PNG: %v", err)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 256 || bounds.Dy() != 256 {
		t.Fatalf("qr image bounds = %v, want 256x256", bounds)
	}
}

func TestHandleChatWebAPIQRRejects(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{name: "missing text", query: "", want: http.StatusBadRequest},
		{name: "empty text", query: "?text=", want: http.StatusBadRequest},
		{name: "text too long", query: "?text=" + url.QueryEscape(strings.Repeat("a", 3000)), want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, ChatWebAPIQRPath+tc.query, nil)
			recorder := httptest.NewRecorder()
			HandleChatWebAPIQR(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}

	post := httptest.NewRequest(http.MethodPost, ChatWebAPIQRPath+"?text=x", nil)
	postRecorder := httptest.NewRecorder()
	HandleChatWebAPIQR(postRecorder, post)
	if postRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", postRecorder.Code)
	}
}
