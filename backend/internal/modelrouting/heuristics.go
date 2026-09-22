package modelrouting

import (
	"strings"
	"unicode"
)

// 关键词启发式（G4）：本仓库的实际工作语言是中文，而历史词表只有英文，导致
// 「安全 / 迁移 / 权限」这类高风险表述一条都不命中。这里把词表扩为两档：
//
//   - 高信号（builtinPromoteKeywords）：单命中即升 hard；
//   - 弱信号（builtinPromoteComboKeywords）：需 ≥2 命中，或 1 命中 + 写任务，
//     用于压制「保持风格一致性」这类误报。
//
// 词表可用 aicli.subagents.routing.heuristics.promote_keywords /
// promote_keywords_combo 追加（追加而非替换），heuristics.disabled 可整体关闭。
var builtinPromoteKeywords = []string{
	// 英文（沿用历史词表，保持向后兼容）
	"security", "permission", "migration", "architecture", "provider", "protocol",
	// 中文
	"安全", "权限", "鉴权", "认证", "迁移", "架构", "协议", "加密", "密钥",
	"跨系统", "并发安全", "跨系统一致性",
}

var builtinPromoteComboKeywords = []string{
	"consistency", "compatibility", "refactor", "rollback", "release", "boundary",
	"一致性", "兼容", "重构", "回滚", "灰度", "发布", "边界", "契约",
}

// weakSignalMinHits 是弱信号单独升档所需的命中数（方案 §5.4）。
const weakSignalMinHits = 2

// promotionHits 记录一次提升命中的证据，用于把命中词写进 route_warnings，
// 使误报可观测、可回溯、可按词调优（方案 §5.4 的误报控制）。
type promotionHits struct {
	// Role 为 true 表示提升来自角色规则（verifier / 非只读 writer），而不是关键词。
	Role bool
	// Strong 是高信号命中词（按词表顺序，最多记录 3 个）。
	Strong []string
	// Combo 是弱信号命中词（按词表顺序，最多记录 3 个）。
	Combo []string
}

func (h promotionHits) empty() bool {
	return !h.Role && len(h.Strong) == 0 && len(h.Combo) == 0
}

// keywords 返回要写入告警的命中词（高信号在前），并有界收敛到 3 个，
// 避免审计载荷被词表命中的长尾撑大。
func (h promotionHits) keywords() []string {
	matched := append(append([]string(nil), h.Strong...), h.Combo...)
	if len(matched) > 3 {
		matched = matched[:3]
	}
	return matched
}

// normalizeKeywordText 归一待匹配文本：全角→半角、大小写折叠、空白折叠。
// 匹配不使用正则，避免配置注入风险（方案 §5.4）；英文词形归一见 keywordStem。
func normalizeKeywordText(raw string) string {
	if raw == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(raw))
	lastSpace := false
	for _, r := range raw {
		if r == '\u3000' { // 全角空格
			r = ' '
		}
		if r >= '\uFF01' && r <= '\uFF5E' { // 全角 ASCII 区
			r = r - 0xFEE0
		}
		if unicode.IsSpace(r) {
			if lastSpace {
				continue
			}
			lastSpace = true
			builder.WriteRune(' ')
			continue
		}
		lastSpace = false
		builder.WriteRune(unicode.ToLower(r))
	}
	return strings.TrimSpace(builder.String())
}

// 词形归一（英文）的两条护栏，共同决定"多激进"。宁漏勿错：误报会直接推高
// 成本，漏报只是少一次升档。
const (
	// minStemmableWordLen 是参与词形归一的最短词长，挡住 act / use / run 这类
	// 短词（否则 action→act、using→us 会与无关词碰撞）。
	minStemmableWordLen = 5
	// minStemLen 是归一后允许的最短词干长度，低于它说明剥得太狠（question→quest、
	// version→vers），此时放弃该规则、保留原形。
	minStemLen = 6
)

