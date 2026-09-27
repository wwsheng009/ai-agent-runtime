package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/approvalexplain"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// chatApprovalExplanation 是一条审批请求的「人话」解释。
type chatApprovalExplanation struct {
	// Action 是动作类别，例如「删除文件或目录（递归）」；为空表示无法解释。
	Action string
	// Target 是作用目标（命令 / 路径 / URL），已截断。
	Target string
	// Impact 是影响范围提示（可为空）。
	Impact []string
}

// approvalExplanation 用启发式规则把工具名 + 参数翻译成人话。
//
// 刻意不调用模型：分类只覆盖高置信度的模式，识别不出来就返回零值
// （宁可不解释，也不能猜错后让用户做出错误判断）。
func approvalExplanation(approval *runtimechat.ApprovalRequest) chatApprovalExplanation {
	if approval == nil {
		return chatApprovalExplanation{}
	}
	tool := strings.ToLower(strings.TrimSpace(approval.ToolName))
	args := decodeApprovalArgs(approval.ArgsJSON)

	switch {
	case isShellApprovalTool(tool):
		return explainShellApproval(approvalRequestArg(args, "command", "cmd", "script", "shell"), args)
	case strings.HasPrefix(tool, "mcp__") || tool == "mcp" || strings.HasSuffix(tool, "__mcp"):
		explanation := chatApprovalExplanation{
			Action: "调用 MCP 工具（由外部 MCP Server 执行）",
			Target: truncateChatRuntimeText(approvalRequestArg(args, "tool", "name", "command"), 120),
		}
		return explanation
	case strings.Contains(tool, "spawn"):
		return chatApprovalExplanation{
			Action: "启动子 agent（会消耗额度并可能与主会话并行执行）",
			Target: truncateChatRuntimeText(approvalRequestArg(args, "goal", "message", "task", "title"), 120),
		}
	case strings.Contains(tool, "background_task"):
		return chatApprovalExplanation{
			Action: "启动后台任务（在独立进程中持续运行）",
			Target: truncateChatRuntimeText(approvalRequestArg(args, "command"), 120),
		}
	case strings.Contains(tool, "delete") || strings.Contains(tool, "remove"):
		return chatApprovalExplanation{
			Action: "删除文件或目录",
			Target: truncateChatRuntimeText(approvalRequestArg(args, "path", "file_path", "filePath", "target"), 120),
		}
	case strings.Contains(tool, "move") || strings.Contains(tool, "rename"):
		return chatApprovalExplanation{
			Action: "移动或重命名文件",
			Target: truncateChatRuntimeText(approvalRequestArg(args, "path", "from", "source"), 120),
		}
	case strings.Contains(tool, "write") || strings.Contains(tool, "edit") || strings.Contains(tool, "patch") || strings.Contains(tool, "replace"):
		return chatApprovalExplanation{
			Action: "修改文件内容（会覆盖磁盘上的现有内容）",
			Target: truncateChatRuntimeText(approvalRequestArg(args, "path", "file_path", "filePath", "target"), 120),
		}
	case strings.Contains(tool, "web") || strings.Contains(tool, "fetch") || strings.Contains(tool, "http") || strings.Contains(tool, "browser") || strings.Contains(tool, "search"):
		return chatApprovalExplanation{
			Action: "访问网络",
			Target: truncateChatRuntimeText(approvalRequestArg(args, "url", "query", "q", "pattern"), 120),
		}
	case strings.Contains(tool, "view") || strings.Contains(tool, "read") || strings.Contains(tool, "grep") || strings.Contains(tool, "glob") || strings.Contains(tool, "ls"):
		return chatApprovalExplanation{
			Action: "读取文件内容（只读，不修改磁盘）",
			Target: truncateChatRuntimeText(approvalRequestArg(args, "path", "file_path", "pattern", "query"), 120),
		}
	}
	return chatApprovalExplanation{}
}

