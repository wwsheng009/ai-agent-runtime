package importers

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// parseCodexTOML 解析 Codex 的 ~/.codex/config.toml 里的 [mcp_servers.*] 表。
//
// 仓库没有 TOML 依赖，这里实现「够用即止」的子集解析器：
//   - 表头 [mcp_servers.<name>] / [mcp_servers."<name>"]（含点号/中文名）；
//   - 键值对：字符串（基本串与字面串）、整数/浮点、布尔、字符串数组（可跨行）；
//   - 内联表 env = { KEY = "value" }（Codex 常见写法）。
//
// 明确不支持的构造（嵌套子表、数组表、多行字符串）会被记入 Warnings 并跳过，
// 而不是让整份文件导入失败——用户能看到缺了什么，也能手工补进 aicli 配置。
func parseCodexTOML(data []byte, source string) ([]Server, []string, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	warnings := make([]string, 0)
	servers := make([]Server, 0)

	currentName := ""
	// current 是当前表头的键值集合；遇到下一个表头时结算。
	current := map[string]interface{}{}
	flush := func() {
		if currentName == "" {
			return
		}
		entry := current
		server, serverWarnings := mapServerEntry(currentName, entry, source)
		servers = append(servers, server)
		warnings = append(warnings, serverWarnings...)
		current = map[string]interface{}{}
	}

	for index := 0; index < len(lines); index++ {
		raw := strings.TrimSpace(stripTOMLComment(lines[index]))
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "[[") {
			warnings = append(warnings, fmt.Sprintf("%s:%d: 不支持数组表（已跳过）", source, index+1))
			continue
		}
		if strings.HasPrefix(raw, "[") {
			name, err := parseTOMLTableHeader(raw)
			flush()
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s:%d: %v", source, index+1, err))
				currentName = ""
				continue
			}
			// name 为空表示「非 mcp_servers 表」：结算当前 server 后忽略后续内容。
			currentName = name
			continue
		}
		if currentName == "" {
			continue
		}

		key, valueText, found := strings.Cut(raw, "=")
		if !found {
			warnings = append(warnings, fmt.Sprintf("%s:%d: 无法解析的行（已忽略）", source, index+1))
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), `"'`)
		valueText = strings.TrimSpace(valueText)

		// 数组可能跨行：一直拼到括号闭合。
		for strings.HasPrefix(valueText, "[") && !bracketsBalanced(valueText) && index+1 < len(lines) {
			index++
			valueText += "\n" + strings.TrimSpace(stripTOMLComment(lines[index]))
		}

		value, err := parseTOMLValue(valueText)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s:%d: %s.%s 取值无法解析（%v，已忽略）", source, index+1, currentName, key, err))
			continue
		}
		current[key] = value
	}
	flush()

	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers, warnings, nil
}

// parseTOMLTableHeader 返回 mcp_servers 下的表名；非 mcp_servers 表返回空。
func parseTOMLTableHeader(raw string) (string, error) {
	body := strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	parts := splitTOMLKeyPath(body)
	if len(parts) == 0 {
		return "", fmt.Errorf("空表头")
	}
	if parts[0] != "mcp_servers" {
		return "", nil
	}
	if len(parts) == 1 {
		return "", nil
	}
	if len(parts) > 2 {
		return "", fmt.Errorf("不支持嵌套子表 %q（已跳过）", raw)
	}
	return parts[1], nil
}

