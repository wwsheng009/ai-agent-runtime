// Package termopen 在「一个新的可见终端窗口」里启动一条命令。
//
// 为什么单独成包：会话列表的「在新终端里打开这个会话」、`aicli resume <id> --detach`
// 以及 gateway/mesh 管理端的「拉起本地窗口」需要同一个能力，而这三处分别位于
// commands 与 mesh/gateway 两个方向。放在 internal 层，三条入口共用一份平台判定，
// 避免出现第二套「怎么开终端」的方言。
//
// 与 internal/executor 的分工：executor.StartDetached 负责「脱离父进程存活」
// （Windows CREATE_NEW_CONSOLE 且不继承 stdio；Unix setsid + null 设备），本包只
// 负责「用哪个终端程序、带什么参数、在哪个目录」。new_console 后端把 aicli 自己
// 交给 StartDetached，于是 Windows 上拿到的是一个真正的控制台窗口。
//
// 失败面收敛成三类：没有可用终端（程序缺失）、参数无法构造（目标为空）、进程启动
// 失败（平台错误）。调用方据此能给出「照着修」的一句话，而不是静默失败的窗口。
package termopen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// EnvTerminal 是终端后端覆盖：值既可以是本表里的后端名（gnome_terminal），
// 也可以是某个终端程序的路径或命令名（/usr/bin/my-term）。
const EnvTerminal = "AICLI_TERMINAL"

// EnvTerminalStrict 打开后，Detect 探测不到终端就直接报错，不做静默兜底。
// 适合 CI 与远程拉起：宁可不弹窗，也不要在错误的终端里弹窗。
const EnvTerminalStrict = "AICLI_TERMINAL_STRICT"

// 后端名（对外可见：UI 展示、错误信息、审计日志共用这一份标识）。
const (
	BackendWindowsTerminal = "windows_terminal" // wt.exe
	BackendNewConsole      = "new_console"      // 原生分离启动（Windows 会开新控制台窗口）
	BackendTerminalApp     = "terminal_app"     // macOS Terminal.app（osascript）
	BackendITerm2          = "iterm2"           // macOS iTerm（osascript）
	BackendGnomeTerminal   = "gnome_terminal"
	BackendKonsole         = "konsole"
	BackendXfceTerminal    = "xfce_terminal"
	BackendKitty           = "kitty"
	BackendAlacritty       = "alacritty"
	BackendWezTerm         = "wezterm"
	BackendFoot            = "foot"
	BackendTerminator      = "terminator"
	BackendXterm           = "xterm"
	BackendXTermEmulator   = "x_terminal_emulator"
	BackendCustom          = "custom" // AICLI_TERMINAL 指向的未知程序，走 -e "<命令行>" 约定
)

// Request 描述一次终端拉起。Binary+Args 是「要跑的东西」，不是终端程序本身。
type Request struct {
	// Title 是窗口标题（尽力而为：并非所有后端都支持）。
	Title string
	// Dir 是子进程的工作目录。会话的工作区锚点由调用方给出；空则继承本进程。
	Dir string
	// Binary 是要在新终端里执行的程序（aicli 的绝对路径最合适）。
	Binary string
	// Args 是 Binary 的参数向量（不含程序名）。
	Args []string
	// Env 是追加到当前进程环境里的 KEY=VALUE 项；同名键覆盖。nil 表示原样继承。
	Env []string
}

// Plan 是一次拉起的确定性方案：可单测、可展示、可复制。
type Plan struct {
	Backend string
	// Path 是真正被启动的程序：终端模拟器（或 macOS 的 osascript）；
	// new_console 后端时就是 Request.Binary 本身。
	Path string
	Args []string
	Dir  string
	// Env 是交给平台的完整环境（当前进程环境 + Request.Env）。
	// executor 的 Unix 分支把 nil Env 当成「空环境」，所以这里永远给出具体值。
	Env []string
	// ShellCommand 是内嵌给 `-e "<cmd>"` 型终端与 AppleScript 的等价命令行；
	// 不使用它的后端为空串。展示时用它解释「窗口里到底会跑什么」。
	ShellCommand string
}

