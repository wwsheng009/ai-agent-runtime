package knowledge

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// 索引范围的 .gitignore 过滤（04 §4.7 G11 / R11 的落地）。
//
// 候选文件收集不能只看内置 ignoreDirs：每个项目自己的 .gitignore 才是
// "什么不是源码"的权威声明（构建产物、缓存、生成文件、日志、本地配置）。
// 不读它，RunIndex 会把 dist/、artifacts/、*.gen.go 之类路径写进知识库，
// 污染检索面，并让 maxIndexFiles 预算被非源码耗尽。
//
// 语义范围（对齐 git dir.c/wildmatch 的常用子集）：
//   - 按目录层级叠加：根 → 深的 .gitignore 顺序求值，最后命中的规则胜出
//     （深层文件覆盖上层文件，同一文件内后面的行覆盖前面的行）；
//   - ! 取反、# 注释、空行、行尾未转义空格、\ 转义（\#、\!、\ ）；
//   - 无 '/' 的模式按 basename 匹配（任意深度）；含 '/' 的模式锚定到
//     .gitignore 所在目录；尾部 '/' 仅匹配目录；'*' / '?' / '[]' 不跨 '/'；
//     '**/'、'/**/'、'/**' 按 git 的"零到多级目录"语义展开；
//   - 目录被忽略即整棵子树被忽略（git 同样不允许对被忽略目录内的文件用
//     ! 重新包含）。
//
// 范围边界：只读取工作区内的 .gitignore；`.git/info/exclude` 与用户全局
// excludesFile 不参与（前者需要先定位仓库根，后者是机器级配置，都超出
// "按项目声明过滤"的语义）。非 git 工作区同样生效——.gitignore 常随源码
// 分发，它表达的是用户对"什么该被索引"的显式声明。
//
// 失败契约（Degrade-Not-Fail）：单个 .gitignore 读失败 / 规则编译失败不
// 阻断索引，只是该规则（文件）不生效。

// gitignoreFileName 是规则来源文件名。
const gitignoreFileName = ".gitignore"

// gitignoreRule 是一条已编译的规则。
type gitignoreRule struct {
	negated bool
	dirOnly bool
	// baseName 表示模式不含 '/'：按 basename 匹配（任意深度，git 语义）。
	baseName bool
	regex    *regexp.Regexp
}

// gitignoreFile 是一个目录下的 .gitignore；base 是该目录的 workspace 相对
// 路径（'/' 分隔；工作区根为 ""）。规则只作用于 base 之下的路径。
type gitignoreFile struct {
	base  string
	rules []gitignoreRule
}

// gitignoreSet 是按 root→深 排序的规则文件集合，对单个路径做"最后命中胜出"判定。
type gitignoreSet struct {
	files []gitignoreFile
}

// loadGitignoreFile 读取 absDir/.gitignore；不存在 / 不可读 / 无有效规则时
// 返回 nil（调用方按"该目录没有规则"处理，绝不因读盘失败中断索引）。
func loadGitignoreFile(absDir, relDir string) *gitignoreFile {
	data, err := os.ReadFile(filepath.Join(absDir, gitignoreFileName))
	if err != nil {
		return nil
	}
	rules := parseGitignoreRules(data)
	if len(rules) == 0 {
		return nil
	}
	return &gitignoreFile{base: normalizeRelPath(relDir), rules: rules}
}

// push 追加一个规则文件（walk 进入目录时调用，顺序即 root→深）。
func (s *gitignoreSet) push(f gitignoreFile) { s.files = append(s.files, f) }

// retainAncestors 弹出不再覆盖 rel 的规则文件。
//
// WalkDir 是深度优先：栈顶是最深目录的规则；进入下一个兄弟路径前，把
// 不是 rel 祖先的规则全部弹出，保证判定只看"该路径祖先目录链上的规则"。
func (s *gitignoreSet) retainAncestors(rel string) {
	for len(s.files) > 0 {
		base := s.files[len(s.files)-1].base
		if base == "" || rel == base || strings.HasPrefix(rel, base+"/") {
			return
		}
		s.files = s.files[:len(s.files)-1]
	}
}

