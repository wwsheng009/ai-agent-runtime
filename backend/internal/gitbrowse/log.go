package gitbrowse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CommitsRequest 是 GET /git/commits 的参数；Cursor 为上一页最后一条的 sha（空 = 第一页）。
type CommitsRequest struct {
	RepoRequest
	Limit  int
	Cursor string
}

// Commit 是提交列表中的一条（refs 永远是非 null 数组）。
type Commit struct {
	SHA     string   `json:"sha"`
	Short   string   `json:"short"`
	Author  string   `json:"author"`
	Date    string   `json:"date"` // ISO8601（%aI）
	Subject string   `json:"subject"`
	Refs    []string `json:"refs"`
}

// CommitsResult 是 GET /git/commits 的响应。
type CommitsResult struct {
	Commits     []Commit `json:"commits"`
	HasMore     bool     `json:"has_more"`
	NextCursor  string   `json:"next_cursor"` // 下一页 cursor（= 本页最后一条 sha）；无更多为空串
	Limit       int      `json:"limit"`
	Warnings    []string `json:"warnings"`
	GeneratedAt int64    `json:"generated_at"`
}

// Commits 执行 `git log --max-count=<limit+1> --pretty=format:…`，用 cursor=<sha> 分页。
// 多取一条用于判定 has_more，避免前端「最后一页还要再请求一次」。
func (s *Service) Commits(ctx context.Context, req CommitsRequest) (*CommitsResult, error) {
	gitCtx, err := s.prepare(ctx, req.RepoRequest)
	if err != nil {
		return nil, err
	}
	limit := s.clampCommitLimit(req.Limit)

	args := []string{
		"log",
		fmt.Sprintf("--max-count=%d", limit+1),
		"--pretty=format:%H%x1f%h%x1f%an%x1f%aI%x1f%s%x1f%D",
	}
	cursor := strings.TrimSpace(req.Cursor)
	if cursor != "" {
		if !isHexRevision(cursor) {
			return nil, errorf(CodeCursorInvalid, "invalid commit cursor: %q", cursor)
		}
		// --skip=1 跳过 cursor 自身，等价于从它的父提交开始，避免大仓库 --skip 偏移性能问题。
		args = append(args, "--skip=1", cursor)
	}

	result, err := s.runner.git(ctx, gitCtx.RepoRoot, "git log", s.limits.LogTimeout, 0, args...)
	if err != nil {
		// 刚 git init 的仓库没有 HEAD 提交：返回空列表而不是 500（前端状态栏要能正常打开）。
		if isUnbornHead(err) {
			return &CommitsResult{
				Commits:     []Commit{},
				Limit:       limit,
				Warnings:    []string{"repository has no commits yet"},
				GeneratedAt: time.Now().Unix(),
			}, nil
		}
		return nil, err
	}

	commits, warnings := parseCommitLog(result.stdout, result.truncated)
	hasMore := len(commits) > limit
	if hasMore {
		commits = commits[:limit]
	}
	payload := &CommitsResult{
		Commits:     commits,
		HasMore:     hasMore,
		Limit:       limit,
		Warnings:    warnings,
		GeneratedAt: time.Now().Unix(),
	}
	if hasMore && len(commits) > 0 {
		payload.NextCursor = commits[len(commits)-1].SHA
	}
	return payload, nil
}

// clampCommitLimit 归一化 limit：默认 50，上限由 Limits.MaxCommits 控制（默认 200）。
func (s *Service) clampCommitLimit(value int) int {
	limit := value
	if limit <= 0 {
		limit = 50
	}
	if max := s.limits.MaxCommits; max > 0 && limit > max {
		limit = max
	}
	return limit
}

// isUnbornHead 判断错误是否为「分支还没有任何提交」（git 文案受 LC_ALL=C 约束，稳定可匹配）。
func isUnbornHead(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "does not have any commits yet")
}

// parseCommitLog 解析 `%H%x1f%h%x1f%an%x1f%aI%x1f%s%x1f%D` 输出（每条一行）。
// 形态异常的记录记 warning 并跳过，不猜测语义。
func parseCommitLog(out []byte, truncated bool) ([]Commit, []string) {
	commits := []Commit{}
	warnings := []string{}
	text := string(out)
	if truncated {
		// 尾行可能被截断：直接丢弃最后一行。
		if lastNewline := strings.LastIndex(text, "\n"); lastNewline >= 0 {
			text = text[:lastNewline]
		} else {
			text = ""
		}
		warnings = append(warnings, "git log output was truncated; the last record was dropped")
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\x1f", 6)
		if len(fields) != 6 {
			warnings = append(warnings, fmt.Sprintf("unrecognized git log record skipped: %q", truncateText(line, 120)))
			continue
		}
		commits = append(commits, Commit{
			SHA:     fields[0],
			Short:   fields[1],
			Author:  fields[2],
			Date:    fields[3],
			Subject: fields[4],
			Refs:    splitRefs(fields[5]),
		})
	}
	return commits, warnings
}

// splitRefs 把 %D（如 "HEAD -> main, origin/main, tag: v1"）拆成列表。
func splitRefs(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return []string{}
	}
	parts := strings.Split(trimmed, ",")
	refs := make([]string, 0, len(parts))
	for _, part := range parts {
		if ref := strings.TrimSpace(part); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}