// approvalExplanationLines 把解释渲染为审批提示行；无法解释时返回 nil，
// 让调用方保持原有提示形态。
func approvalExplanationLines(approval *runtimechat.ApprovalRequest) []string {
	explanation := approvalExplanation(approval)
	if explanation.Action == "" && explanation.Target == "" {
		return nil
	}
	lines := make([]string, 0, 2+len(explanation.Impact))
	if explanation.Action != "" {
		lines = append(lines, "[说明] 动作："+explanation.Action)
	}
	if explanation.Target != "" {
		lines = append(lines, "[说明] 目标："+explanation.Target+"（完整内容用 [3] 查看）")
	}
	for _, impact := range explanation.Impact {
		if impact = strings.TrimSpace(impact); impact != "" {
			lines = append(lines, "[说明] 影响："+impact)
		}
	}
	return lines
}

// approvalExplainHook 是 [6]「解释这次调用」的可选模型解释钩子：
// runtime-server 宿主接入 §4.13 的只读解释端点（见 chat_runtime_server.go 的
// explainRuntimeServerApproval），本地模式接入 newLocalApprovalExplainHook；
// nil = 模型解释未启用或不可用（off / 无 provider / 建 runtime 失败），
// [6] 只输出规则解释加一行降级说明，不发网络请求。
type approvalExplainHook func(ctx context.Context, requestID string) (runtimeServerApprovalExplanation, error)

// approvalExplainBlockLines 生成 [6] 触发的解释块：
//  1. 规则解释（零成本；[说明] 前缀改为 [解释]，与 [6] 的语义一致）；
//  2. 模型摘要（本地或服务端钩子可用且成功时）；
//  3. 来源行（模型 <model>（已缓存）/ 规则模板）或降级行。
//
// 同一个审批的「只调一次模型」由调用方用 explainShown 守卫保证；本函数只负责
// 生成一次内容。解释路径永不阻塞决策：任何失败都降级为可见的一行说明。
func approvalExplainBlockLines(approval *runtimechat.ApprovalRequest, explain approvalExplainHook) []string {
	rules := approvalExplanationLines(approval)
	lines := make([]string, 0, len(rules)+3)
	for _, line := range rules {
		lines = append(lines, strings.Replace(line, "[说明] ", "[解释] ", 1))
	}

	if explain == nil {
		lines = append(lines, "[解释] 来源：规则模板",
			"[解释] 模型解释未启用或不可用（仅规则说明）")
		return lines
	}

	requestID := ""
	if approval != nil {
		requestID = strings.TrimSpace(approval.ID)
	}
	if requestID == "" {
		lines = append(lines, "[解释] 来源：规则模板", "[解释] 模型解释不可用：审批请求缺少 request id")
		return lines
	}

	result, err := explain(context.Background(), requestID)
	if err != nil {
		lines = append(lines, "[解释] 来源：规则模板", "[解释] 模型解释不可用："+shortApprovalExplainError(err))
		return lines
	}
	if strings.EqualFold(strings.TrimSpace(result.Source), "model") {
		if text := flattenApprovalExplainText(result.Explanation); text != "" {
			model := strings.TrimSpace(result.Model)
			if model == "" {
				model = "未知模型"
			}
			lines = append(lines, "[解释] 模型补充（"+model+"）："+text)
			source := "[解释] 来源：模型 " + model
			if result.Cached {
				source += "（已缓存）"
			}
			lines = append(lines, source)
			return lines
		}
	}
	// 服务端也回退到 rules（或返回了空摘要）：规则解释之外如实交代来源；
	// 本地规则解释不出来时，补上服务端给出的规则摘要，避免一无所获。
	lines = append(lines, "[解释] 来源：规则模板")
	if len(rules) == 0 {
		if text := flattenApprovalExplainText(result.Explanation); text != "" {
			lines = append(lines, "[解释] 服务端规则说明："+text)
		}
	}
	return lines
}

// shortApprovalExplainError 把钩子错误折叠成一行可读原因：超时/取消优先，
// HTTP 错误用状态行（例如 `409 Conflict`），其余截断原文，绝不把长栈刷进卡片。
func shortApprovalExplainError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "请求超时"
	case errors.Is(err, context.Canceled):
		return "请求已取消"
	}
	var httpErr *runtimeServerHTTPError
	if errors.As(err, &httpErr) && strings.TrimSpace(httpErr.Status) != "" {
		return strings.TrimSpace(httpErr.Status)
	}
	return truncateChatRuntimeText(strings.TrimSpace(err.Error()), 160)
}