// Result 是一次成功拉起的结果。PID 属于被启动的终端宿主（new_console 时即目标程序）。
type Result struct {
	Plan Plan
	PID  int
	Mode string
}

// Launcher 把平台判定与进程启动收进一个可注入对象，供测试替换。
type Launcher struct {
	GOOS     string
	LookPath func(string) (string, error)
	Getenv   func(string) string
	// Environ 提供基础环境；nil 时用 os.Environ。
	Environ func() []string
	// Start 是分离启动器；nil 时用 executor.StartDetached。
	Start func(runtimeexecutor.DetachedLaunch) (*runtimeexecutor.DetachedProcess, error)
}

// Default 返回按当前进程环境装配的 Launcher。
func Default() *Launcher {
	return &Launcher{
		GOOS:     runtime.GOOS,
		LookPath: exec.LookPath,
		Getenv:   os.Getenv,
		Environ:  os.Environ,
		Start:    runtimeexecutor.StartDetached,
	}
}

// Detect 返回当前可用的后端名。
func (l *Launcher) Detect() (string, error) {
	l = l.orDefault()
	if override := strings.TrimSpace(l.getenv(EnvTerminal)); override != "" {
		if name := normalizeBackendName(override); isKnownBackend(name) {
			return name, nil
		}
		// 指到具体程序：先验证存在性，否则用户会看到「启动失败」而实际是
		// 「根本没有这个程序」。
		if _, err := l.lookPath(override); err == nil {
			return BackendCustom, nil
		}
		return "", fmt.Errorf("%s=%s 既不是已知后端也不是可执行程序", EnvTerminal, override)
	}
	// 桌面环境自己声明的终端（$TERMINAL 等）优先于按名字扫描。
	for _, key := range terminalProgramEnv {
		value := strings.TrimSpace(l.getenv(key))
		if value == "" {
			continue
		}
		if name := normalizeBackendName(value); isKnownBackend(name) {
			return name, nil
		}
		if _, err := l.lookPath(value); err == nil {
			return BackendCustom, nil
		}
	}
	for _, name := range l.order() {
		spec := backendTable[name]
		if spec.probeless {
			return name, nil
		}
		if _, err := l.lookPath(spec.program); err == nil {
			return name, nil
		}
	}
	if l.strict() {
		return "", fmt.Errorf("%s 已启用但没找到可用终端（设置 %s=<后端名或终端程序路径>）", EnvTerminalStrict, EnvTerminal)
	}
	return "", fmt.Errorf("当前环境没有可用的终端程序（设置 %s=<后端名或终端程序路径>）", EnvTerminal)
}

// Describe 给出一句人类可读的后端说明，供 UI 与日志展示。
func (l *Launcher) Describe() string {
	backend, err := l.Detect()
	if err != nil {
		return err.Error()
	}
	spec := backendTable[backend]
	if spec.program == "" || spec.probeless {
		return backend
	}
	resolved, err := l.lookPath(spec.program)
	if err != nil {
		return backend + " (" + spec.program + " 未找到)"
	}
	return backend + " (" + filepath.Base(resolved) + ")"
}

