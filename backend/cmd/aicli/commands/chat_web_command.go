package commands

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// executeStructuredWebCommand 处理 /web 子命令：
//   - /web              显示 Web 服务器状态
//   - /web status       (同上)
//   - /web token        显示当前写令牌
//   - /web endpoints    显示全部 Web 调试端点清单
//   - /web open         在浏览器中打开 Web 客户端
func executeStructuredWebCommand(session *ChatSession, command string) CommandResult {
	arg := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))

	switch arg {
	case "", "status":
		return executeStructuredWebStatusCommand(session)
	case "token":
		return executeStructuredWebTokenCommand(session)
	case "endpoints":
		return executeStructuredWebEndpointsCommand(session)
	case "open":
		return executeStructuredWebStartCommand(session)
	default:
		return CommandResult{
			Blocks: []RenderBlock{{Document: render.SingleLineDoc(render.TextSpan(
				"错误: 未知子命令 /web " + arg + "，可用: token | endpoints | open | status",
			))}},
			Action: CommandContinue,
		}
	}
}

// executeStructuredWebStatusCommand 显示 Web 服务器的状态信息。
func executeStructuredWebStatusCommand(session *ChatSession) CommandResult {
	_ = session
	var builder chatDebugDocumentBuilder
	builder.heading("Web 客户端")

	webURL := chatDebugPprofWebURL()
	if webURL == "" {
		builder.meta("状态:", "未启用")
		builder.plain("  启动时加 --pprof / --debug 启用 HTTP 服务器，或用 --web-port <端口>")
		builder.plain("  --web-host 0.0.0.0 在 IPv4 局域网访问，--web-host :: 在 IPv6")
		return CommandResult{
			Blocks: []RenderBlock{{Document: builder.document()}},
			Action: CommandContinue,
		}
	}

	builder.meta("状态:", "运行中")
	builder.meta("URL:", webURL)

	if IsChatWebLoopbackMode() {
		builder.meta("模式:", "回环 (127.0.0.1/localhost)")
		builder.meta("令牌:", ChatWebAuthToken())
		builder.plain("  回环模式下回环 IP 自动免令牌，POST/PUT/DELETE 无需令牌")
	} else {
		builder.meta("模式:", "非回环 (局域网访问)")
		builder.plain("  本地浏览器: 回环 IP 始终免令牌")
		token := ChatWebAuthToken()
		if token != "" {
			builder.meta("令牌:", token)
		}
		port := chatDebugListenPort()
		lanAddrs := ChatWebLocalAddresses()
		if len(lanAddrs) > 0 {
			tokenParam := ChatWebTokenQueryParam()
			for _, ip := range lanAddrs {
				builder.plain("  LAN: http://" + ip + ":" + port + ChatWebPath + tokenParam)
			}
		}
	}

	return CommandResult{
		Blocks: []RenderBlock{{Document: builder.document()}},
		Action: CommandContinue,
	}
}

// executeStructuredWebTokenCommand 显示当前 Web 写令牌。
func executeStructuredWebTokenCommand(session *ChatSession) CommandResult {
	_ = session
	token := ChatWebAuthToken()
	if token == "" {
		return CommandResult{
			Blocks: []RenderBlock{{Document: render.SingleLineDoc(render.TextSpan(
				"Web 写令牌未初始化（HTTP 服务器未启用）",
			))}},
			Action: CommandContinue,
		}
	}

	var builder chatDebugDocumentBuilder
	builder.heading("Web 写令牌")
	builder.meta("X-AICLI-Token:", token)
	builder.meta("来源:", ChatWebAuthTokenSource())
	builder.plain("  在浏览器中带上 ?token=" + url.QueryEscape(token))
	builder.plain("  或通过 /web open 自动打开浏览器")

	return CommandResult{
		Blocks: []RenderBlock{{Document: builder.document()}},
		Action: CommandContinue,
	}
}

