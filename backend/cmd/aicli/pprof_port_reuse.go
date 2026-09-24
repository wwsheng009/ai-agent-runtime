package main

import (
	"net"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// 本文件实现"会话粘性 loopback 端口"的启动期解析：
//
//	aicli resume <session-id> --yolo --pprof --debug
//
// 在未显式指定 --web-port / AICLI_PPROF 时，复用该会话上一次实际监听的端口，
// 而不是每次都申请一个新的随机端口。这样 /debug/chat/*、/web/ 等 URL 在 resume
// 后保持不变，外部脚本与浏览器书签无需重新发现端口。
//
// 会话绑定由 internal/mesh 维护（mesh/bindings/<session-id>.json，S3）；
// 这里只负责"从命令行解析出本次要恢复的会话"并把它接到监听地址上。

// resolveChatWebPortTargetSessionID 解析本次启动将要恢复的会话 ID。
// 只认显式目标：
//   - aicli resume <session-id>
//   - aicli resume/chat --session <session-id>
//   - aicli exec resume <session-id>（--last 时位置参数是 prompt，不是会话）
//
// 裸 `aicli resume`（恢复最近会话）与交互式选择器在选择完成前无法确定 ID，
// 此时返回空串，由会话加载路径在加载完成后补写端口档案。
func resolveChatWebPortTargetSessionID(cmd *cobra.Command, args []string) string {
	if cmd == nil {
		return ""
	}
	parentName := ""
	if parent := cmd.Parent(); parent != nil {
		parentName = parent.Name()
	}
	name := cmd.Name()

	var candidate string
	switch {
	case parentName == "exec" && name == "resume":
		// exec resume 的位置参数是 SESSION_ID，但 --last 时是 PROMPT。
		last, _ := cmd.Flags().GetBool("last")
		if !last && len(args) > 0 {
			candidate = args[0]
		}
	case parentName != "exec" && name == "resume":
		// aicli resume <session-id>（chat 不接受位置参数，只走 --session）。
		if len(args) > 0 {
			candidate = args[0]
		}
	}
	if strings.TrimSpace(candidate) == "" && cmd.Flags().Lookup("session") != nil {
		if flagValue, err := cmd.Flags().GetString("session"); err == nil {
			candidate = flagValue
		}
	}
	return strings.TrimSpace(candidate)
}

// stickyLoopbackServerAddr 在 baseAddr（通常是 <host>:0，随机端口）之上叠加
// 会话粘性端口：命中绑定时返回 <webHost>:<preferred-port>，未命中时原样返回。
func stickyLoopbackServerAddr(webHost, baseAddr, sessionID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return baseAddr, false
	}
	binding, ok := mesh.LoadBinding(mesh.ResolvePaths(), sessionID)
	if !ok {
		return baseAddr, false
	}
	return net.JoinHostPort(webHost, strconv.Itoa(binding.Preferred.Port)), true
}

// loopbackServerHostPort 拆分已启动服务器的监听地址，返回 host 与端口号。
func loopbackServerHostPort(handle *pprofServerHandle) (string, int) {
	if handle == nil {
		return "", 0
	}
	host, portStr, err := net.SplitHostPort(handle.Addr())
	if err != nil {
		return "", 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0
	}
	return host, port
}
