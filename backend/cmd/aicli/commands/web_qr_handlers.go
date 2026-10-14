package commands

// web_qr_handlers.go — micro web client 的二维码图片端点（GET /web/api/qr）。
//
// 背景：iLink 扫码登录返回的 qr_code_content 通常是登录链接/短码**文本**，
// 浏览器不能把任意文本直接当二维码展示。此端点用 skip2/go-qrcode（与 TUI
// /bots 同一依赖）把文本编码为 PNG 返回；前端把链接交给本端点即可用 <img>
// 展示，不再需要 JS 侧引入二维码库。
//
//   - 鉴权与 /web/api/* 同源（ChatWebAuthGuard 已在 mux 层统一承担）；
//   - GET ?text=<urlencoded>，上限 2KB（QR Medium 容量内），非法 400；
//   - 响应 image/png，带 no-store（二维码内容随会话变化，禁止缓存）。

import (
	"fmt"
	"image/png"
	"net/http"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// ChatWebAPIQRPath 是二维码图片端点路径。
const ChatWebAPIQRPath = "/web/api/qr"

// chatWebQRMaxTextLen 限制可编码文本长度：QR Medium 版本上限约 1.8KB 字节，
// 2KB 以内的请求按 400 拒绝，避免生成超大图。
const chatWebQRMaxTextLen = 2048

// HandleChatWebAPIQR 渲染 GET /web/api/qr?text=...
func HandleChatWebAPIQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	text := strings.TrimSpace(r.URL.Query().Get("text"))
	if text == "" {
		http.Error(w, "text query parameter is required", http.StatusBadRequest)
		return
	}
	if len(text) > chatWebQRMaxTextLen {
		http.Error(w, fmt.Sprintf("text too long (max %d bytes)", chatWebQRMaxTextLen), http.StatusBadRequest)
		return
	}
	code, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		http.Error(w, "encode qr failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := png.Encode(w, code.Image(256)); err != nil {
		// 头已写：只能截断连接（客户端会看到图片错误），记日志便于排查。
		logQRWriteError(err)
	}
}

// logQRWriteError 只在编码/写响应失败时记录（正常路径无日志噪音）。
func logQRWriteError(err error) {
	logpkg.Warnf("web qr encode failed: %v", err)
}