// Build 组装一次拉起的 Plan，不启动进程。
func (l *Launcher) Build(req Request) (Plan, error) {
	l = l.orDefault()
	binary := strings.TrimSpace(req.Binary)
	if binary == "" {
		return Plan{}, fmt.Errorf("termopen: 要执行的程序为空")
	}
	req.Binary = binary
	backend, err := l.Detect()
	if err != nil {
		return Plan{}, err
	}
	spec := backendTable[backend]
	launcherPath := binary
	if !spec.self {
		program := spec.program
		if backend == BackendCustom {
			// 宿主程序名来自 AICLI_TERMINAL/$TERMINAL 本身。
			program = firstCustomProgram(l)
		}
		resolved, lookErr := l.lookPath(program)
		if lookErr != nil {
			// Detect 之后程序被删/移走（PATH 变更）：报可读错误而不是启动失败。
			return Plan{}, fmt.Errorf("termopen: 终端程序 %s 不可用: %w", program, lookErr)
		}
		launcherPath = resolved
	}
	shell := ShellCommand(req)
	args, err := spec.argv(launcherPath, req, shell)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		Backend: backend,
		Path:    launcherPath,
		Args:    args,
		Dir:     strings.TrimSpace(req.Dir),
		Env:     l.mergedEnv(req),
	}
	// new_console 直接跑目标程序，没有「内嵌命令行」可展示；其余后端的
	// ShellCommand 只在 -e 单字符串形态下才有意义，其余留空避免 UI 误读。
	if spec.self || !spec.usesShell {
		plan.ShellCommand = ""
	} else {
		plan.ShellCommand = shell
	}
	return plan, nil
}

// Launch 在新终端里启动 req。
func (l *Launcher) Launch(req Request) (Result, error) {
	l = l.orDefault()
	plan, err := l.Build(req)
	if err != nil {
		return Result{}, err
	}
	start := l.Start
	if start == nil {
		start = runtimeexecutor.StartDetached
	}
	proc, err := start(runtimeexecutor.DetachedLaunch{
		Path: plan.Path,
		Args: plan.Args,
		Dir:  plan.Dir,
		Env:  plan.Env,
	})
	if err != nil {
		return Result{}, fmt.Errorf("termopen: 在新终端启动（后端 %s）失败: %w", plan.Backend, err)
	}
	return Result{Plan: plan, PID: proc.PID, Mode: proc.Mode}, nil
}

func (l *Launcher) orDefault() *Launcher {
	if l != nil {
		return l
	}
	fallback := Default()
	return fallback
}

func (l *Launcher) order() []string {
	if order, ok := candidateOrder[l.GOOS]; ok {
		return order
	}
	return candidateOrder[fallbackGOOSKey]
}

