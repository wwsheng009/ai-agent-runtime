package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// ChatWebPortRecord 是"会话粘性端口"的持久化记录：某个 chat 会话上一次实际
// 监听的 loopback（pprof / /debug / Web 客户端）端口。
//
// 存在的原因：`aicli resume <session-id> --pprof --debug` 在未指定 --web-port
// 时每次都会重新申请一个随机空闲端口，导致上一次会话期间保存的
// http://127.0.0.1:<port>/debug/chat/status、/web/ 等 URL 在 resume 后全部失效。
// 记录端口后，resume 同一个会话会优先复用该端口（仅当端口不可用时才回退随机）。
//
// 记录按 session ID 存放在 ~/.aicli/web-ports/<session-id>.json，独立于会话
// 存储目录：这样即使会话目录来自运行时配置（或启动早期还读不到配置），
// resume 也能命中同一份端口档案。
type ChatWebPortRecord struct {
	SessionID string    `json:"session_id"`
	Port      int       `json:"port"`
	Host      string    `json:"host,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// chatWebPortStoreDirName 仅用于文档/错误信息，实际目录来自 aiclipaths。
const chatWebPortStoreDirName = "web-ports"

var (
	chatWebPortRuntimeMu     sync.RWMutex
	chatWebPortRuntimePort   int
	chatWebPortRuntimeHost   string
	chatWebPortRuntimeActive bool
)

// SetChatWebPortRuntimeInfo 记录当前进程实际监听的 loopback 端口。
// main 在 loopback 服务器启动成功后调用；未启动服务器时保持未设置状态，
// 这样会话加载路径不会把"没有诊断端点"误写成端口档案。
func SetChatWebPortRuntimeInfo(port int, host string) {
	if port < 1 || port > 65535 {
		return
	}
	chatWebPortRuntimeMu.Lock()
	chatWebPortRuntimePort = port
	chatWebPortRuntimeHost = strings.TrimSpace(host)
	chatWebPortRuntimeActive = true
	chatWebPortRuntimeMu.Unlock()
}

// ChatWebPortRuntimeInfo 返回当前进程的 loopback 端口（未启动时 ok=false）。
func ChatWebPortRuntimeInfo() (port int, host string, ok bool) {
	chatWebPortRuntimeMu.RLock()
	defer chatWebPortRuntimeMu.RUnlock()
	if !chatWebPortRuntimeActive {
		return 0, "", false
	}
	return chatWebPortRuntimePort, chatWebPortRuntimeHost, true
}

// resetChatWebPortRuntimeInfoForTest 清空进程级端口信息（测试用）。
func resetChatWebPortRuntimeInfoForTest() {
	chatWebPortRuntimeMu.Lock()
	chatWebPortRuntimePort = 0
	chatWebPortRuntimeHost = ""
	chatWebPortRuntimeActive = false
	chatWebPortRuntimeMu.Unlock()
}

// chatWebPortStoreDir 返回端口档案目录；测试可通过环境变量
// AICLI_WEB_PORTS_DIR 覆盖（默认 ~/.aicli/web-ports）。
func chatWebPortStoreDir() string {
	if override := strings.TrimSpace(os.Getenv("AICLI_WEB_PORTS_DIR")); override != "" {
		return override
	}
	return aiclipaths.DefaultWebPortsDir()
}

// sanitizeChatWebPortSessionID 把会话 ID 规整为安全的文件名片段。
// 会话 ID 形如 session_20260922173838_lsirc1la；任何不在白名单内的字符都会被
// 替换，避免路径穿越或非法文件名。
func sanitizeChatWebPortSessionID(sessionID string) string {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	sanitized := b.String()
	if sanitized == "." || sanitized == ".." {
		return ""
	}
	return sanitized
}

// ChatWebPortRecordPath 返回会话端口档案的文件路径；会话 ID 非法时返回空串。
func ChatWebPortRecordPath(sessionID string) string {
	sanitized := sanitizeChatWebPortSessionID(sessionID)
	if sanitized == "" {
		return ""
	}
	return filepath.Join(chatWebPortStoreDir(), sanitized+".json")
}

// LoadChatWebPortRecord 读取会话的端口档案。
// 档案缺失、损坏或端口越界都按"没有记录"处理（ok=false），调用方回退随机端口。
func LoadChatWebPortRecord(sessionID string) (ChatWebPortRecord, bool) {
	path := ChatWebPortRecordPath(sessionID)
	if path == "" {
		return ChatWebPortRecord{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ChatWebPortRecord{}, false
	}
	var record ChatWebPortRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return ChatWebPortRecord{}, false
	}
	if record.Port < 1 || record.Port > 65535 {
		return ChatWebPortRecord{}, false
	}
	if strings.TrimSpace(record.SessionID) == "" {
		record.SessionID = strings.TrimSpace(sessionID)
	}
	return record, true
}

// SaveChatWebPortRecord 写入会话的端口档案（原子替换，避免半截文件）。
func SaveChatWebPortRecord(sessionID string, port int, host string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid web port %d: must be between 1 and 65535", port)
	}
	path := ChatWebPortRecordPath(sessionID)
	if path == "" {
		return fmt.Errorf("invalid session id %q for %s record", sessionID, chatWebPortStoreDirName)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s dir: %w", chatWebPortStoreDirName, err)
	}
	record := ChatWebPortRecord{
		SessionID: strings.TrimSpace(sessionID),
		Port:      port,
		Host:      strings.TrimSpace(host),
		UpdatedAt: time.Now(),
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s record: %w", chatWebPortStoreDirName, err)
	}
	data = append(data, '\n')
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s record: %w", chatWebPortStoreDirName, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace %s record: %w", chatWebPortStoreDirName, err)
	}
	return nil
}

// persistChatWebPortForSession 把当前进程的 loopback 端口记到指定会话名下。
// 在会话成为活动会话（新建或恢复）时调用，best-effort：
// 没有启动 loopback 服务器时静默跳过，写盘失败也不影响会话流程。
func persistChatWebPortForSession(sessionID string) {
	port, host, ok := ChatWebPortRuntimeInfo()
	if !ok {
		return
	}
	_ = SaveChatWebPortRecord(sessionID, port, host)
}