// keywordStem 对英文词做保守的词形归一，把 migration / migrate / migrating /
// migrated / migrations 收敛到同一词干（migrat），使词表命中不再依赖词尾形态。
//
// 设计约束（避免退化成任意模糊匹配、放大误报）：
//   - 只处理纯 ASCII 字母词；中文与含空格/连字符的多词条目仍走子串匹配；
//   - 只按固定后缀表做一次剥离，不递归、不做编辑距离、不比较相似度；
//   - 剥离方向单一（只去后缀），不会把两个不同词源的词归到一起；
//   - 词长与词干长度双重护栏（见上方常量），短词直接不参与。
//
// 输入需为 normalizeKeywordText 之后的小写文本；返回 "" 表示该词不参与词形归一。
func keywordStem(word string) string {
	if len(word) < minStemmableWordLen || !isASCIILetters(word) {
		return ""
	}
	if !isLowerASCIILetters(word) {
		word = strings.ToLower(word)
	}
	switch {
	case strings.HasSuffix(word, "ies") && len(word)-3 >= minStemLen:
		return word[:len(word)-3] + "i" // boundaries → boundari
	case strings.HasSuffix(word, "y") && isConsonantLetter(word[len(word)-2]) && len(word)-1 >= minStemLen:
		return word[:len(word)-1] + "i" // security → securiti
	case strings.HasSuffix(word, "ions") && len(word)-4 >= minStemLen:
		return word[:len(word)-4] // permissions → permiss
	case strings.HasSuffix(word, "ion") && len(word)-3 >= minStemLen:
		return word[:len(word)-3] // migration → migrat
	case strings.HasSuffix(word, "ing") && len(word)-3 >= minStemLen:
		return undoubleFinalConsonant(word[:len(word)-3]) // migrating → migrat
	case strings.HasSuffix(word, "ed") && len(word)-2 >= minStemLen:
		return undoubleFinalConsonant(word[:len(word)-2]) // migrated → migrat
	case strings.HasSuffix(word, "es") && len(word)-2 >= minStemLen:
		return word[:len(word)-2] // releases → releas
	case strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") &&
		!strings.HasSuffix(word, "us") && !strings.HasSuffix(word, "is") &&
		len(word)-1 >= minStemLen:
		return word[:len(word)-1] // providers → provider
	case strings.HasSuffix(word, "e") && len(word)-1 >= minStemLen:
		return word[:len(word)-1] // migrate → migrat
	}
	return ""
}

// stemOrSelf 返回词形归一结果；不适用时返回原词，供集合查找使用。
func stemOrSelf(word string) string {
	if stem := keywordStem(word); stem != "" {
		return stem
	}
	return word
}

// undoubleFinalConsonant 处理 running → runn → run 这类双写辅音（-ing/-ed 前
// 的双写）。只做一次，且只在确实是双写辅音时生效。
func undoubleFinalConsonant(word string) string {
	if len(word) < 3 {
		return word
	}
	last, prev := word[len(word)-1], word[len(word)-2]
	if last == prev && isConsonantLetter(last) {
		return word[:len(word)-1]
	}
	return word
}

// goalWordStems 把归一文本切成 ASCII 词并做同样的词形归一，得到词干集合。
// 中文、数字、标点都是分隔符，因此「迁移migration脚本」也能切出 migration；
// 非英文词条不会出现在集合里，不会误命中。
func goalWordStems(text string) map[string]struct{} {
	stems := make(map[string]struct{}, 16)
	start := -1
	for i := 0; i <= len(text); i++ {
		if i < len(text) && isASCIILetter(text[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			stems[stemOrSelf(text[start:i])] = struct{}{}
			start = -1
		}
	}
	return stems
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isASCIILetters(word string) bool {
	if word == "" {
		return false
	}
	for i := 0; i < len(word); i++ {
		if !isASCIILetter(word[i]) {
			return false
		}
	}
	return true
}

func isLowerASCIILetters(word string) bool {
	for i := 0; i < len(word); i++ {
		if word[i] < 'a' || word[i] > 'z' {
			return false
		}
	}
	return true
}

func isConsonantLetter(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return false
	}
	return isASCIILetter(b)
}

// matchedKeywords 返回词表中命中归一文本的条目（保持词表顺序、去重）。
//
// 两级匹配，第二级是第一级的纯增量：
//  1. 子串匹配（历史语义；中文与含空格/连字符的多词条目只走这一级）；
//  2. 英文词形归一匹配：词尾形态不同时按词干再比一次（migration ↔ migrate /
//     migrating / migrated），详见 keywordStem。命中记的仍是词表条目本身，
//     因此 route_warnings 的格式与调优方式不变。
func matchedKeywords(text string, keywords []string) []string {
	if text == "" || len(keywords) == 0 {
		return nil
	}
	matched := make([]string, 0, len(keywords))
	seen := make(map[string]struct{}, len(keywords))
	var wordStems map[string]struct{} // 惰性构建：子串命中时无需分词
	for _, keyword := range keywords {
		needle := normalizeKeywordText(keyword)
		if needle == "" {
			continue
		}
		if _, dup := seen[needle]; dup {
			continue
		}
		if !strings.Contains(text, needle) {
			if wordStems == nil {
				wordStems = goalWordStems(text)
			}
			if _, hit := wordStems[stemOrSelf(needle)]; !hit {
				continue
			}
		}
		seen[needle] = struct{}{}
		matched = append(matched, keyword)
	}
	return matched
}

// keywordPromotionWarnings 把命中词展开成 route_warnings 条目。每条都带词，
// 因此"为什么被升档"在审计里自解释。
func keywordPromotionWarnings(hits promotionHits) []string {
	keywords := hits.keywords()
	if len(keywords) == 0 {
		return nil
	}
	warnings := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		warnings = append(warnings, "difficulty_promoted_by_keyword:"+keyword)
	}
	return warnings
}

// promotionWarnings 汇总一次提升的全部证据：角色规则 + 关键词命中。
func promotionWarnings(hits promotionHits, role string) []string {
	warnings := []string{}
	if hits.Role {
		warnings = append(warnings, "difficulty_promoted_by_role:"+NormalizeRole(role))
	}
	return append(warnings, keywordPromotionWarnings(hits)...)
}