// isIgnored 按 git 语义判定 rel（workspace 相对、'/' 分隔）是否被忽略：
// 所有规则文件按 root→深 顺序求值，最后命中的规则（含取反）胜出。
func (s *gitignoreSet) isIgnored(rel string, isDir bool) bool {
	ignored := false
	for _, f := range s.files {
		sub, ok := pathUnderBase(f.base, rel)
		if !ok {
			continue
		}
		for _, rule := range f.rules {
			if rule.matches(sub, isDir) {
				ignored = !rule.negated
			}
		}
	}
	return ignored
}

// pathUnderBase 返回 rel 相对 base 的子路径；不在 base 之下时 ok=false。
func pathUnderBase(base, rel string) (string, bool) {
	if base == "" {
		return rel, true
	}
	if rel == base {
		return "", true
	}
	if strings.HasPrefix(rel, base+"/") {
		return rel[len(base)+1:], true
	}
	return "", false
}

// matches 报告规则是否命中 sub（相对规则文件所在目录的路径）。
func (r gitignoreRule) matches(sub string, isDir bool) bool {
	if r.regex == nil || (r.dirOnly && !isDir) {
		return false
	}
	if r.baseName {
		return r.regex.MatchString(path.Base(sub))
	}
	return r.regex.MatchString(sub)
}

