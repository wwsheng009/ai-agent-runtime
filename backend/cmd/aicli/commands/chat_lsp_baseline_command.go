package commands

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	lspbaseline "github.com/wwsheng009/ai-agent-runtime/internal/lsp/baseline"
)

// chatLSPBaselineUsage 是 /lsp baseline 的用法卡。
const chatLSPBaselineUsage = `用法:
  /lsp baseline [--days N]              归因最近 N 天（默认 14；0 = 全窗口）
  /lsp baseline --since YYYY-MM-DD      指定起始日期（与 --days 互斥，优先 --since）
  /lsp baseline --root DIR              指定 chat-logs 根（默认 ~/.aicli/chat-logs）
  /lsp baseline --help                  显示本帮助

说明：全库只读扫描，可能耗时数秒（窗口内按文件 mtime 可整文件跳过）。`

// chatLSPBaselineText 执行 /lsp baseline：读会话 runtime 事件日志归因出
// §4.3 基线报告（internal/lsp/baseline，与离线脚本同口径）。
//
// 这是一次只读的全库扫描（可被 --days/--since 的按文件 mtime 快速路径收窄），
// 输出较长，由 executeStructuredLSPCommand 按副屏文档规则呈现；未采集项
// 一律 n/a，不做阈值判断（方案 §4.3 反模式纪律）。
func chatLSPBaselineText(args []string) string {
	days := 14
	sinceText := ""
	roots := []string{aiclipaths.DefaultChatLogsDir()}

	for index := 0; index < len(args); index++ {
		switch strings.ToLower(strings.TrimSpace(args[index])) {
		case "-h", "--help", "help":
			return chatLSPBaselineUsage
		case "--days":
			if index+1 >= len(args) {
				return "错误: --days 需要天数\n" + chatLSPBaselineUsage
			}
			value, err := strconv.Atoi(strings.TrimSpace(args[index+1]))
			if err != nil || value < 0 {
				return fmt.Sprintf("错误: --days 需要非负整数，收到 %q\n%s", args[index+1], chatLSPBaselineUsage)
			}
			days = value
			index++
		case "--since":
			if index+1 >= len(args) {
				return "错误: --since 需要日期\n" + chatLSPBaselineUsage
			}
			sinceText = strings.TrimSpace(args[index+1])
			index++
		case "--root":
			if index+1 >= len(args) {
				return "错误: --root 需要目录\n" + chatLSPBaselineUsage
			}
			roots = []string{strings.TrimSpace(args[index+1])}
			index++
		default:
			return fmt.Sprintf("错误: 未知参数 %q\n%s", args[index], chatLSPBaselineUsage)
		}
	}

	opts := lspbaseline.Options{Roots: roots}
	windowText := "全窗口"
	if sinceText != "" {
		parsed, err := time.ParseInLocation("2006-01-02", sinceText, time.Local)
		if err != nil {
			return fmt.Sprintf("错误: --since 需要 YYYY-MM-DD，收到 %q\n%s", sinceText, chatLSPBaselineUsage)
		}
		opts.Since = parsed
		windowText = "自 " + parsed.Format("2006-01-02")
	} else if days > 0 {
		opts.Since = time.Now().AddDate(0, 0, -days)
		windowText = fmt.Sprintf("最近 %d 天", days)
	}

	started := time.Now()
	stats, err := lspbaseline.Analyze(opts)
	if err != nil {
		return fmt.Sprintf("错误: 基线归因失败: %v", err)
	}
	report := lspbaseline.RenderMarkdown(stats)
	return fmt.Sprintf("%s\n（扫描根: %s；窗口: %s；整文件跳过 %d 个，按时间跳过 %d 行，耗时 %s）",
		report, strings.Join(roots, ", "), windowText, stats.Scan.SkippedFiles, stats.Scan.SkippedOld,
		time.Since(started).Round(time.Millisecond))
}