// flattenApprovalExplainText 把模型返回的多行摘要压成单行（分号分隔），
// 卡片不会因为解释换行而挤散审批选项。
func flattenApprovalExplainText(text string) string {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' })
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return truncateChatRuntimeText(strings.Join(cleaned, "；"), 600)
}

func isShellApprovalTool(tool string) bool {
	switch {
	case strings.Contains(tool, "shell"), strings.Contains(tool, "cmd"), strings.Contains(tool, "exec"), strings.Contains(tool, "command"), strings.Contains(tool, "terminal"):
		// 文件写工具名里可能带 "command"（例如自定义工具），但它们已在前面的
		// 分支里被更强的模式命中；这里只兜底执行类工具。
		return !strings.Contains(tool, "write") && !strings.Contains(tool, "patch")
	default:
		return false
	}
}

func explainShellApproval(command string, args map[string]interface{}) chatApprovalExplanation {
	command = strings.TrimSpace(command)
	if command == "" {
		// 参数缺失或 JSON 不可解析：仍然如实说明这是执行类工具，并让用户去
		// [3] 看原文，而不是假装没有这条审批。
		return chatApprovalExplanation{
			Action: "执行 shell 命令",
			Impact: []string{"参数无法解析，请用 [3] 查看完整参数"},
		}
	}
	lower := strings.ToLower(command)
	action := "执行 shell 命令"
	switch {
	case containsAny(lower, "rm -rf", "rm -fr", "rm -r ", "remove-item -recurse", "remove-item -force -recurse", "del /s", "rmdir /s", "rd /s"):
		action = "删除文件或目录（递归）"
	case containsAny(lower, "git reset --hard", "git clean", "git checkout --", "git checkout .", "git restore "):
		action = "丢弃未提交的改动（不可通过 git 撤销）"
	case containsAny(lower, "git push", "git push --force", "git push -f"):
		action = "推送到远端仓库"
	case containsAny(lower, "npm install", "npm i ", "pnpm add", "pnpm install", "yarn add", "pip install", "pip3 install", "go get ", "go install", "cargo add", "go mod tidy", "apt-get install", "choco install", "winget install"):
		action = "安装依赖（会联网并改写 lockfile）"
	case containsAny(lower, "| sh", "| bash", "|sh", "|bash", "| iex", "invoke-expression", "iex (", "iex(") && containsAny(lower, "curl", "wget", "invoke-webrequest", "iwr ", "http://", "https://"):
		action = "下载并执行远程内容"
	case containsAny(lower, "chmod ", "chown ", "icacls ", "takeown ", "setfacl ", "attrib -"):
		action = "修改文件权限或所有者"
	case containsAny(lower, "mkfs", "dd if=", "> /dev/", "format ", "diskpart"):
		action = "写入设备或格式化磁盘"
	case containsAny(lower, "git commit", "git add ", "git merge", "git rebase", "git tag ", "git stash", "git apply"):
		action = "改写 Git 索引或历史（仅本地仓库）"
	case containsAny(lower, "drop table", "drop database", "truncate table", "delete from"):
		action = "执行破坏性数据库操作"
	case containsAny(lower, "docker rm", "docker rmi", "docker system prune", "kubectl delete"):
		action = "删除容器 / 镜像 / 集群资源"
	}

	explanation := chatApprovalExplanation{
		Action: action,
		Target: truncateChatRuntimeText(command, 120),
	}
	if path := approvalRequestArg(args, "workdir", "cwd", "dir"); strings.TrimSpace(path) != "" {
		explanation.Impact = append(explanation.Impact, "工作目录："+truncateChatRuntimeText(path, 80))
	}
	if outOfWorkspaceCommand(lower) {
		explanation.Impact = append(explanation.Impact, "命令涉及绝对路径或上级目录，影响范围可能超出当前项目")
	}
	return explanation
}