// parseGitignoreRules 把 .gitignore 内容解析为规则列表。
//
// 单行长度上限 1 MiB：畸形超长行按无效规则跳过，不阻断索引。
func parseGitignoreRules(data []byte) []gitignoreRule {
	var rules []gitignoreRule
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		if rule, ok := parseGitignoreRule(scanner.Text()); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

// parseGitignoreRule 解析单行规则；注释 / 空行 / 无效行返回 ok=false。
func parseGitignoreRule(line string) (gitignoreRule, bool) {
	line = strings.TrimSuffix(line, "\r") // Windows CRLF
	line = trimUnescapedTrailingSpaces(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return gitignoreRule{}, false
	}

	var rule gitignoreRule
	switch {
	case strings.HasPrefix(line, "!"):
		rule.negated = true
		line = line[1:]
	case strings.HasPrefix(line, `\!`):
		// \! 是字面量 '!'，不是取反。
		line = line[1:]
	}
	if line == "" {
		return gitignoreRule{}, false
	}

	if strings.HasSuffix(line, "/") {
		rule.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	anchored := strings.HasPrefix(line, "/")
	if anchored {
		line = strings.TrimPrefix(line, "/")
	}
	if line == "" {
		return gitignoreRule{}, false
	}

	// 无 '/'（且未用前导 '/' 锚定）的模式对 basename 生效，任意深度；
	// 其余模式锚定到规则文件所在目录。
	rule.baseName = !anchored && !strings.Contains(line, "/")
	re, err := compileGitignorePattern(line)
	if err != nil {
		return gitignoreRule{}, false
	}
	rule.regex = re
	return rule, true
}

// trimUnescapedTrailingSpaces 去掉行尾未转义的空格（git 语义）。
// 被 `\ ` 转义的空格保留，由编译阶段还原为字面空格。
func trimUnescapedTrailingSpaces(line string) string {
	for len(line) > 0 && line[len(line)-1] == ' ' {
		backslashes := 0
		for i := len(line) - 2; i >= 0 && line[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			break
		}
		line = line[:len(line)-1]
	}
	return line
}

// compileGitignorePattern 把 gitignore 的 glob 编译为锚定正则（大小写敏感，
// 与 git 一致）。支持 '*' / '?' / '[]' 与 '**' 的三种整段形态；未闭合的 '['
// 按字面量处理（wildmatch 也不把它当错误）。
func compileGitignorePattern(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		switch c := pattern[i]; c {
		case '*':
			j := i
			for j < len(pattern) && pattern[j] == '*' {
				j++
			}
			prevSlash := i == 0 || pattern[i-1] == '/'
			nextSlash := j == len(pattern) || pattern[j] == '/'
			if j-i >= 2 && prevSlash && nextSlash {
				if j == len(pattern) {
					// 尾部 '/**'（或裸 '**'）：匹配其下一切（零到多段）。
					b.WriteString(".*")
					i = j
					continue
				}
				// 前导 '**/' 或中段 '/**/'：零到多级目录。
				b.WriteString("(?:.*/)?")
				i = j + 1 // 跳过 '**' 之后的 '/'
				continue
			}
			// 非整段的连续 '*' 等价于单个 '*'（git wildmatch 语义）。
			b.WriteString("[^/]*")
			i = j
		case '?':
			b.WriteString("[^/]")
			i++
		case '[':
			i = appendGitignoreCharClass(&b, pattern, i)
		case '\\':
			if i+1 < len(pattern) {
				b.WriteString(regexp.QuoteMeta(string(pattern[i+1])))
				i += 2
			} else {
				b.WriteString(regexp.QuoteMeta(`\`))
				i++
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// appendGitignoreCharClass 处理 '[' 字符类，返回下一个待处理下标。
// 未闭合 / 空的字符类按字面量 '[' 处理；'[!...]' 按 fnmatch 语义翻成 '[^...]'。
func appendGitignoreCharClass(b *strings.Builder, pattern string, start int) int {
	j := start + 1
	if j < len(pattern) && (pattern[j] == '!' || pattern[j] == '^') {
		j++
	}
	if j < len(pattern) && pattern[j] == ']' {
		j++ // 首字符 ']' 是字面量
	}
	for j < len(pattern) && pattern[j] != ']' {
		if pattern[j] == '\\' && j+1 < len(pattern) {
			j += 2
			continue
		}
		j++
	}
	if j >= len(pattern) {
		b.WriteString(`\[`)
		return start + 1
	}
	raw := pattern[start+1 : j]
	negated := false
	if strings.HasPrefix(raw, "!") || strings.HasPrefix(raw, "^") {
		negated = true
		raw = raw[1:]
	}
	if raw == "" {
		b.WriteString(`\[`)
		return start + 1
	}

	// 逐字符翻译：`\x` 是字面量 x（与 fnmatch 一致），其余按 RE2 转义。
	var cls strings.Builder
	if negated {
		cls.WriteString("^")
	}
	for k := 0; k < len(raw); {
		if raw[k] == '\\' && k+1 < len(raw) {
			cls.WriteString(regexp.QuoteMeta(string(raw[k+1])))
			k += 2
			continue
		}
		cls.WriteString(regexp.QuoteMeta(string(raw[k])))
		k++
	}
	b.WriteString("[")
	b.WriteString(cls.String())
	b.WriteString("]")
	return j + 1
}

// isIgnoredByGitignore 判定单个 workspace 相对路径是否被 .gitignore 忽略。
//
// 与 walk 口径一致：先逐级检查祖先目录（任一祖先被忽略 → 整体被忽略），
// 再对路径本身做"最后命中胜出"判定。只读取路径链上的 .gitignore，复杂度
// O(路径深度) 次读盘，适合定向增量的少量显式路径。
func isIgnoredByGitignore(root, rel string, isDir bool) bool {
	rel = normalizeRelPath(rel)
	if rel == "" || rel == "." {
		return false
	}
	parts := strings.Split(rel, "/")

	var set gitignoreSet
	if f := loadGitignoreFile(root, ""); f != nil {
		set.push(*f)
	}
	dirAbs := root
	dirRel := ""
	for i := 0; i < len(parts)-1; i++ {
		dirRel = path.Join(dirRel, parts[i])
		dirAbs = filepath.Join(dirAbs, filepath.FromSlash(parts[i]))
		if set.isIgnored(dirRel, true) {
			return true
		}
		if f := loadGitignoreFile(dirAbs, dirRel); f != nil {
			set.push(*f)
		}
	}
	return set.isIgnored(rel, isDir)
}
