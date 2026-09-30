package knowledge

import (
	pathpkg "path"
	"strings"
)

// scopeFilter 判定候选路径是否落在被拦截调用的作用域内（ADR-0003 §4.4）。
//
// 两种形态：
//   - 普通路径（含绝对路径）：折叠为 workspace 相对路径后做前缀匹配。索引的
//     files.path 一律是相对路径——不折叠绝对路径，带绝对作用域的调用候选
//     会恒为空（Phase1-shadow 实测发现的缺陷）；
//   - glob（含 * ? [）：无 '/' 时匹配 basename（rg -g '*.go' 语义），有 '/'
//     时按路径段匹配整条相对路径（支持 **）。
type scopeFilter struct {
	// prefixes 是作用域前缀集合：多路径（`path` + `paths`）时是 OR 关系（rg 语义：
	// 任一作用域内即命中）。单路径调用只含一项，行为与历史单 prefix 一致。
	prefixes []string
	glob     string
}

// newScopeFilter 把调用参数里的 path / glob / include 归一化为作用域过滤器。
func newScopeFilter(scope, workspace string) scopeFilter {
	scope = relativizeWorkspacePath(scope, workspace)
	if scope == "" {
		return scopeFilter{}
	}
	if strings.ContainsAny(scope, "*?[") {
		return scopeFilter{glob: scope}
	}
	return scopeFilter{prefixes: []string{strings.TrimSuffix(scope, "/")}}
}

// newScopeFilterSpec 归一化**同时给出** path 与 glob/include 的作用域：
// 两个约束是 AND 关系（rg 语义：path 限定搜索根，-g 限定文件名）。
// 只给其一的调用与 newScopeFilter 等价。
func newScopeFilterSpec(pathScope, globScope, workspace string) scopeFilter {
	return newScopeFilterSpecMulti([]string{pathScope}, globScope, workspace)
}

// newScopeFilterSpecMulti 归一化**多路径**（`path` + `paths` 逐项，2026-09-29）与
// glob/include：多个 path 前缀是 OR，glob 与它们仍是 AND（rg 语义：多根搜索 +
// 文件名过滤）；glob 形态的 path 项并入 glob 约束（首个生效，保持历史语义）。
func newScopeFilterSpecMulti(pathScopes []string, globScope, workspace string) scopeFilter {
	filter := scopeFilter{}
	for _, scope := range pathScopes {
		scope = relativizeWorkspacePath(scope, workspace)
		if scope == "" {
			continue
		}
		if strings.ContainsAny(scope, "*?[") {
			if filter.glob == "" {
				filter.glob = scope
			}
			continue
		}
		filter.prefixes = append(filter.prefixes, strings.TrimSuffix(scope, "/"))
	}
	if glob := relativizeWorkspacePath(globScope, workspace); strings.TrimSpace(glob) != "" {
		filter.glob = strings.TrimSpace(glob)
	}
	return filter
}

// empty 报告过滤器是否放行一切（调用未提供作用域）。
func (f scopeFilter) empty() bool { return len(f.prefixes) == 0 && f.glob == "" }

// matches 报告候选路径是否在作用域内。
func (f scopeFilter) matches(path string) bool {
	path = normalizeShadowPath(path)
	if path == "" {
		return false
	}
	if len(f.prefixes) > 0 {
		matched := false
		for _, prefix := range f.prefixes {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if f.glob != "" && !matchPathGlob(f.glob, path) {
		return false
	}
	return true
}

// relativizeWorkspacePath 把 workspace 内的绝对路径折叠成相对路径（大小写
// 不敏感，Windows/POSIX 通吃）；workspace 外或相对路径原样返回。
func relativizeWorkspacePath(p, workspace string) string {
	p = normalizeShadowPath(p)
	if p == "" {
		return ""
	}
	ws := strings.TrimSuffix(normalizeShadowPath(workspace), "/")
	if ws == "" {
		return p
	}
	lowerPath, lowerWS := strings.ToLower(p), strings.ToLower(ws)
	if lowerPath == lowerWS {
		return ""
	}
	if strings.HasPrefix(lowerPath, lowerWS+"/") {
		return p[len(ws)+1:]
	}
	return p
}

// pathFromKey 从 `path:line` 键还原 path。
//
// path 自身可能含冒号（Windows 盘符），因此取最后一个冒号。
func pathFromKey(key string) string {
	if idx := strings.LastIndex(key, ":"); idx > 0 {
		return key[:idx]
	}
	return key
}

// matchPathGlob 用 rg -g 的直觉匹配相对路径：无 '/' 的 pattern 匹配 basename，
// 有 '/' 的 pattern 按段匹配（** 跨段）。
func matchPathGlob(pattern, candidate string) bool {
	pattern = strings.ToLower(normalizeShadowPath(pattern))
	candidate = strings.ToLower(normalizeShadowPath(candidate))
	if pattern == "" {
		return true
	}
	if !strings.Contains(pattern, "/") {
		ok, err := pathpkg.Match(pattern, pathpkg.Base(candidate))
		return err == nil && ok
	}
	return matchGlobSegments(strings.Split(pattern, "/"), strings.Split(candidate, "/"))
}

// matchGlobSegments 逐段匹配，支持 **（匹配零到多段）。
func matchGlobSegments(pattern, parts []string) bool {
	if len(pattern) == 0 {
		return len(parts) == 0
	}
	if pattern[0] == "**" {
		if matchGlobSegments(pattern[1:], parts) {
			return true
		}
		if len(parts) > 0 {
			return matchGlobSegments(pattern, parts[1:])
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, err := pathpkg.Match(pattern[0], parts[0])
	if err != nil || !ok {
		return false
	}
	return matchGlobSegments(pattern[1:], parts[1:])
}