// outOfWorkspaceCommand 粗判命令是否触及工作区外路径（含绝对路径、家目录或 ..）。
func outOfWorkspaceCommand(lowerCommand string) bool {
	for _, token := range strings.FieldsFunc(lowerCommand, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ';' || r == '|' || r == '&' || r == '\n'
	}) {
		token = strings.Trim(token, `"'()`)
		switch {
		case strings.HasPrefix(token, "/") && !strings.HasPrefix(token, "/dev/null"):
			return true
		case strings.HasPrefix(token, "~/") || token == "~":
			return true
		case strings.HasPrefix(token, "..") && strings.Contains(token, "/"):
			return true
		case len(token) >= 3 && token[1] == ':' && (token[2] == '\\' || token[2] == '/'):
			return true
		}
	}
	return false
}

func decodeApprovalArgs(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return payload
}

func approvalRequestArg(args map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := args[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		case float64:
			return fmt.Sprintf("%v", typed)
		case bool:
			if typed {
				return "true"
			}
		}
	}
	return ""
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// localApprovalExplainSourceModel 与 runtimeapi 的 source 取值保持一致
// （`model`）；本地模式不引入自己的来源词汇。
const localApprovalExplainSourceModel = "model"

// localApprovalExplainMode 解析本地解释模式，取值与别名语义完全复用共享包
// （与 runtimeapi.ParseApprovalExplainMode 同源）。pre_generate 在本地没有预热
// 通路——本地审批只会经由交互面板出现，没有服务端 /runtime 读路径可供预生成，
// 因此按 on_demand 处理（首次按 [6] 时调用一次，调用方仍有 explainShown 去重）。
func localApprovalExplainMode() approvalexplain.Mode {
	mode, ok := approvalexplain.ParseMode(os.Getenv(approvalexplain.ModeEnv))
	if !ok || mode == approvalexplain.ModePreGenerate {
		return approvalexplain.ModeOnDemand
	}
	return mode
}

// newLocalApprovalExplainHook 为本地模式（无 runtime-server）安装 [6] 的模型
// 解释钩子；不满足可用性时返回 nil，[6] 保持规则解释 + 一行降级说明。
//
// 可用性判定：
//   - AICLI_APPROVAL_EXPLAIN_MODE=off（或 none/disabled 等别名）→ 不注入；
//   - 会话没有 provider（session.Provider.GetType() 为空）→ 不注入；
//   - 用会话自身 provider/model 装配一次性 runtime 失败 → 不注入。
//
// 钩子每次调用都重新装配 runtime 并重新解析 provider/model，跟随会话中途的
// provider/model 切换；runtime-server 执行器仍会在自己的 run 内临时覆盖该默认
// 值并在 defer 中恢复（见 chat_runtime_server.go），语义不变。
func newLocalApprovalExplainHook(session *ChatSession) approvalExplainHook {
	if session == nil || localApprovalExplainMode() == approvalexplain.ModeOff {
		return nil
	}
	if strings.TrimSpace(session.Provider.GetType()) == "" {
		return nil
	}
	// 装配 runtime 只做本地校验（不发网络请求）；失败说明当前会话配置不足以
	// 解释，宁可保持可见的降级行，也不把失败推迟到用户点击 [6] 时。
	if runtime, err := buildSharedChatAutoCompactRuntime(session); err != nil || runtime == nil {
		return nil
	}
	return func(ctx context.Context, requestID string) (runtimeServerApprovalExplanation, error) {
		return explainApprovalLocally(ctx, session, requestID)
	}
}

