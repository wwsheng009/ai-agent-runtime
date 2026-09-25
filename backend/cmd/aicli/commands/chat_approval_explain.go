package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
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