// executeStructuredWebEndpointsCommand 显示全部 Web 调试端点清单。
// 统一交互模式下在独立的全屏覆盖层展示（复用 debug overlay），不会进入
// 消息流；非交互/JSON 模式下回退为纯文本文档单元格。
func executeStructuredWebEndpointsCommand(session *ChatSession) CommandResult {
	_ = session
	text := BuildChatDebugEndpointsText()
	if text == "" {
		return CommandResult{
			Blocks: []RenderBlock{{Document: render.SingleLineDoc(render.TextSpan(
				"Web 调试端点未启用（HTTP 服务器未启动）",
			))}},
			Action: CommandContinue,
		}
	}

	if unifiedDirectInteractiveOutput(session) {
		// 统一交互模式：在全屏覆盖层展示，避免污染消息流。
		return CommandResult{
			Action:                 CommandContinue,
			OpenWebEndpointsScreen: true,
		}
	}

	// 非交互 / JSON 模式：回退为文本文档单元格。
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatPlainTextCommandDocument(text)}},
		Action: CommandContinue,
	}
}

// executeStructuredWebStartCommand 在浏览器中打开 Web 客户端页面。
// 非回环模式下，自动在 URL 中附加 token 参数。
func executeStructuredWebStartCommand(session *ChatSession) CommandResult {
	_ = session
	webURL := chatDebugPprofWebURL()
	if webURL == "" {
		return CommandResult{
			Blocks: []RenderBlock{{Document: render.SingleLineDoc(render.TextSpan(
				"错误: Web 服务器未启用。启动时加 --pprof / --debug / --web-port <端口> 启用",
			))}},
			Action: CommandContinue,
		}
	}

	// 非回环模式下，URL 需要 ?token= 参数；回环模式下不需要。
	finalURL := webURL + ChatWebTokenQueryParam()

	if err := openBrowser(finalURL); err != nil {
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatPlainTextCommandDocument(
				fmt.Sprintf("打开浏览器失败: %v\n请手动访问: %s", err, finalURL),
			)}},
			Action: CommandContinue,
		}
	}

	var builder chatDebugDocumentBuilder
	builder.heading("已打开 Web 客户端")
	builder.meta("URL:", finalURL)
	if !IsChatWebLoopbackMode() {
		builder.plain("  非回环模式：URL 已附加 token 参数")
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: builder.document()}},
		Action: CommandContinue,
	}
}

// openBrowser 在默认浏览器中打开指定 URL。
// 跨平台实现：macOS 用 open，Linux 用 xdg-open，Windows 用 rundll32。
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// canOpenChatWebEndpointsScreen 与 openChatWebEndpointsScreen 复用了
// /debug display 的 overlay 能力门控：仅当终端支持全屏模式（TTY+ANSI+
// 足够高度且无活跃的屏幕租约/弹窗）时才进入全屏；否则退回为
// printChatCommandOutput 直接打印文本到终端。
func canOpenChatWebEndpointsScreen(session *ChatSession) bool {
	return canOpenChatDebugOverlay(session)
}

// openChatWebEndpointsScreen 在独立的全屏覆盖层显示 Web 调试端点清单。
// 与 /debug display 的 overlay 复用同一套租约管理与键位循环；
// 进入失败时回退为直接打印纯文本。
func openChatWebEndpointsScreen(session *ChatSession) {
	if !canOpenChatWebEndpointsScreen(session) {
		printChatCommandOutput(session, BuildChatDebugEndpointsText())
		return
	}
	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: "Web 调试端点",
	})
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开调试信息界面失败: %w", err)), false)
		return
	}
	if !session.Interaction.waitUIActorIdleBounded("open web endpoints screen") {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("界面渲染未就绪")), false)
		return
	}

	body := BuildChatDebugEndpointsText()
	runErr := ui.RunDebugOverlayWithLease(context.Background(), resumeFullScreenTerminal(session), ui.DebugOverlayOptions{
		Title: "Web 调试端点",
		Body:  body,
	}, lease)
	releaseErr := lease.Release(context.Background())
	if runErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("界面异常: %w", runErr)), false)
		return
	}
	_ = releaseErr
}
