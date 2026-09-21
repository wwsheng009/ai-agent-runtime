package transport

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// stdioCommand 描述最终要执行的 stdio 子进程。
//
// 绝大多数情况只需要 Path/Args。Windows 的 .cmd/.bat 垫片（npx.cmd、uvx.cmd、
// 企业内自研 bat 启动器）是唯一例外：这类脚本不能被 CreateProcess 直接执行，
// 必须经 `cmd.exe /d /s /c "<整条命令行>"` 包装。
//
// 关键点：这条命令行只能由我们自己拼好，并通过 syscall.SysProcAttr.CmdLine
// 原样下发给 CreateProcess。若仍走 os/exec 的 argv 拼装，Go 会用 MSVCRT 的
// `\"` 规则为每个 argv 元素加引号，而 cmd.exe 不识别该转义规则；当命令或参数
// 含空格时，cmd.exe 会把首个 token 截断成 `C:\Program` 之类的片段并报
// "is not recognized as an internal or external command"（详见
// docs/plan/aicli-windows-cmd-shim-compat-plan.md）。
type stdioCommand struct {
	Path string   // 交给 exec.Command 的程序（垫片场景固定为 cmd.exe）
	Args []string // 交给 exec.Command 的参数（日志/诊断用）
	// RawCmdLine 非空时，调用方必须把它写入 syscall.SysProcAttr.CmdLine 原样下发，
	// 不能让 os/exec 重新拼装。
	RawCmdLine string
}

// RawCmdLineExplicit 表示该命令必须使用原始命令行执行（Windows 垫片）。
func (c stdioCommand) RawCmdLineExplicit() bool {
	return strings.TrimSpace(c.RawCmdLine) != ""
}

// resolveStdioCommand 把 stdio 命令解析成 exec 可直接执行的形式。
//
// Windows 上 npx / uvx 等常见 MCP 启动器是 .cmd 垫片脚本：CreateProcess 无法
// 直接执行批处理，`exec.Command("npx", ...)` 会以 "not a valid application"
// 失败。这里按 PATHEXT（exec.LookPath 语义）解析出真实脚本，并用
// `cmd.exe /d /s /c <script> <args>` 包装；其余平台与非垫片命令原样返回
// （解析失败时保留原命令，让错误在启动阶段以原始形式暴露）。
//
// `/d` 跳过注册表 AutoRun（避免本机 AutoRun 脚本注入命令，且保证行为确定）；
// `/s` 让 cmd.exe 统一按「剥掉首尾引号」的规则处理 /c 之后的整串文本 —— 这与
// Node `child_process` / cross-spawn 的标准做法一致。
func resolveStdioCommand(command string, args []string) stdioCommand {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return stdioCommand{Path: command, Args: args}
	}
	if runtime.GOOS != "windows" {
		return stdioCommand{Path: command, Args: args}
	}

	target := trimmed
	if filepath.Ext(target) == "" {
		resolved, err := exec.LookPath(target)
		if err != nil {
			return stdioCommand{Path: command, Args: args}
		}
		target = resolved
	}
	if !isWindowsBatchShim(target) {
		return stdioCommand{Path: target, Args: args}
	}

	// 转义规则移植自 cross-spawn（Node 生态事实标准，MIT）：
	//   1) command 与每个 arg 分别按 cmd.exe 规则转义；
	//   2) 拼成一条完整命令行，再交给 `cmd.exe /d /s /c "<整条命令>"`；
	//   3) 由调用方写入 SysProcAttr.CmdLine（windowsVerbatimArguments 等价物）。
	doubleEscape := isNodeBinShim(target)
	inner := escapeCmdCommand(target)
	for _, arg := range args {
		inner += " " + escapeCmdArgument(arg, doubleEscape)
	}
	quoted := `"` + inner + `"`

	return stdioCommand{
		Path:       "cmd.exe",
		Args:       []string{"/d", "/s", "/c", quoted},
		RawCmdLine: "cmd.exe /d /s /c " + quoted,
	}
}