func (l *Launcher) strict() bool {
	switch strings.ToLower(strings.TrimSpace(l.getenv(EnvTerminalStrict))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (l *Launcher) getenv(key string) string {
	if l.Getenv == nil {
		return ""
	}
	return l.Getenv(key)
}

func (l *Launcher) lookPath(name string) (string, error) {
	if name == "" {
		return "", exec.ErrNotFound
	}
	if l.LookPath == nil {
		return exec.LookPath(name)
	}
	return l.LookPath(name)
}

// mergedEnv 是交给平台的完整环境：当前进程环境 + 请求追加项（同名覆盖）。
func (l *Launcher) mergedEnv(req Request) []string {
	base := []string(nil)
	if l.Environ != nil {
		base = l.Environ()
	} else {
		base = os.Environ()
	}
	if len(req.Env) == 0 {
		return append([]string(nil), base...)
	}
	return mergeEnv(base, req.Env)
}

func firstCustomProgram(l *Launcher) string {
	for _, key := range append([]string{EnvTerminal}, terminalProgramEnv...) {
		if value := strings.TrimSpace(l.getenv(key)); value != "" {
			if _, ok := backendTable[normalizeBackendName(value)]; !ok {
				return value
			}
		}
	}
	return ""
}

// Launch 用默认平台装配启动一次。
func Launch(req Request) (Result, error) { return Default().Launch(req) }

// Detect 用默认平台装配探测后端。
func Detect() (string, error) { return Default().Detect() }

// Describe 用默认平台装配描述后端。
func Describe() string { return Default().Describe() }

// Available 报告当前是否存在可用终端后端（UI 据此决定要不要展示该动作）。
func Available() bool {
	_, err := Default().Detect()
	return err == nil
}

// isKnownBackend 报告 name 是否是后端表里的成员（后端名大小写/连字符已归一）。
func isKnownBackend(name string) bool {
	_, ok := backendTable[name]
	return ok
}

// candidateOrder 是各平台自动探测的优先级。Windows 先试 Windows Terminal，
// 再用原生新控制台；类 Unix 先试桌面环境声明的终端，再按常见程度扫描。
var candidateOrder = map[string][]string{
	"windows": {BackendWindowsTerminal, BackendNewConsole},
	"darwin":  {BackendTerminalApp},
	"linux":   {BackendGnomeTerminal, BackendKonsole, BackendKitty, BackendWezTerm, BackendAlacritty, BackendXfceTerminal, BackendTerminator, BackendFoot, BackendXterm, BackendXTermEmulator},
}

// fallbackGOOSKey 用于 GOOS 不在表里（*BSD、Solaris…）：按桌面常见顺序试。
const fallbackGOOSKey = "linux"

// terminalProgramEnv 是桌面环境通常写明「我的终端」的变量，命中即优先。
var terminalProgramEnv = []string{"TERMINAL_PROGRAM", "XDG_TERMINAL", "TERMINAL"}

// backendSpec 描述一个终端后端的参数形状。每个 argv 函数只拼参数，判定全在
// Detect/Build 里，这样本表可以逐项单测而完全不碰进程。
type backendSpec struct {
	// program 是终端宿主程序名（new_console 为空：直接启动目标程序）。
	program string
	// self 为真表示不经过终端程序，把目标程序本身分离启动。
	self bool
	// probeless 为真表示无需 LookPath（新控制台，以及 macOS 上必然存在的
	// osascript）。
	probeless bool
	// usesShell 标记该后端把命令交给 shell 字符串（`-e "<cmd>"` / AppleScript），
	// 此时 Plan.ShellCommand 才具有「窗口里跑的是什么」的解释力。
	usesShell bool
	argv      func(host string, req Request, shell string) ([]string, error)
}

// backendTable 是全部后端的参数形状。argv 形态的后端把 Dir 交给宿主自己的
// 工作目录选项；只接受单字符串命令的后端靠 `cd <dir> && …` 承载目录。
var backendTable = map[string]backendSpec{
	BackendWindowsTerminal: {
		program: "wt.exe",
		argv: func(_ string, req Request, _ string) ([]string, error) {
			args := []string{}
			if dir := strings.TrimSpace(req.Dir); dir != "" {
				args = append(args, "-d", dir)
			}
			if title := strings.TrimSpace(req.Title); title != "" {
				args = append(args, "--title", title)
			}
			return append(args, launchArgv(req)...), nil
		},
	},
	BackendNewConsole: {
		probeless: true,
		self:      true,
		argv: func(_ string, req Request, _ string) ([]string, error) {
			return append([]string(nil), req.Args...), nil
		},
	},
	BackendTerminalApp: {
		program:   "osascript",
		probeless: true,
		usesShell: true,
		argv:      appleScriptArgv("Terminal"),
	},
	BackendITerm2: {
		program:   "osascript",
		probeless: true,
		usesShell: true,
		argv:      appleScriptArgv("iTerm"),
	},
	BackendGnomeTerminal: {
		program: "gnome-terminal",
		argv: func(_ string, req Request, _ string) ([]string, error) {
			args := []string{}
			if dir := strings.TrimSpace(req.Dir); dir != "" {
				args = append(args, "--working-directory="+dir)
			}
			if title := strings.TrimSpace(req.Title); title != "" {
				args = append(args, "--title="+title)
			}
			// gnome-terminal 的 `--` 之后是 argv 形态（不是单字符串 -e）。
			return append(args, append([]string{"--"}, launchArgv(req)...)...), nil
		},
	},
	BackendKonsole: {
		program: "konsole",
		argv: func(_ string, req Request, _ string) ([]string, error) {
			args := []string{}
			if dir := strings.TrimSpace(req.Dir); dir != "" {
				args = append(args, "-d", dir)
			}
			if title := strings.TrimSpace(req.Title); title != "" {
				args = append(args, "--title", title)
			}
			return append(args, append([]string{"-e"}, launchArgv(req)...)...), nil
		},
	},
	BackendXfceTerminal: {
		program:   "xfce4-terminal",
		usesShell: true,
		argv: func(_ string, req Request, shell string) ([]string, error) {
			args := []string{}
			if dir := strings.TrimSpace(req.Dir); dir != "" {
				args = append(args, "--working-directory="+dir)
			}
			if title := strings.TrimSpace(req.Title); title != "" {
				args = append(args, "--title="+title)
			}
			return append(args, "--command="+shell), nil
		},
	},
	BackendKitty: {
		program: "kitty",
		argv: func(_ string, req Request, _ string) ([]string, error) {
			args := []string{}
			if dir := strings.TrimSpace(req.Dir); dir != "" {
				args = append(args, "--directory="+dir)
			}
			if title := strings.TrimSpace(req.Title); title != "" {
				args = append(args, "--title="+title)
			}
			return append(args, launchArgv(req)...), nil
		},
	},
	BackendAlacritty: {
		program: "alacritty",
		argv: func(_ string, req Request, _ string) ([]string, error) {
			args := []string{}
			if dir := strings.TrimSpace(req.Dir); dir != "" {
				args = append(args, "--working-directory", dir)
			}
			if title := strings.TrimSpace(req.Title); title != "" {
				args = append(args, "--title", title)
			}
			return append(args, append([]string{"-e"}, launchArgv(req)...)...), nil
		},
	},
	BackendWezTerm: {
		program: "wezterm",
		argv: func(_ string, req Request, _ string) ([]string, error) {
			args := []string{"start"}
			if dir := strings.TrimSpace(req.Dir); dir != "" {
				args = append(args, "--cwd", dir)
			}
			if title := strings.TrimSpace(req.Title); title != "" {
				args = append(args, "--title", title)
			}
			return append(args, append([]string{"--"}, launchArgv(req)...)...), nil
		},
	},
	BackendFoot: {
		program: "foot",
		argv: func(_ string, req Request, _ string) ([]string, error) {
			args := []string{}
			if dir := strings.TrimSpace(req.Dir); dir != "" {
				args = append(args, "--working-directory="+dir)
			}
			if title := strings.TrimSpace(req.Title); title != "" {
				args = append(args, "--title="+title)
			}
			return append(args, launchArgv(req)...), nil
		},
	},
	BackendTerminator: {
		program:   "terminator",
		usesShell: true,
		argv: func(_ string, req Request, shell string) ([]string, error) {
			args := []string{}
			if dir := strings.TrimSpace(req.Dir); dir != "" {
				args = append(args, "--working-directory="+dir)
			}
			return append(args, "-e", shell), nil
		},
	},
	BackendXterm: {
		program:   "xterm",
		usesShell: true,
		argv: func(_ string, req Request, shell string) ([]string, error) {
			args := []string{}
			if title := strings.TrimSpace(req.Title); title != "" {
				args = append(args, "-T", title)
			}
			// xterm 没有工作目录选项：目录靠 shell 命令行里的 cd 承载。
			return append(args, "-e", "sh", "-c", shell), nil
		},
	},
	BackendXTermEmulator: {
		program:   "x-terminal-emulator",
		usesShell: true,
		argv: func(_ string, _ Request, shell string) ([]string, error) {
			// x-terminal-emulator 只是 alternatives 包装，参数按最保守的
			// `-e sh -c "<命令行>"` 形态传递。
			return []string{"-e", "sh", "-c", shell}, nil
		},
	},
	BackendCustom: {
		program:   "",
		usesShell: true,
		argv: func(_ string, _ Request, shell string) ([]string, error) {
			return []string{"-e", "sh", "-c", shell}, nil
		},
	},
}

// appleScriptArgv 生成把命令行交给 Terminal.app / iTerm 的 osascript 参数。
//
// AppleScript 字符串只能双引号，内部反斜杠与引号都要转义；命令行本身再走一层
// POSIX 单引号（quotePOSIX），于是「用户 argv 里有空格或引号」也拼不坏脚本。
func appleScriptArgv(app string) func(string, Request, string) ([]string, error) {
	return func(_ string, _ Request, shell string) ([]string, error) {
		lines := []string{
			"tell application " + quoteAppleScript(app),
			"activate",
			"do script " + quoteAppleScript(shell),
			"end tell",
		}
		args := make([]string, 0, len(lines)*2)
		for _, line := range lines {
			args = append(args, "-e", line)
		}
		return args, nil
	}
}

// launchArgv 是「目标程序 + 参数」的完整向量。
func launchArgv(req Request) []string {
	out := make([]string, 0, len(req.Args)+1)
	out = append(out, strings.TrimSpace(req.Binary))
	out = append(out, req.Args...)
	return out
}

// ShellCommand 把 Request 渲染成一条 shell 命令行（先 cd 到目录再执行），供
// `-e "<cmd>"` 型终端与 AppleScript 使用。所有 token 走 POSIX 单引号，绝不信任拼接。
func ShellCommand(req Request) string {
	parts := make([]string, 0, len(req.Args)+2)
	if dir := strings.TrimSpace(req.Dir); dir != "" {
		parts = append(parts, "cd "+quotePOSIX(dir), "&&")
	}
	parts = append(parts, quotePOSIX(strings.TrimSpace(req.Binary)))
	for _, arg := range req.Args {
		parts = append(parts, quotePOSIX(arg))
	}
	return strings.Join(parts, " ")
}

// quotePOSIX 用单引号包住 token，内部单引号按 '\” 展开。
// 纯安全字符（不含空格与 shell 元字符）不加引号，保证展示出来的命令行可读。
func quotePOSIX(token string) string {
	if token == "" {
		return "''"
	}
	if !strings.ContainsAny(token, " \t\n\"'\\$`;&|<>()*?[]{}#!~=") && !strings.HasPrefix(token, "-") {
		return token
	}
	return "'" + strings.ReplaceAll(token, "'", `'\''`) + "'"
}

// quoteAppleScript 把字符串渲染成 AppleScript 双引号字面量。
func quoteAppleScript(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// normalizeBackendName 接受 AICLI_TERMINAL 里的大小写、连字符与点号写法
// （gnome-terminal / GNOME_TERMINAL 指向同一后端）。
func normalizeBackendName(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	trimmed = strings.ReplaceAll(trimmed, "-", "_")
	trimmed = strings.ReplaceAll(trimmed, ".", "_")
	return trimmed
}

// mergeEnv 用 override 覆盖 base 中的同名键，保持 base 顺序、追加新键。
func mergeEnv(base, override []string) []string {
	byKey := map[string]int{}
	merged := make([]string, 0, len(base)+len(override))
	for _, entry := range base {
		key := envKey(entry)
		if key == "" {
			continue
		}
		byKey[strings.ToUpper(key)] = len(merged)
		merged = append(merged, entry)
	}
	for _, entry := range override {
		key := envKey(entry)
		if key == "" {
			continue
		}
		upper := strings.ToUpper(key)
		if index, ok := byKey[upper]; ok {
			merged[index] = entry
			continue
		}
		byKey[upper] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

func envKey(entry string) string {
	index := strings.IndexByte(entry, '=')
	if index <= 0 {
		return ""
	}
	return entry[:index]
}

// AbsolutePathForTerminal 把可能相对的路径归一成绝对路径：新终端的工作目录由后端
// 决定，不能依赖当前进程的相对路径解析结果。
func AbsolutePathForTerminal(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return trimmed
	}
	if resolved, err := exec.LookPath(trimmed); err == nil {
		return resolved
	}
	if absolute, err := filepath.Abs(trimmed); err == nil {
		return absolute
	}
	return trimmed
}
