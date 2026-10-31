package main

// qr.go — 网关控制台的二维码图片端点（GET /api/qr?text=...）。
//
// 与 aicli micro web 的 /web/api/qr 同口径（docs/bots/07 §扫码）：
// 浏览器不能把任意文本直接当二维码展示，而微信扫码返回的 `qr_code_content`
// 是 `https://liteapp.weixin.qq.com/q/...` 这类**待编码的链接**，不是图片。
// 因此控制台把链接交给本端点，用 skip2/go-qrcode（与 TUI /bots 同一依赖）
// 编码为 PNG 后以 <img> 展示。

import (
	"net/http"
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	// qrTextLimit 限制编码文本长度，避免超长输入放大 CPU/内存。
	qrTextLimit = 2048
	// qrImageSize 与 micro web 的 320px 对齐。
	qrImageSize = 320
)

// handleQRCode 渲染 GET /api/qr?text=... 为 image/png。
// 鉴权由外层 owner.RequireSession 负责（同源 <img> 会携带会话 cookie）。
func handleQRCode() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		text := strings.TrimSpace(r.URL.Query().Get("text"))
		if text == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is required"})
			return
		}
		if len(text) > qrTextLimit {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text too long (limit 2048 bytes)"})
			return
		}
		code, err := qrcode.New(text, qrcode.Medium)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid text: " + err.Error()})
			return
		}
		png, err := code.PNG(qrImageSize)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "encode png: " + err.Error()})
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", strconv.Itoa(len(png)))
		_, _ = w.Write(png)
	}
}