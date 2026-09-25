package skill

import (
	"strconv"
	"strings"
)

// SubstitutionContext 描述一次 skill 文本替换所需的运行时变量。
//
// 语义对齐 Agent Skills 标准的占位符：`$ARGUMENTS`、`$ARGUMENTS[N]`、`${N}`、
// 声明式命名参数，以及 `${SKILL_DIR}` / `${PROJECT_DIR}` / `${SESSION_ID}` /
// `${EFFORT}`（含 CLAUDE_* / COMMANDCODE_* 兼容别名）。
type SubstitutionContext struct {
	// Enabled 为 false 时直接返回原文（skills_runtime.argument_substitution=off）。
	Enabled bool
	// Arguments 是 `$ARGUMENTS` 与 `$ARGUMENTS[N]` / `${N}` 的数据源（0-based）。
	Arguments []string
	// Named 是 frontmatter `arguments` 声明的命名参数（键为声明名）。
	Named map[string]string
	// SkillDir 是技能目录（SKILL.md 所在目录）。
	SkillDir string
	// ProjectDir 是会话工作目录。
	ProjectDir string
	// SessionID 是当前会话标识。
	SessionID string
	// Effort 是当前推理强度（可能为空）。
	Effort string
}

// SubstitutionReport 记录替换结果，供调试与诊断使用。
type SubstitutionReport struct {
	// Applied 是成功替换的 token 数。
	Applied int
	// Unknown 是无法解析的 `${...}` token（原文保留）。
	Unknown []string
	// Empty 是解析成功但值为空的 token。
	Empty []string
}

// Changed 报告本次是否发生过替换。
func (r SubstitutionReport) Changed() bool { return r.Applied > 0 }

// substitutionTokenMaxLen 限制 `${...}` 的花括号内长度，避免把普通文本误判为 token。
const substitutionTokenMaxLen = 64

// SubstituteSkillText 对 skill 文本做单趟占位符替换。
//
// 规则：
//   - `$ARGUMENTS`：全部参数以空格连接；
//   - `$ARGUMENTS[N]` / `${N}`：第 N 个参数（0-based），缺失替换为空串；
//   - `$name` / `${name}`：`arguments` 声明的命名参数，未提供时用默认值，仍为空则替换为空串；
//   - `${SKILL_DIR}` / `${PROJECT_DIR}` / `${SESSION_ID}` / `${EFFORT}` 及其兼容别名；
//   - 其余 `${...}` 原样保留并记入 Unknown；
//   - **单趟替换**：插入内容不再被解析，避免二次注入。
func SubstituteSkillText(text string, ctx SubstitutionContext) (string, SubstitutionReport) {
	report := SubstitutionReport{}
	if text == "" || !ctx.Enabled {
		return text, report
	}

	var b strings.Builder
	b.Grow(len(text) + 16)

	for i := 0; i < len(text); {
		ch := text[i]
		if ch != '$' {
			b.WriteByte(ch)
			i++
			continue
		}

		// `${...}`
		if i+1 < len(text) && text[i+1] == '{' {
			end := strings.IndexByte(text[i+2:], '}')
			if end < 0 || end > substitutionTokenMaxLen {
				b.WriteByte(ch)
				i++
				continue
			}
			token := text[i+2 : i+2+end]
			if value, ok := resolveSkillBracedToken(token, ctx); ok {
				report.Applied++
				if value == "" {
					report.Empty = appendUniqueToken(report.Empty, token)
				}
				b.WriteString(value)
			} else {
				report.Unknown = appendUniqueToken(report.Unknown, token)
				b.WriteString(text[i : i+2+end+1])
			}
			i += 2 + end + 1
			continue
		}

		// `$ARGUMENTS` / `$ARGUMENTS[N]`
		if strings.HasPrefix(text[i+1:], "ARGUMENTS") {
			after := i + 1 + len("ARGUMENTS")
			if after >= len(text) || !isSkillTokenByte(text[after]) {
				if after < len(text) && text[after] == '[' {
					if close := strings.IndexByte(text[after:], ']'); close > 0 && close <= 8 {
						indexText := text[after+1 : after+close]
						if n, err := strconv.Atoi(indexText); err == nil && n >= 0 {
							value := skillArgumentAt(ctx.Arguments, n)
							report.Applied++
							if value == "" {
								report.Empty = appendUniqueToken(report.Empty, "$ARGUMENTS["+indexText+"]")
							}
							b.WriteString(value)
							i = after + close + 1
							continue
						}
					}
				}
				joined := strings.Join(ctx.Arguments, " ")
				report.Applied++
				if joined == "" {
					report.Empty = appendUniqueToken(report.Empty, "$ARGUMENTS")
				}
				b.WriteString(joined)
				i = after
				continue
			}
		}

		// `$name`
		if end := scanSkillTokenName(text, i+1); end > i+1 {
			name := text[i+1 : end]
			if value, ok := ctx.Named[name]; ok {
				report.Applied++
				if value == "" {
					report.Empty = appendUniqueToken(report.Empty, "$"+name)
				}
				b.WriteString(value)
				i = end
				continue
			}
		}

		b.WriteByte(ch)
		i++
	}

	return b.String(), report
}

