package commands

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// HandleChatWebAPIExport 是微型 Web 客户端的会话导出下载端点（顶部菜单栏
// 「文件 → 导出会话」的数据源）：
//
//	GET /web/api/export[?format=full|body|tools|trace][&session_id=<id>]
//
// 会话解析、格式归一化与写出实现全部复用 CLI 同一份代码
// （resolveChatExportRuntimeSession / writeChatSessionExportToPath），因此下载内容
// 与 TUI `/export`、顶层 `aicli export` 的产物逐字节同源，不存在第二套格式实现。
// 差别只在产物归属：命令行导出落盘到 exports 目录，web 导出把内容作为附件回传，
// 用同机临时文件中转（写临时文件 → http.ServeContent → 删除），大 artifact 不会
// 整份驻留内存，也不在服务端留下副本。
//
// 鉴权沿用 /web/* 统一约定（ChatWebAuthGuard：Host/Origin + 非回环模式写令牌）；
// 本端点只读，回环模式免令牌，非回环模式由页面注入的 fetch 包装附 X-AICLI-Token。
func HandleChatWebAPIExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, chatWebExportErrorBody("method_not_allowed", "导出端点只接受 GET"))
		return
	}
	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, chatWebExportErrorBody("chat_session_not_ready", "chat session not ready"))
		return
	}
	opts := chatExportOptions{Target: "current", Format: chatExportFormatFull, ExplicitTarget: true}
	if sessionID := strings.TrimSpace(r.URL.Query().Get("session_id")); sessionID != "" {
		opts.Target = sessionID
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("format")); raw != "" {
		if err := applyChatExportFormat(&opts, raw); err != nil {
			writeWebAPIJSON(w, http.StatusBadRequest, chatWebExportErrorBody("invalid_format", err.Error()))
			return
		}
	}
	runtimeSession, source, err := resolveChatExportRuntimeSession(session, opts)
	if err != nil {
		writeWebAPIJSON(w, http.StatusNotFound, chatWebExportErrorBody("session_unavailable", err.Error()))
		return
	}
	if runtimeSession == nil {
		writeWebAPIJSON(w, http.StatusNotFound, chatWebExportErrorBody("session_unavailable", "未找到可导出的会话"))
		return
	}
	extension := ".json"
	if _, isMarkdown := chatExportMarkdownModeForFormat(opts.Format); isMarkdown {
		extension = ".md"
	}
	temporaryFile, err := os.CreateTemp("", "aicli-web-export-*"+extension)
	if err != nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, chatWebExportErrorBody("export_failed", fmt.Sprintf("创建导出临时文件失败: %v", err)))
		return
	}
	temporaryPath := temporaryFile.Name()
	if err := temporaryFile.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		writeWebAPIJSON(w, http.StatusInternalServerError, chatWebExportErrorBody("export_failed", fmt.Sprintf("关闭导出临时文件失败: %v", err)))
		return
	}
	defer os.Remove(temporaryPath)

	format, stats, err := writeChatSessionExportToPath(temporaryPath, session, runtimeSession, source, opts.Format)
	if err != nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, chatWebExportErrorBody("export_failed", err.Error()))
		return
	}
	file, err := os.Open(temporaryPath)
	if err != nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, chatWebExportErrorBody("export_failed", fmt.Sprintf("读取导出文件失败: %v", err)))
		return
	}
	defer file.Close()

	filename := chatWebExportFileName(runtimeSession, format, extension)
	w.Header().Set("Content-Type", chatWebExportContentType(format))
	w.Header().Set("Content-Disposition", chatWebExportContentDisposition(filename))
	w.Header().Set("Cache-Control", "no-store")
	// 响应头附带导出摘要：前端在下载成功后直接展示消息条数，不必再解析文件。
	w.Header().Set("X-AICLI-Export-Format", string(format))
	w.Header().Set("X-AICLI-Export-Messages", strconv.Itoa(stats.MessageCount))
	w.Header().Set("X-AICLI-Export-Session", sanitizeChatExportFileComponent(runtimeSession.ID))
	// ServeContent 负责 HEAD / Range / Content-Length；modtime 置零避免多一次 Stat。
	http.ServeContent(w, r, filename, time.Time{}, file)
}

// chatWebExportErrorBody 与 /web/* 其它端点一致的稳定错误 envelope。
func chatWebExportErrorBody(code, message string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
}

// chatWebExportContentType 按实际生效格式给出下载响应的 Content-Type：
// markdown 家族为 text/markdown，其余（完整 JSON）为 application/json。
func chatWebExportContentType(format chatExportFormat) string {
	if _, isMarkdown := chatExportMarkdownModeForFormat(format); isMarkdown {
		return "text/markdown; charset=utf-8"
	}
	return "application/json; charset=utf-8"
}

// chatWebExportFileName 复用 CLI 默认命名规则
// （{session}_{YYYYMMDD_HHMMSS}_{format}{扩展名}，见 resolveChatExportOutputPath），
// 让浏览器下载的文件名与 `aicli export` 的落盘产物同名同序，便于对照排查。
func chatWebExportFileName(runtimeSession *runtimechat.Session, format chatExportFormat, extension string) string {
	sessionID := "session"
	if runtimeSession != nil && strings.TrimSpace(runtimeSession.ID) != "" {
		sessionID = sanitizeChatExportFileComponent(runtimeSession.ID)
	}
	return fmt.Sprintf("%s_%s_%s%s", sessionID, time.Now().Format("20060102_150405"), format, extension)
}

// chatWebExportContentDisposition 同时给出 ASCII 回退名与 RFC 5987 的 UTF-8 名：
// 会话 ID 可能含非 ASCII 字符，只写 filename= 会让部分浏览器拿到乱码名甚至丢弃
// 文件名；filename*= 是支持面最广的替代写法。
func chatWebExportContentDisposition(filename string) string {
	var ascii strings.Builder
	for _, r := range filename {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			ascii.WriteRune('_')
			continue
		}
		ascii.WriteRune(r)
	}
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", ascii.String(), url.PathEscape(filename))
}
