package knowledge

import (
	"strings"
	"unicode"
)

// shadowPatternTokenLimit 限制一次 grep 调用展开的检索 token 数，防止长正则
// 交替把候选查询放大成几十次索引检索。
const shadowPatternTokenLimit = 6

// grepPatternList 汇总一次 grep 调用的检索文本：
//   - `pattern`（单数，最常用）；
//   - `patterns`（toolkit 批量形态：[]string / []any，或 arg_preview 渲染的
//     "a | b" 字符串）。
//
// 空串/坏元素被丢弃；返回顺序保留调用方给的意义顺序。
func grepPatternList(args map[string]any) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	add(argString(args, "pattern"))
	switch v := args["patterns"].(type) {
	case string:
		for _, part := range strings.Split(v, " | ") {
			add(part)
		}
	case []string:
		for _, item := range v {
			add(item)
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	return out
}

// collectShadowTokens 把多个 pattern 展开成去重、限量的检索 token
// （每个 pattern 先过 shadowPatternTokens，再全局去重并截断）。
func collectShadowTokens(patterns []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, pattern := range patterns {
		for _, token := range shadowPatternTokens(pattern) {
			if seen[token] {
				continue
			}
			seen[token] = true
			out = append(out, token)
			if len(out) >= shadowPatternTokenLimit {
				return out
			}
		}
	}
	return out
}

// shadowPatternTokens 从 grep pattern 提取索引可检索的字面 token
// （ADR-0003 §4.4 的候选查询映射）。
//
// 为什么需要它：真实会话里的 grep pattern 大量是 regex（`a|b`、`func \(h \*T\)`），
// 直接交给 symbols_fts 只会命中零条——Phase1-shadow 重放实测暴露的映射缺陷。
// 映射规则：
//   - 纯字面量（无 regex 元字符）整体保留，保留词形与大小写；
//   - 含元字符时按未转义的 `|` 拆分交替分支，每支只保留 ≥3 字符的标识符片段
//     （字母 / 数字 / 下划线），去重并截断到 shadowPatternTokenLimit 个。
//
// 返回空表示 pattern 里没有可用 token（例如纯 `.*`），调用方退回原始 pattern。
func shadowPatternTokens(pattern string) []string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	if !strings.ContainsAny(pattern, `\|()[]{}*+?^$`) {
		return []string{pattern}
	}

	const escapedPipe = "\x00"
	normalized := strings.ReplaceAll(pattern, `\|`, escapedPipe)
	seen := make(map[string]bool)
	var out []string
	for _, branch := range strings.Split(normalized, "|") {
		branch = strings.ReplaceAll(branch, escapedPipe, "|")
		for _, token := range identifierTokens(branch) {
			if len(token) < 3 || seen[token] {
				continue
			}
			seen[token] = true
			out = append(out, token)
			if len(out) >= shadowPatternTokenLimit {
				return out
			}
		}
	}
	return out
}

// identifierTokens 把一段 regex 文本切成标识符样式片段（其余字符作分隔）。
func identifierTokens(s string) []string {
	var (
		out []string
		b   strings.Builder
	)
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}