// SplitSkillArguments 把 `/skill <name> <args>` 的参数文本拆成参数列表。
// 支持单引号、双引号与反斜杠转义；未闭合的引号按字面处理。
func SplitSkillArguments(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var args []string
	var current strings.Builder
	inToken := false
	var quote byte

	for i := 0; i < len(text); i++ {
		ch := text[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(text) {
				i++
				current.WriteByte(text[i])
				continue
			}
			if ch == quote {
				quote = 0
				continue
			}
			current.WriteByte(ch)
			continue
		}
		switch ch {
		case ' ', '\t', '\n', '\r':
			if inToken {
				args = append(args, current.String())
				current.Reset()
				inToken = false
			}
		case '\'', '"':
			quote = ch
			inToken = true
		case '\\':
			if i+1 < len(text) {
				i++
				current.WriteByte(text[i])
				inToken = true
			}
		default:
			current.WriteByte(ch)
			inToken = true
		}
	}
	if inToken {
		args = append(args, current.String())
	}
	return args
}

// NewSubstitutionContext 从 skill 定义与调用参数构造替换上下文。
// 命名参数按 `arguments` 声明顺序与位置参数绑定，位置缺省时回退声明默认值。
func NewSubstitutionContext(item *Skill, args []string, projectDir, sessionID, effort string, enabled bool) SubstitutionContext {
	ctx := SubstitutionContext{
		Enabled:    enabled,
		Arguments:  append([]string(nil), args...),
		ProjectDir: strings.TrimSpace(projectDir),
		SessionID:  strings.TrimSpace(sessionID),
		Effort:     strings.TrimSpace(effort),
	}
	if item == nil {
		return ctx
	}
	if item.Source != nil {
		ctx.SkillDir = strings.TrimSpace(item.Source.Dir)
	}
	if item.Codex == nil || len(item.Codex.Arguments) == 0 {
		return ctx
	}
	named := make(map[string]string, len(item.Codex.Arguments))
	for index, arg := range item.Codex.Arguments {
		name := strings.TrimSpace(arg.Name)
		if name == "" {
			continue
		}
		value := ""
		if index < len(args) {
			value = args[index]
		}
		if value == "" {
			value = arg.Default
		}
		named[name] = value
	}
	if len(named) > 0 {
		ctx.Named = named
	}
	return ctx
}

func resolveSkillBracedToken(token string, ctx SubstitutionContext) (string, bool) {
	raw := strings.TrimSpace(token)
	if raw == "" {
		return "", false
	}
	if isAllDigits(raw) {
		index, err := strconv.Atoi(raw)
		if err != nil || index < 0 {
			return "", false
		}
		return skillArgumentAt(ctx.Arguments, index), true
	}
	switch strings.ToUpper(raw) {
	case "ARGUMENTS":
		return strings.Join(ctx.Arguments, " "), true
	case "SKILL_DIR", "CLAUDE_SKILL_DIR", "COMMANDCODE_SKILL_DIR":
		return ctx.SkillDir, true
	case "PROJECT_DIR", "CLAUDE_PROJECT_DIR", "COMMANDCODE_PROJECT_DIR":
		return ctx.ProjectDir, true
	case "SESSION_ID", "CLAUDE_SESSION_ID", "COMMANDCODE_SESSION_ID":
		return ctx.SessionID, true
	case "EFFORT", "CLAUDE_EFFORT", "COMMANDCODE_EFFORT":
		return ctx.Effort, true
	}
	if ctx.Named != nil {
		if value, ok := ctx.Named[raw]; ok {
			return value, true
		}
		if value, ok := ctx.Named[strings.ToLower(raw)]; ok {
			return value, true
		}
	}
	return "", false
}

func skillArgumentAt(args []string, index int) string {
	if index < 0 || index >= len(args) {
		return ""
	}
	return args[index]
}

func isSkillTokenByte(ch byte) bool {
	return ch == '_' || ch == '-' ||
		(ch >= '0' && ch <= '9') ||
		(ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z')
}

func scanSkillTokenName(text string, start int) int {
	i := start
	for i < len(text) && isSkillTokenByte(text[i]) {
		i++
	}
	return i
}

func isAllDigits(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return len(value) > 0
}

func appendUniqueToken(tokens []string, token string) []string {
	for _, existing := range tokens {
		if existing == token {
			return tokens
		}
	}
	return append(tokens, token)
}
