package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// 本文件实现 MCP 配置的环境变量插值，语法对齐 CommandCode 文档并在
// 兼容旧行为的前提下收紧语义：
//
//	${NAME}          严格：变量未设置（LookupEnv 为 false）时记入该 server 的
//	                 EnvError（错误信息指名变量）；变量已设置但为空串视为合法。
//	${NAME:-default} 变量未设置或为空串时使用 default（default 为字面量，
//	                 不做二次展开，且不支持嵌套 } 字符）。
//	$${NAME}         转义：展开为字面量 ${NAME}。
//	$NAME            历史语法（等价 os.ExpandEnv）：变量未设置时展开为空串，
//	                 仅在 url/command/env 值这些历史字段上保留。
//
// 展开范围与模式：
//   - url / command / env 值：历史字段，完整语法（含 $NAME 宽松展开）；
//   - args / headers 值：新增展开字段，只识别 ${...} / $${...} 显式语法，
//     裸 $ 原样保留，避免误伤 "$1"、"Bearer ab$cd" 这类字面量。
//
// 缺失变量按 server 隔离：写入 MCPConfig.EnvError 而不是让整个配置加载失败，
// 这样 `mcp list/status/add/remove` 等管理操作与其它 server 不受影响，
// manager 启动时会把该 server 标记为失败并在状态里带出 EnvError。

// ExpandEnv 就地展开配置中的环境变量引用，并把缺失项记录到各 server 的 EnvError。
func ExpandEnv(cfg *Config) {
	if cfg == nil || len(cfg.MCPServers) == 0 {
		return
	}
	for name, mcp := range cfg.MCPServers {
		missing := map[string]struct{}{}
		mcp.URL = expandEnvValue(mcp.URL, missing, true)
		mcp.Command = expandEnvValue(mcp.Command, missing, true)
		for i, arg := range mcp.Args {
			mcp.Args[i] = expandEnvValue(arg, missing, false)
		}
		for key, value := range mcp.Env {
			mcp.Env[key] = expandEnvValue(value, missing, true)
		}
		for key, value := range mcp.Headers {
			mcp.Headers[key] = expandEnvValue(value, missing, false)
		}
		mcp.EnvError = formatMissingEnvError(missing)
		cfg.MCPServers[name] = mcp
	}
}

func formatMissingEnvError(missing map[string]struct{}) string {
	if len(missing) == 0 {
		return ""
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("引用未设置的环境变量: %s（可写成 ${VAR:-默认值} 提供默认值，或 $${VAR} 表示字面量）", strings.Join(names, ", "))
}

// expandEnvValue 展开单个字符串；legacy 为 true 时额外支持 `$NAME` 历史语法。
func expandEnvValue(input string, missing map[string]struct{}, legacy bool) string {
	if input == "" || !strings.Contains(input, "$") {
		return input
	}
	var b strings.Builder
	b.Grow(len(input) + 8)
	for i := 0; i < len(input); {
		if input[i] != '$' {
			b.WriteByte(input[i])
			i++
			continue
		}
		// `$${...}` 转义：输出字面量 ${...}（保留原内容，不再展开）。
		if i+2 < len(input) && input[i+1] == '$' && input[i+2] == '{' {
			if end := strings.IndexByte(input[i+2:], '}'); end >= 0 {
				b.WriteString(input[i+1 : i+2+end+1])
				i = i + 2 + end + 1
				continue
			}
		}
		// `${NAME}` / `${NAME:-default}`。
		if i+1 < len(input) && input[i+1] == '{' {
			if end := strings.IndexByte(input[i+1:], '}'); end >= 0 {
				body := input[i+2 : i+1+end]
				if name, def, hasDefault, ok := parseBracedEnvBody(body); ok {
					value, exists := os.LookupEnv(name)
					switch {
					case exists && (value != "" || !hasDefault):
						b.WriteString(value)
					case hasDefault:
						b.WriteString(def)
					case exists:
						// 已设置但为空且无默认值：合法，展开为空串。
					default:
						missing[name] = struct{}{}
					}
					i = i + 1 + end + 1
					continue
				}
			}
			// 非法或缺右花括号：按字面量处理裸 $。
			b.WriteByte('$')
			i++
			continue
		}
		// 历史 `$NAME` / `$特殊字符`（仅 legacy 字段）。
		if legacy {
			if name, width := shellVarName(input[i+1:]); width > 0 && name != "" {
				b.WriteString(os.Getenv(name))
				i += 1 + width
				continue
			}
		}
		b.WriteByte('$')
		i++
	}
	return b.String()
}

// parseBracedEnvBody 解析 `${...}` 内部内容。
// 仅接受 shell 变量名（[A-Za-z_][A-Za-z0-9_]*）与可选的 `:-default` 后缀。
func parseBracedEnvBody(body string) (name, def string, hasDefault, ok bool) {
	if body == "" {
		return "", "", false, false
	}
	name = body
	if idx := strings.Index(body, ":-"); idx >= 0 {
		name = body[:idx]
		def = body[idx+2:]
		hasDefault = true
	}
	if !isShellVarName(name) {
		return "", "", false, false
	}
	return name, def, hasDefault, true
}

func isShellVarName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// shellVarName 复刻 os.Expand 的变量名识别规则（不含 `${`，由调用方处理）。
func shellVarName(s string) (name string, width int) {
	if s == "" {
		return "", 0
	}
	if isShellSpecialVar(s[0]) {
		return s[0:1], 1
	}
	i := 0
	for i < len(s) && isShellAlphaNum(s[i]) {
		i++
	}
	return s[:i], i
}

func isShellSpecialVar(c byte) bool {
	switch c {
	case '*', '#', '$', '@', '!', '?', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return true
	}
	return false
}

func isShellAlphaNum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}