// explainApprovalLocally 执行一次 §4.13 只读本地解释：
// 读当前 pending 审批 → 一次小预算模型调用（共享包 prompt/预算）→ 记账。
// 任何失败都返回 error，由 approvalExplainBlockLines 降级为「规则 + 一行可读
// 原因」；本路径不写任何决策状态，也绝不代替批准/拒绝。
func explainApprovalLocally(ctx context.Context, session *ChatSession, requestID string) (runtimeServerApprovalExplanation, error) {
	var empty runtimeServerApprovalExplanation
	if session == nil {
		return empty, fmt.Errorf("chat session is unavailable")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return empty, fmt.Errorf("审批请求缺少 request id")
	}
	if session.LocalRuntimeHost == nil {
		// runtime-server 会话或 headless 宿主没有本地状态源：本地解释不可用，
		// 如实降级（runtime-server 执行器会在 run 内改用服务端端点钩子）。
		return empty, fmt.Errorf("本地会话状态不可用")
	}
	pending := localPendingApprovalForExplain(session, requestID)
	if pending == nil {
		// 与 runtime-server 端点 409 同义：审批已被裁决 / 过期 / 从未存在。
		return empty, fmt.Errorf("审批已不在等待中")
	}
	llmRuntime, err := buildSharedChatAutoCompactRuntime(session)
	if err != nil {
		return empty, fmt.Errorf("解释运行时不可用：%w", err)
	}
	if llmRuntime == nil {
		return empty, fmt.Errorf("当前会话没有可用的 provider")
	}
	// 与 auto-compact 相同的解析口径：provider 名优先会话声明，缺 model 时
	// 回落到 provider 默认模型，最后才是 runtime 默认（buildSharedChat… 的解析）。
	provider := strings.TrimSpace(session.ProviderName)
	if provider == "" {
		provider = strings.TrimSpace(llmRuntime.DefaultProvider())
	}
	model := strings.TrimSpace(session.Model)
	if model == "" {
		model = strings.TrimSpace(session.Provider.DefaultModel)
	}
	if model == "" {
		model = strings.TrimSpace(llmRuntime.DefaultModel())
	}
	if model == "" {
		return empty, fmt.Errorf("当前会话没有可用的模型")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, approvalexplain.Timeout)
	defer cancel()
	sessionID := currentRuntimeSessionID(session)
	llmRequestID := fmt.Sprintf("approval-explain-%s-%d", sessionID, time.Now().UnixNano())
	resp, callErr := llmRuntime.Call(callCtx, &runtimellm.LLMRequest{
		Provider:  provider,
		Model:     model,
		MaxTokens: approvalexplain.MaxTokens,
		Messages: []runtimetypes.Message{
			{Role: "system", Content: approvalexplain.SystemPrompt},
			{Role: "user", Content: approvalexplain.UserPrompt(pending)},
		},
	})
	// 成功与失败都记账（与 runtimeapi 同形同义），bus 拿不到时静默跳过。
	publishLocalApprovalExplainUsage(session, sessionID, llmRequestID, provider, model, resp, callErr)
	if callErr != nil {
		return empty, callErr
	}
	if resp == nil {
		return empty, fmt.Errorf("模型返回为空")
	}
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		return empty, fmt.Errorf("模型返回为空")
	}
	return runtimeServerApprovalExplanation{
		Explanation: text,
		Source:      localApprovalExplainSourceModel,
		Model:       model,
	}, nil
}

// localPendingApprovalForExplain 读取本地 durable runtime state 里与 requestID
// 匹配的 pending 审批；不匹配（已被裁决 / 换了下一条）返回 nil，避免解释错对象。
func localPendingApprovalForExplain(session *ChatSession, requestID string) *runtimechat.ApprovalRequest {
	state := loadRestoredPendingRuntimeState(session)
	if state == nil || state.PendingApproval == nil {
		return nil
	}
	if strings.TrimSpace(state.PendingApproval.ID) != requestID {
		return nil
	}
	return state.PendingApproval
}

// publishLocalApprovalExplainUsage 把本地解释调用发布到本地 EventBus 的
// `llm.request.finished`（usageledger / usageanalytics 的既有订阅点）：
// payload 由共享包构造，与 runtimeapi.publishApprovalExplainUsage 同形同义
// （origin/source=approval_explain、provider/model、成功带 usage_*、失败带 error）。
//
// host 或 bus 缺失（headless / 测试 / 未绑定本地宿主的会话）时静默跳过——
// 记账是 best-effort，绝不 panic、绝不改变解释结果。
func publishLocalApprovalExplainUsage(
	session *ChatSession,
	sessionID, requestID, provider, model string,
	resp *runtimellm.LLMResponse,
	callErr error,
) {
	if session == nil {
		return
	}
	host := session.LocalRuntimeHost
	if host == nil || host.EventBus == nil {
		return
	}
	host.EventBus.Publish(runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: sessionID,
		Payload:   approvalexplain.UsagePayload(requestID, provider, model, resp, callErr),
	})
}
