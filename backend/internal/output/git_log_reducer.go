package output

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// GitLogReducer 压缩 git history 输出，避免整段 log 进入上下文。
type GitLogReducer struct{}

// Name 返回 reducer 名称。
func (r *GitLogReducer) Name() string {
	return "git_log"
}

// Reduce 提取 commit 数、revert 风险和高频触达目录。
func (r *GitLogReducer) Reduce(_ context.Context, input ReducedInput) (*Envelope, bool, error) {
	if !looksLikeGitLog(input.Raw.ToolName, input.Raw.Metadata, input.Text) {
		return nil, false, nil
	}

	lines := strings.Split(strings.ReplaceAll(input.Text, "\r\n", "\n"), "\n")
	commitCount := 0
	revertCount := 0
	authors := make(map[string]int)
	paths := make(map[string]int)
	subjects := make([]string, 0, 3)

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)

		switch {
		case strings.HasPrefix(line, "commit "):
			commitCount++
		case looksLikeOnelineCommit(line):
			commitCount++
			subject := strings.TrimSpace(line[8:])
			if subject != "" && len(subjects) < 3 {
				subjects = append(subjects, summarizeLine(subject, 120))
			}
		case strings.HasPrefix(line, "Author:"):
			author := strings.TrimSpace(strings.TrimPrefix(line, "Author:"))
			if author != "" {
				authors[author]++
			}
		case strings.Contains(line, "|"):
			path := strings.TrimSpace(strings.Split(line, "|")[0])
			if prefix := topLevelPath(path); prefix != "" {
				paths[prefix]++
			}
		}

		if strings.Contains(lower, "revert") {
			revertCount++
		}
	}

	summary := fmt.Sprintf("Parsed git history: %d commits, %d revert-like entries", commitCount, revertCount)
	if len(authors) > 0 {
		summary += fmt.Sprintf(", %d authors", len(authors))
	}

	keyFacts := make([]string, 0, 6)
	if topAuthors := topCounts(authors, 3); len(topAuthors) > 0 {
		keyFacts = append(keyFacts, "authors="+strings.Join(topAuthors, ", "))
	}
	if topTouched := topCounts(paths, 3); len(topTouched) > 0 {
		keyFacts = append(keyFacts, "touched_paths="+strings.Join(topTouched, ", "))
	}
	for _, subject := range subjects {
		keyFacts = append(keyFacts, "recent_commit="+subject)
	}

	return &Envelope{
		ToolName:   input.Raw.ToolName,
		ToolCallID: input.Raw.ToolCallID,
		Summary:    summary,
		Error:      strings.TrimSpace(input.Raw.Error),
		Metadata: map[string]interface{}{
			"commit_count": commitCount,
			"revert_count": revertCount,
			"key_facts":    keyFacts,
		},
	}, true, nil
}

func looksLikeGitLog(toolName string, metadata map[string]interface{}, text string) bool {
	lowerTool := strings.ToLower(toolName)
	if strings.Contains(lowerTool, "git") {
		return true
	}
	if metadata != nil {
		if cmd, ok := metadata["cmd"].(string); ok && strings.EqualFold(cmd, "git") {
			return true
		}
	}

	lowerText := strings.ToLower(text)
	return strings.Contains(lowerText, "\ncommit ") ||
		strings.Contains(lowerText, "\nauthor:") ||
		strings.Contains(lowerText, "files changed")
}

func looksLikeOnelineCommit(line string) bool {
	if len(line) < 9 {
		return false
	}
	hash := line[:7]
	for _, ch := range hash {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		default:
			return false
		}
	}
	return line[7] == ' ' || line[7] == '\t'
}

func topLevelPath(path string) string {
	path = strings.TrimSpace(strings.TrimPrefix(path, "./"))
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func topCounts(values map[string]int, limit int) []string {
	type item struct {
		key   string
		count int
	}

	items := make([]item, 0, len(values))
	for key, count := range values {
		items = append(items, item{key: key, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].key < items[j].key
		}
		return items[i].count > items[j].count
	})

	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprintf("%s(%d)", item.key, item.count))
	}
	return out
}
