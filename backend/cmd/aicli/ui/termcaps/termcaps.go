// Package termcaps 汇总「当前终端对交互式按键/粘贴能力的支持情况」。
//
// 目标是把「某终端上哪个键真正可用」变成可展示、可断言的事实：探测结果 +
// 一行原因，未知按保守值处理，绝不静默降级。探测只依赖环境变量与平台，
// 不做任何副作用调用，便于单测覆盖各终端矩阵。
package termcaps

import "strings"

// Kind 是识别出的终端类型（粗粒度，足够决定按键矩阵）。
type Kind string

const (
	KindWindowsTerminal Kind = "windows-terminal"
	KindWSL             Kind = "wsl"
	KindVSCode          Kind = "vscode"
	KindWezTerm         Kind = "wezterm"
	KindConEmu          Kind = "conemu"
	KindMintty          Kind = "mintty"
	KindITerm2          Kind = "iterm2"
	KindAppleTerminal   Kind = "apple-terminal"
	KindLegacyConsole   Kind = "legacy-console"
	KindXterm           Kind = "xterm"
	KindUnknown         Kind = "unknown"
)

// Support 描述单项能力的可用性与原因（原因可直接展示给用户）。
type Support struct {
	Supported bool
	Reason    string
}

// Capabilities 是当前终端的能力矩阵。
type Capabilities struct {
	Kind           Kind
	Name           string
	ShiftTab       Support
	AltM           Support
	ModifiedEnter  Support
	BracketedPaste Support
	ClipboardText  Support
	ClipboardImage Support
	Notes          []string
}

// Detect 基于环境变量与平台给出能力矩阵。getenv 允许注入（测试用），
// 传 nil 表示空环境；stdoutIsTTY 表示标准输出是否为终端。
func Detect(getenv func(string) string, goos string, stdoutIsTTY bool) Capabilities {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	kind, name := detectKind(getenv, goos)
	caps := Capabilities{Kind: kind, Name: name}
	interactive := stdoutIsTTY

	switch kind {
	case KindLegacyConsole:
		caps.ShiftTab = Support{false, "传统 conhost 常吞掉 CSI Z；请用 alt+m 或 /permission-mode"}
		caps.ModifiedEnter = Support{false, "conhost 不上报 CSI 13;2u；请用 ctrl+j / ctrl+o 换行"}
		caps.BracketedPaste = Support{false, "conhost 不识别 2004 序列，粘贴按普通输入插入"}
	case KindVSCode:
		caps.ShiftTab = Support{true, "CSI Z 可用；IDE 可能抢占该组合键，失效时请用 alt+m"}
		caps.ModifiedEnter = Support{true, "支持 CSI 13;2u 的 IDE 终端；ctrl+j / ctrl+o 始终可用"}
		caps.BracketedPaste = Support{true, "已启用 bracketed paste"}
	case KindMintty:
		caps.ShiftTab = Support{true, "CSI Z 可用"}
		caps.AltM = Support{true, "ESC+m；若终端把 Alt 设为 Meta 前缀仍可识别"}
		caps.ModifiedEnter = Support{true, "依赖终端对 CSI 13;2u 的透传；ctrl+j / ctrl+o 始终可用"}
		caps.BracketedPaste = Support{true, "已启用 bracketed paste"}
	case KindWindowsTerminal, KindWSL, KindWezTerm, KindConEmu, KindITerm2, KindAppleTerminal, KindXterm:
		caps.ShiftTab = Support{true, "CSI Z 可用"}
		caps.ModifiedEnter = Support{true, "支持 modified-enter；ctrl+j / ctrl+o 始终可用"}
		caps.BracketedPaste = Support{true, "已启用 bracketed paste"}
	default:
		caps.ShiftTab = Support{false, "未识别的终端：默认不承诺 shift+tab，请用 alt+m 或 /permission-mode"}
		caps.ModifiedEnter = Support{true, "按 modified-enter 处理；ctrl+j / ctrl+o 始终可用"}
		caps.BracketedPaste = Support{true, "已启用 bracketed paste"}
	}

	if !caps.AltM.Supported && caps.AltM.Reason == "" {
		caps.AltM = Support{true, "ESC+m（alt 与 ESC 同码，编辑器按前缀区分）"}
	}

	if goos == "windows" {
		caps.ClipboardText = Support{true, "通过 Windows 剪贴板 API 读取 Unicode 文本"}
	} else {
		caps.ClipboardText = Support{false, "未实现 Unix 剪贴板读取；请用终端自身粘贴（Ctrl+Shift+V / Cmd+V），它走 bracketed paste"}
	}
	// 剪贴板图片能力依赖运行平台与外部工具，属于运行时事实而非环境变量推导结果；
	// Detect 保持纯函数，调用方（/hotkeys 命令）用 clipboardimage.Availability() 覆盖此行。
	caps.ClipboardImage = Support{false, "未探测（/hotkeys 会按运行平台覆盖该行）；可先用 /attach <path> 添加图片附件"}

	if !interactive {
		caps.Notes = append(caps.Notes, "标准输出不是终端：按键与粘贴能力不适用（headless/JSON 输出）")
	}
	if kind == KindWSL {
		caps.Notes = append(caps.Notes, "WSL 下的剪贴板/图片能力由宿主终端决定，终端内粘贴走 bracketed paste")
	}
	return caps
}

func detectKind(getenv func(string) string, goos string) (Kind, string) {
	if strings.TrimSpace(getenv("WT_SESSION")) != "" {
		if distro := strings.TrimSpace(getenv("WSL_DISTRO_NAME")); distro != "" {
			return KindWSL, "WSL（Windows Terminal 宿主：" + distro + "）"
		}
		return KindWindowsTerminal, "Windows Terminal"
	}
	switch strings.TrimSpace(getenv("TERM_PROGRAM")) {
	case "vscode":
		return KindVSCode, "VS Code 集成终端"
	case "WezTerm":
		return KindWezTerm, "WezTerm"
	case "mintty":
		return KindMintty, "mintty / Git Bash"
	case "iTerm.app":
		return KindITerm2, "iTerm2"
	case "Apple_Terminal":
		return KindAppleTerminal, "Apple Terminal"
	}
	if strings.TrimSpace(getenv("ConEmuPID")) != "" || strings.TrimSpace(getenv("ConEmuANSI")) != "" {
		return KindConEmu, "ConEmu / Cmder"
	}
	if distro := strings.TrimSpace(getenv("WSL_DISTRO_NAME")); distro != "" {
		return KindWSL, "WSL（宿主终端未知：" + distro + "）"
	}
	if goos == "windows" {
		return KindLegacyConsole, "Windows 传统控制台 (conhost)"
	}
	if term := strings.TrimSpace(getenv("TERM")); term != "" && term != "dumb" {
		return KindXterm, term
	}
	return KindUnknown, "未识别的终端"
}

// Describe 生成一行「可用/不可用 + 原因」的展示文本。
func (s Support) Describe() string {
	label := "不可用"
	if s.Supported {
		label = "可用"
	}
	if reason := strings.TrimSpace(s.Reason); reason != "" {
		return label + "（" + reason + "）"
	}
	return label
}