// splitTOMLKeyPath 切分 a.b."c.d" 形态的键路径，保留引号内的点号。
func splitTOMLKeyPath(body string) []string {
	parts := make([]string, 0, 2)
	var builder strings.Builder
	inQuote := rune(0)
	for _, r := range body {
		switch {
		case inQuote != 0:
			if r == inQuote {
				inQuote = 0
				continue
			}
			builder.WriteRune(r)
		case r == '"' || r == '\'':
			inQuote = r
		case r == '.':
			parts = append(parts, strings.TrimSpace(builder.String()))
			builder.Reset()
		default:
			builder.WriteRune(r)
		}
	}
	parts = append(parts, strings.TrimSpace(builder.String()))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// stripTOMLComment 去掉行尾注释（字符串内的 # 不处理）。
func stripTOMLComment(line string) string {
	inQuote := rune(0)
	for index, r := range line {
		switch {
		case inQuote != 0:
			if r == inQuote {
				inQuote = 0
			}
		case r == '"' || r == '\'':
			inQuote = r
		case r == '#':
			return line[:index]
		}
	}
	return line
}

func bracketsBalanced(text string) bool {
	depth := 0
	inQuote := rune(0)
	for _, r := range text {
		switch {
		case inQuote != 0:
			if r == inQuote {
				inQuote = 0
			}
		case r == '"' || r == '\'':
			inQuote = r
		case r == '[':
			depth++
		case r == ']':
			depth--
		}
	}
	return depth <= 0
}

// parseTOMLValue 解析受支持的 TOML 取值（字符串/数字/布尔/数组/内联表）。
func parseTOMLValue(text string) (interface{}, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("空值")
	}
	switch text[0] {
	case '"', '\'':
		return parseTOMLString(text)
	case '[':
		return parseTOMLArray(text)
	case '{':
		return parseTOMLInlineTable(text)
	}
	switch strings.ToLower(text) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	if value, err := strconv.ParseFloat(strings.ReplaceAll(text, "_", ""), 64); err == nil {
		return value, nil
	}
	// TOML 里裸值非法，但宽松处理成字符串更符合「尽力导入」的目标。
	return text, nil
}

func parseTOMLString(text string) (string, error) {
	quote := text[0]
	if len(text) < 2 || text[len(text)-1] != quote {
		return "", fmt.Errorf("字符串未闭合")
	}
	body := text[1 : len(text)-1]
	if quote == '"' {
		if unescaped, err := strconv.Unquote(`"` + strings.ReplaceAll(body, `"`, `\"`) + `"`); err == nil {
			return unescaped, nil
		}
	}
	return body, nil
}

func parseTOMLArray(text string) ([]interface{}, error) {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "["), "]"))
	if inner == "" {
		return nil, nil
	}
	items := splitTOMLTopLevel(inner, ',')
	out := make([]interface{}, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		value, err := parseTOMLValue(item)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func parseTOMLInlineTable(text string) (map[string]interface{}, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return nil, fmt.Errorf("内联表未闭合")
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	out := map[string]interface{}{}
	if inner == "" {
		return out, nil
	}
	for _, item := range splitTOMLTopLevel(inner, ',') {
		key, valueText, found := strings.Cut(item, "=")
		if !found {
			return nil, fmt.Errorf("内联表条目缺少 =")
		}
		value, err := parseTOMLValue(valueText)
		if err != nil {
			return nil, err
		}
		out[strings.Trim(strings.TrimSpace(key), `"'`)] = value
	}
	return out, nil
}

// splitTOMLTopLevel 按分隔符切分，忽略引号与嵌套括号内部的分隔符。
func splitTOMLTopLevel(text string, separator rune) []string {
	parts := make([]string, 0, 4)
	var builder strings.Builder
	inQuote := rune(0)
	depth := 0
	for _, r := range text {
		switch {
		case inQuote != 0:
			builder.WriteRune(r)
			if r == inQuote {
				inQuote = 0
			}
		case r == '"' || r == '\'':
			inQuote = r
			builder.WriteRune(r)
		case r == '[' || r == '{':
			depth++
			builder.WriteRune(r)
		case r == ']' || r == '}':
			depth--
			builder.WriteRune(r)
		case r == separator && depth == 0:
			parts = append(parts, builder.String())
			builder.Reset()
		default:
			builder.WriteRune(r)
		}
	}
	parts = append(parts, builder.String())
	return parts
}
