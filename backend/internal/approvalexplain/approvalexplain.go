// Package approvalexplain 是 §4.13「审批解释」的共享单一真源：预算常量、
// 提示词、参数摘要与规则降级都在这里定义，runtime-server
// （internal/api/runtimeapi）与 aicli 本地模式（cmd/aicli/commands）共同委托，
// 保证两端在同一条 pending 审批上得到逐字节一致的 prompt 与规则输出。
//
// 这里只放「纯数据」：解释模式门控、缓存、单飞与使用量事件发布仍由各自宿主
// 负责（服务端在 runtimeapi，本地在 CLI 的命令层）。
package approvalexplain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

const (
	// MaxTokens / Timeout 是一次解释调用的独立预算：给足要点即可，避免把
	// 费用放大到与真实回合同量级。
	MaxTokens = 320
	Timeout   = 20 * time.Second
	// ArgumentLimit 是参数（命令 / 补丁）截断上限：解释只需要「大致做什么」，
	// 不需要全文。
	ArgumentLimit = 4000
	// DigestLineLimit 是参数摘要最多抽取的字段行数。
	DigestLineLimit = 6

	// ModeEnv 是审批解释模式的宿主环境变量名（runtime-server 与 aicli 共用）：
	// off | on_demand（默认）| pre_generate，取值语义见 ParseMode。
	ModeEnv = "AICLI_APPROVAL_EXPLAIN_MODE"
)

// SystemPrompt 是一次只读审批解释的系统提示词。
const SystemPrompt = "你是审批解释器：用户即将批准或拒绝一次工具调用，需要快速判断它在做什么。\n" +
	"用不超过 3 条要点说明：1) 这条命令/补丁具体会做什么；2) 会触碰哪些路径、网络或副作用；3) 需要留意的风险点。\n" +
	"不要建议批准或拒绝，不要复述参数全文，不要输出 JSON 或 Markdown 标题。\n" +
	"用与「触发原因」相同的语言作答；判断不了时用简体中文。"

// UserPrompt 把一条 pending 审批整理为模型输入：只给结构化事实（工具 / 原因 /
// 风险 / 可记忆模式 / 截断后的参数摘要），不携带任何决策指令。
func UserPrompt(pending *chat.ApprovalRequest) string {
	if pending == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("工具：" + ToolLabel(pending) + "\n")
	if reason := strings.TrimSpace(pending.Reason); reason != "" {
		b.WriteString("触发原因：" + reason + "\n")
	}
	if risk := strings.TrimSpace(pending.RiskLevel); risk != "" {
		b.WriteString("风险级别：" + risk + "\n")
	}
	if pattern := strings.TrimSpace(pending.RememberPattern); pattern != "" {
		b.WriteString("可记忆为：" + pattern + "\n")
	}
	if digest := ArgumentDigest(pending.ArgsJSON); digest != "" {
		b.WriteString("参数摘要：\n" + digest)
	}
	return strings.TrimSpace(b.String())
}

// RuleBased 是模型不可用时的降级：不猜语义，只把引擎已有的事实（工具 / 原因 /
// 风险 / 参数 / 可否记忆）整理成可核对的一段话。
func RuleBased(pending *chat.ApprovalRequest) string {
	if pending == nil {
		return ""
	}
	lines := []string{"工具：" + ToolLabel(pending)}
	if reason := strings.TrimSpace(pending.Reason); reason != "" {
		lines = append(lines, "触发原因："+reason)
	}
	if risk := strings.TrimSpace(pending.RiskLevel); risk != "" {
		lines = append(lines, "风险级别："+risk)
	}
	if digest := ArgumentDigest(pending.ArgsJSON); digest != "" {
		lines = append(lines, "参数摘要：", digest)
	}
	if pattern := strings.TrimSpace(pending.RememberPattern); pattern != "" {
		lines = append(lines, "可记忆为："+pattern+"（批准并勾选「记住」后生效）")
	} else {
		lines = append(lines, "不可记忆：该审批每次都需要人工确认")
	}
	return strings.Join(lines, "\n")
}

// ToolLabel 返回审批的工具名；缺失时回退 "unknown"（与规则摘要一致）。
func ToolLabel(pending *chat.ApprovalRequest) string {
	if pending == nil {
		return "unknown"
	}
	if tool := strings.TrimSpace(pending.ToolName); tool != "" {
		return tool
	}
	return "unknown"
}

// ArgumentDigest 优先抽取「一眼能看出在做什么」的字段，其次回退原始 JSON。
// 所有输出都截断，避免把整份补丁塞进解释或 prompt。
func ArgumentDigest(argsJSON json.RawMessage) string {
	raw := strings.TrimSpace(string(argsJSON))
	if raw == "" || raw == "null" || raw == "{}" {
		return ""
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(argsJSON, &decoded); err != nil {
		return Truncate(raw, ArgumentLimit)
	}
	priority := []string{
		"command", "command_line", "cmd", "script",
		"file_path", "path", "paths", "target_file",
		"url", "query", "pattern", "glob",
		"patch", "diff", "content", "prompt",
	}
	lines := make([]string, 0, DigestLineLimit)
	seen := map[string]bool{}
	for _, key := range priority {
		value, ok := decoded[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(ArgumentValue(value))
		if text == "" {
			continue
		}
		lines = append(lines, "- "+key+": "+Truncate(text, ArgumentLimit))
		seen[key] = true
		if len(lines) >= DigestLineLimit {
			break
		}
	}
	if len(lines) == 0 {
		return Truncate(raw, ArgumentLimit)
	}
	return strings.Join(lines, "\n")
}

// ArgumentValue 把参数值展平成单行文本：数组用 ", " 连接，其余类型走高保真
// JSON 编码；编码失败时退回 %v，绝不丢字段。
func ArgumentValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(ArgumentValue(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	default:
		if encoded, err := json.Marshal(typed); err == nil {
			return string(encoded)
		}
		return fmt.Sprintf("%v", typed)
	}
}

// Truncate 按 rune 截断文本并追加省略号，避免把多字节字符切坏；limit<=0 表示
// 不截断。
func Truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