func isWindowsBatchShim(path string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".cmd", ".bat":
		return true
	default:
		return false
	}
}

// nodeBinShimPattern 对应 cross-spawn 的 isCmdShimRegExp：node_modules\.bin\*.cmd。
// 这类垫片内部会把 `%*` 再转交给 node，等于多一层解析，需要双重转义元字符。
var nodeBinShimPattern = regexp.MustCompile(`(?i)node_modules[\\/]\.bin[\\/][^\\/]+\.cmd$`)

func isNodeBinShim(path string) bool {
	return nodeBinShimPattern.MatchString(path)
}

// cmdMetaChars 是 cmd.exe 会解释的元字符集合（与 cross-spawn 的
// metaCharsRegExp `([()\][%!^"` + "`" + `<>&|;, *?])` 等价）。
const cmdMetaChars = "()[]%!^\"`<>&|;, *?"

// escapeCmdCommand 按 cross-spawn escape.command 的规则转义可执行文件路径：
// 逐个元字符加 `^`，避免路径中的空格、括号、`&` 等在 cmd.exe 解析时失去字面量语义。
func escapeCmdCommand(command string) string {
	return caretEscapeMetaChars(command)
}

// escapeCmdArgument 按 cross-spawn escape.argument 的规则转义单个参数。
//
// 算法来自 https://qntm.org/cmd ：
//  1. n 个反斜杠 + 一个双引号：反斜杠加倍，双引号保留（并作为整体被外层引号包裹）；
//  2. 结尾 n 个反斜杠：加倍（因为闭合引号会紧跟其后）；
//  3. 其余反斜杠保持字面量；
//  4. 整体加双引号，再对元字符加 `^`（引号本身也会被 `^"` 化，保证在 cmd 的首轮
//     解析中不切换引号状态，从而能穿透到 .cmd 垫片的下一层解析）。
//
// doubleEscape 为 true 时（node_modules\.bin\*.cmd 垫片）再转义一遍。
func escapeCmdArgument(arg string, doubleEscape bool) string {
	escaped := doubleTrailingBackslashes(doubleBackslashesBeforeQuotes(arg))
	quoted := escapeCmdCommand(`"` + escaped + `"`)
	if doubleEscape {
		quoted = escapeCmdCommand(quoted)
	}
	return quoted
}

func caretEscapeMetaChars(s string) string {
	hasMeta := false
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(cmdMetaChars, s[i]) >= 0 {
			hasMeta = true
			break
		}
	}
	if !hasMeta {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(cmdMetaChars, s[i]) >= 0 {
			b.WriteByte('^')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// doubleBackslashesBeforeQuotes 实现 qntm 规则 1 / cross-spawn 的
// `/(?=(\\+?)?)\1"/g → '$1$1\\"'`：n 个反斜杠 + `"` → 2n 个反斜杠 + `\"`。
// 反斜杠转义服务于下一层（.cmd 垫片之后的 node/CRT argv 解析），cmd.exe 自身的
// 首轮解析由外层 `^"` 承担。
func doubleBackslashesBeforeQuotes(s string) string {
	if !strings.Contains(s, `"`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	run := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			run++
		case '"':
			writeRepeated(&b, '\\', run*2)
			b.WriteByte('\\')
			b.WriteByte('"')
			run = 0
		default:
			writeRepeated(&b, '\\', run)
			run = 0
			b.WriteByte(s[i])
		}
	}
	writeRepeated(&b, '\\', run)
	return b.String()
}

// doubleTrailingBackslashes 实现 qntm 规则 2：结尾 n 个反斜杠 → 2n 个反斜杠。
func doubleTrailingBackslashes(s string) string {
	n := 0
	for n < len(s) && s[len(s)-1-n] == '\\' {
		n++
	}
	if n == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + n)
	b.WriteString(s)
	writeRepeated(&b, '\\', n)
	return b.String()
}

func writeRepeated(b *strings.Builder, ch byte, count int) {
	for i := 0; i < count; i++ {
		b.WriteByte(ch)
	}
}
