package commands

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	// chatMentionCandidateLimit 是单次补全返回的候选上限（超过只提示数量）。
	chatMentionCandidateLimit = 40
	// chatMentionScanLimit 是目录扫描的条目上限：超大仓库里保证一次 Tab 的成本可控。
	chatMentionScanLimit = 6000
)

// chatMentionSkipDirs 是补全扫描跳过的目录（版本库、依赖与构建产物）。
var chatMentionSkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true, "out": true,
	"target": true, "__pycache__": true, ".venv": true, "venv": true, ".idea": true,
	".next": true, ".cache": true, "coverage": true,
}

// chatMentionToken 描述光标处的一个 @ 路径引用。
type chatMentionToken struct {
	Query string // @ 之后到光标之间的文本（不含 @）
	Start int    // @ 在文本中的 rune 下标
	Valid bool
}

// chatMentionTokenAt 解析光标处的 @ 引用。
//
// 规则（保守，避免误伤普通文本）：
//   - 从光标向左找最近的 @，中间不得出现空白或引号/括号；
//   - @ 必须是行首或紧跟在空白/左括号之后（因此 `a@b` 这类邮箱不会被当成引用）；
//   - 只处理光标处的 token，光标右侧的内容不参与匹配。
func chatMentionTokenAt(text string, cursor int) chatMentionToken {
	runes := []rune(text)
	if cursor < 0 || cursor > len(runes) {
		return chatMentionToken{}
	}
	at := -1
	for index := cursor - 1; index >= 0; index-- {
		r := runes[index]
		if r == '@' {
			at = index
			break
		}
		if unicode.IsSpace(r) || strings.ContainsRune("\"'`()[]{}<>", r) {
			return chatMentionToken{}
		}
	}
	if at < 0 {
		return chatMentionToken{}
	}
	if at > 0 {
		prev := runes[at-1]
		if !unicode.IsSpace(prev) && !strings.ContainsRune("([{", prev) {
			return chatMentionToken{}
		}
	}
	return chatMentionToken{Query: string(runes[at+1 : cursor]), Start: at, Valid: true}
}

// chatMentionWorkspaceRoot 返回补全扫描根：进程工作目录（chat 的工作区）。
func chatMentionWorkspaceRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

type chatMentionCandidate struct {
	Path  string // 工作区相对路径，目录以 "/" 结尾
	score int
}

// chatMentionCandidates 在工作区根下做有界扫描，返回匹配 query 的相对路径。
// 排序：路径前缀命中 > 目录/文件名命中 > 子串命中，同级按路径字典序。
func chatMentionCandidates(root, query string, limit int) []string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	if limit <= 0 {
		limit = chatMentionCandidateLimit
	}
	normalized := filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(query), "./"))
	normalizedLower := strings.ToLower(normalized)

	candidates := make([]chatMentionCandidate, 0, limit)
	scanned := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // 权限/竞争导致的读取失败直接跳过该条目
		}
		if entry.IsDir() {
			if path != root && chatMentionSkipDirs[strings.ToLower(entry.Name())] {
				return filepath.SkipDir
			}
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil
		}
		scanned++
		if scanned > chatMentionScanLimit {
			return fs.SkipAll
		}
		rel = filepath.ToSlash(rel)
		relLower := strings.ToLower(rel)
		baseLower := strings.ToLower(entry.Name())

		score := -1
		switch {
		case normalizedLower == "":
			score = 0
		case strings.HasPrefix(relLower, normalizedLower):
			score = 0
		case strings.HasPrefix(baseLower, normalizedLower):
			score = 1
		case strings.Contains(relLower, normalizedLower):
			score = 2
		}
		if score < 0 {
			return nil
		}
		if entry.IsDir() {
			rel += "/"
		}
		candidates = append(candidates, chatMentionCandidate{Path: rel, score: score})
		return nil
	})

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score < candidates[j].score
		}
		if len(candidates[i].Path) != len(candidates[j].Path) {
			return len(candidates[i].Path) < len(candidates[j].Path)
		}
		return candidates[i].Path < candidates[j].Path
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.Path)
	}
	return paths
}

// chatMentionCompletionResult 是一次 Tab 补全的结果。
type chatMentionCompletionResult struct {
	Text    string
	Cursor  int
	Status  string
	Handled bool // true 表示该按键已被 @ 引用语义消费（不得再触发 plan mode 切换）
}

// applyChatMentionCompletion 处理光标处 @ 引用的 Tab 补全：
//   - 唯一命中：补全为完整相对路径，文件补一个空格；
//   - 多命中：补到公共前缀（若比已输入更长），状态行列出候选数量与示例；
//   - 无命中：不改文本，状态行说明无匹配；
//   - 非 @ 上下文：Handled=false，交由原有 Tab 语义。
func applyChatMentionCompletion(root, text string, cursor, limit int) chatMentionCompletionResult {
	token := chatMentionTokenAt(text, cursor)
	if !token.Valid {
		return chatMentionCompletionResult{}
	}
	candidates := chatMentionCandidates(root, token.Query, limit)
	runes := []rune(text)
	notHandled := chatMentionCompletionResult{Text: text, Cursor: cursor, Handled: true}
	if len(candidates) == 0 {
		notHandled.Status = fmt.Sprintf("无匹配路径：@%s", token.Query)
		return notHandled
	}
	if len(candidates) == 1 {
		insert := candidates[0]
		if !strings.HasSuffix(insert, "/") {
			insert += " "
		}
		replacement := "@" + insert
		return chatMentionCompletionResult{
			Text:    string(runes[:token.Start]) + replacement + string(runes[cursor:]),
			Cursor:  token.Start + len([]rune(replacement)),
			Status:  "已补全：" + strings.TrimSpace(insert),
			Handled: true,
		}
	}
	prefix := chatMentionCommonPrefix(candidates)
	if len([]rune(prefix)) > len([]rune(token.Query)) {
		replacement := "@" + prefix
		return chatMentionCompletionResult{
			Text:    string(runes[:token.Start]) + replacement + string(runes[cursor:]),
			Cursor:  token.Start + len([]rune(replacement)),
			Status:  fmt.Sprintf("匹配 %d 项，已补全到 %s（再按 Tab 继续）", len(candidates), prefix),
			Handled: true,
		}
	}
	notHandled.Status = fmt.Sprintf("匹配 %d 项：%s", len(candidates), strings.Join(chatMentionPreview(candidates, 6), "  "))
	return notHandled
}

// chatMentionCommonPrefix 返回候选路径的最长公共前缀（按 rune 计算）。
// 调用方只在它比已输入内容更长时才应用，因此不会补出比用户输入更短的路径。
func chatMentionCommonPrefix(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	prefix := []rune(candidates[0])
	for _, candidate := range candidates[1:] {
		other := []rune(candidate)
		limit := len(prefix)
		if len(other) < limit {
			limit = len(other)
		}
		index := 0
		for index < limit && prefix[index] == other[index] {
			index++
		}
		prefix = prefix[:index]
		if len(prefix) == 0 {
			break
		}
	}
	trimmed := strings.TrimSpace(string(prefix))
	if trimmed == "" {
		return ""
	}
	return trimmed
}

func chatMentionPreview(candidates []string, max int) []string {
	if len(candidates) <= max {
		return candidates
	}
	preview := append([]string{}, candidates[:max]...)
	preview = append(preview, fmt.Sprintf("…（共 %d 项）", len(candidates)))
	return preview
}
