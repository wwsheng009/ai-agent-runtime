package gitbrowse

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DiffRequest 是 GET /git/diff 的参数。
//
//	target:     working | staged | commit:<sha>
//	context:    上下文行数（默认 3，上限 100000，供「展开整个文件」使用）
//	whitespace: show | ignore_all（ignore_all 走服务端 --ignore-all-space 重新取 diff）
type DiffRequest struct {
	RepoRequest
	File       string
	Target     string
	Context    int
	Whitespace string
}

// FileInfo 是 diff 的文件头（§5.6）；old_path 为 null 表示非重命名/复制。
type FileInfo struct {
	Path        string  `json:"path"`     // 作用域相对路径
	AbsPath     string  `json:"abs_path"` // 绝对路径，便于前端拼接本地工具
	OldPath     *string `json:"old_path"`
	Status      string  `json:"status"`
	IsBinary    bool    `json:"is_binary"`
	IsSubmodule bool    `json:"is_submodule"`
}

// HunkLine 是 hunk 内的一行。old_no/new_no 用指针以输出 null（0 是合法行号，不能作为缺省）。
type HunkLine struct {
	Type  string `json:"type"` // context | add | del | nonewline
	OldNo *int   `json:"old_no"`
	NewNo *int   `json:"new_no"`
	Text  string `json:"text"`
}

// Hunk 是结构化 hunk（行文本不做 HTML 转义，前端用文本节点渲染）。
type Hunk struct {
	Header   string     `json:"header"`
	OldStart int        `json:"old_start"`
	OldLines int        `json:"old_lines"`
	NewStart int        `json:"new_start"`
	NewLines int        `json:"new_lines"`
	Lines    []HunkLine `json:"lines"`
}

// DiffResult 是 GET /git/diff 的响应（§5.6）。
type DiffResult struct {
	File            FileInfo `json:"file"`
	Target          string   `json:"target"`
	Context         int      `json:"context"`
	Whitespace      string   `json:"whitespace"`
	Insertions      int      `json:"insertions"`
	Deletions       int      `json:"deletions"`
	Hunks           []Hunk   `json:"hunks"`
	Raw             string   `json:"raw"`
	ParseError      string   `json:"parse_error"`
	Truncated       bool     `json:"truncated"`
	TruncatedReason string   `json:"truncated_reason"`
	GeneratedAt     int64    `json:"generated_at"`
}

// diffTarget 表示解析后的 diff 目标。
type diffTarget struct {
	name string // working | staged | commit
	sha  string // 仅 commit 目标
}

// Diff 执行结构化 diff：`git diff --no-color --no-ext-diff --find-renames [--cached|<sha>^!] ...`。
// 解析失败不返回 500，而是 hunks=[] + parse_error，让前端降级为纯文本展示。
func (s *Service) Diff(ctx context.Context, req DiffRequest) (*DiffResult, error) {
	gitCtx, err := s.prepare(ctx, req.RepoRequest)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.File) == "" {
		return nil, newError(CodePathInvalid, "file is required")
	}
	scopeRel, err := s.normalizeFile(gitCtx.ScopeRoot, req.File)
	if err != nil {
		return nil, err
	}
	repoRel, err := repoRelative(gitCtx.ScopeRoot, gitCtx.RepoRoot, scopeRel)
	if err != nil {
		return nil, err
	}
	target, err := normalizeTarget(req.Target)
	if err != nil {
		return nil, err
	}
	if err := rejectOptionLike("file", repoRel); err != nil {
		return nil, err
	}
	contextLines := clampContext(req.Context)
	whitespace := normalizeWhitespace(req.Whitespace)

	args := []string{
		"diff", "--no-color", "--no-ext-diff", "--find-renames",
		"--unified=" + strconv.Itoa(contextLines),
	}
	if whitespace == "ignore_all" {
		args = append(args, "--ignore-all-space")
	}
	switch target.name {
	case "staged":
		args = append(args, "--cached")
	case "commit":
		// `<sha>^!` = 该提交自身的变更（对首提交等价于空树对比，对合取首个父提交）。
		args = append(args, target.sha+"^!")
	}
	args = append(args, "--", repoRel)

	result, err := s.runner.git(ctx, gitCtx.RepoRoot, "git diff", s.limits.DiffTimeout, 0, args...)
	if err != nil {
		return nil, err
	}

	raw := string(result.stdout)
	parsed := parseUnifiedDiff(raw, result.truncated)
	diff := &DiffResult{
		File: FileInfo{
			Path:        scopeRel,
			AbsPath:     filepath.Join(gitCtx.ScopeRoot, filepath.FromSlash(scopeRel)),
			OldPath:     parsed.oldPath,
			Status:      parsed.status,
			IsBinary:    parsed.isBinary,
			IsSubmodule: parsed.isSubmodule,
		},
		Target:      target.name,
		Context:     contextLines,
		Whitespace:  whitespace,
		Insertions:  parsed.insertions,
		Deletions:   parsed.deletions,
		Hunks:       parsed.hunks,
		Raw:         raw,
		ParseError:  parsed.err,
		GeneratedAt: time.Now().Unix(),
	}
	if target.name == "commit" {
		diff.Target = "commit:" + target.sha
	}

	// 字节上限由 runner 截断；行数上限在这里按 hunk 边界截断。
	if result.truncated {
		diff.Truncated = true
		diff.TruncatedReason = fmt.Sprintf("output exceeds %s", formatBytes(s.limits.MaxOutputBytes))
	}
	if rawLines := strings.Split(raw, "\n"); len(rawLines) > s.limits.MaxDiffLines {
		diff.Raw = strings.Join(rawLines[:s.limits.MaxDiffLines], "\n")
		diff.Hunks = filterHunksByLine(parsed, s.limits.MaxDiffLines)
		diff.Truncated = true
		diff.TruncatedReason = fmt.Sprintf("diff exceeds %d lines", s.limits.MaxDiffLines)
	}
	return diff, nil
}

// normalizeTarget 解析 target 参数（working/staged/commit:<sha>）。
func normalizeTarget(value string) (diffTarget, error) {
	trimmed := strings.TrimSpace(value)
	switch {
	case trimmed == "" || trimmed == "working":
		return diffTarget{name: "working"}, nil
	case trimmed == "staged":
		return diffTarget{name: "staged"}, nil
	case strings.HasPrefix(trimmed, "commit:"):
		sha := strings.TrimSpace(strings.TrimPrefix(trimmed, "commit:"))
		if !validRevision(sha) {
			return diffTarget{}, errorf(CodeTargetInvalid, "invalid commit sha in target: %q", trimmed)
		}
		return diffTarget{name: "commit", sha: sha}, nil
	default:
		return diffTarget{}, errorf(CodeTargetInvalid, `target must be "working", "staged" or "commit:<sha>": %q`, value)
	}
}

// clampContext 归一化上下文行数：默认 3，负数归 0，超上限归 100000（「展开整个文件」）。
func clampContext(value int) int {
	const maxContext = 100000
	switch {
	case value <= 0:
		return 3
	case value > maxContext:
		return maxContext
	default:
		return value
	}
}

// normalizeWhitespace 归一化空白策略：未知值按 show 处理。
func normalizeWhitespace(value string) string {
	if strings.TrimSpace(value) == "ignore_all" {
		return "ignore_all"
	}
	return "show"
}

// diffParse 是 unified diff 的解析结果。
type diffParse struct {
	hunks       []Hunk
	oldPath     *string
	status      string
	isBinary    bool
	isSubmodule bool
	insertions  int
	deletions   int
	lineStarts  []int // 每个 hunk 在原文中的起始行号（0-based）
	err         string
}

// parseUnifiedDiff 把 unified diff 解析为结构化 hunks（§5.6）。
// 解析失败时返回 hunks=[] + err（调用方保留 raw，绝不 500）；
// outputTruncated=true 表示 stdout 已被上限截断，此时对尾部残缺不做 parse_error 判定。
func parseUnifiedDiff(raw string, outputTruncated bool) *diffParse {
	parsed := &diffParse{hunks: []Hunk{}, status: "M"}
	lines := strings.Split(raw, "\n")
	var current *Hunk
	remainingOld, remainingNew := 0, 0
	for idx := 0; idx < len(lines); idx++ {
		line := lines[idx]
		// `\ No newline at end of file` 紧跟其所属行（含 hunk 最后一行），
		// 必须在上面的计数值归零、current 被重置之前处理，否则会丢掉尾标记。
		if current != nil && strings.HasPrefix(line, `\`) {
			appendHunkLine(current, HunkLine{Type: "nonewline", Text: line})
			continue
		}
		if remainingOld == 0 && remainingNew == 0 {
			current = nil
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// 仅单文件 diff 参与结构化解析；多文件（理论上不会出现）按原样保留 raw。
			continue
		case strings.HasPrefix(line, "rename from "):
			value := unquoteGitPath(strings.TrimPrefix(line, "rename from "))
			parsed.oldPath = &value
			parsed.status = "R"
			continue
		case strings.HasPrefix(line, "new file mode "):
			parsed.status = "A"
			continue
		case strings.HasPrefix(line, "deleted file mode "):
			parsed.status = "D"
			continue
		case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
			parsed.isBinary = true
			continue
		case strings.HasPrefix(line, "Subproject commit "):
			parsed.isSubmodule = true
			continue
		case strings.HasPrefix(line, "@@"):
			// current 只有在「上一行仍在补足 hunk」时才非 nil；已喂满的 hunk 会在循环开头被重置。
			if current != nil && (remainingOld > 0 || remainingNew > 0) && !outputTruncated {
				parsed.err = fmt.Sprintf("hunk %d ended early at line %d", len(parsed.hunks), idx+1)
				parsed.hunks = []Hunk{}
				return parsed
			}
			hunk, ok := parseHunkHeader(line)
			if !ok {
				if outputTruncated {
					continue
				}
				parsed.err = fmt.Sprintf("unparsable hunk header at line %d: %s", idx+1, truncateText(line, 120))
				parsed.hunks = []Hunk{}
				return parsed
			}
			remainingOld, remainingNew = hunk.OldLines, hunk.NewLines
			parsed.hunks = append(parsed.hunks, hunk)
			parsed.lineStarts = append(parsed.lineStarts, idx)
			current = &parsed.hunks[len(parsed.hunks)-1]
			continue
		}
		if current == nil {
			// 文件头（index / --- / +++ / similarity index / mode 等）与空行：忽略。
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			lineNo := current.NewStart + current.NewLines - remainingNew
			remainingNew--
			appendHunkLine(current, HunkLine{Type: "add", NewNo: intPtr(lineNo), Text: line[1:]})
			parsed.insertions++
		case strings.HasPrefix(line, "-"):
			lineNo := current.OldStart + current.OldLines - remainingOld
			remainingOld--
			appendHunkLine(current, HunkLine{Type: "del", OldNo: intPtr(lineNo), Text: line[1:]})
			parsed.deletions++
		default:
			if remainingOld == 0 && remainingNew == 0 && line == "" {
				continue
			}
			oldNo := current.OldStart + current.OldLines - remainingOld
			newNo := current.NewStart + current.NewLines - remainingNew
			remainingOld--
			remainingNew--
			appendHunkLine(current, HunkLine{Type: "context", OldNo: intPtr(oldNo), NewNo: intPtr(newNo), Text: strings.TrimPrefix(line, " ")})
		}
	}
	return parsed
}

// parseHunkHeader 解析 `@@ -a,b +c,d @@ section`（省略计数时按 1 处理）。
func parseHunkHeader(line string) (Hunk, bool) {
	body := strings.TrimPrefix(line, "@@")
	closing := strings.Index(body, "@@")
	if closing < 0 {
		return Hunk{}, false
	}
	ranges := strings.Fields(strings.TrimSpace(body[:closing]))
	if len(ranges) != 2 || !strings.HasPrefix(ranges[0], "-") || !strings.HasPrefix(ranges[1], "+") {
		return Hunk{}, false
	}
	oldStart, oldLines, ok := parseHunkRange(ranges[0][1:])
	if !ok {
		return Hunk{}, false
	}
	newStart, newLines, ok := parseHunkRange(ranges[1][1:])
	if !ok {
		return Hunk{}, false
	}
	return Hunk{
		Header:   line,
		OldStart: oldStart,
		OldLines: oldLines,
		NewStart: newStart,
		NewLines: newLines,
		Lines:    []HunkLine{},
	}, true
}

// parseHunkRange 解析 `start[,lines]`。
func parseHunkRange(value string) (int, int, bool) {
	parts := strings.SplitN(value, ",", 2)
	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	if len(parts) == 1 {
		return start, 1, true
	}
	count, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return start, count, true
}

// appendHunkLine 追加一行到 hunk（保证 lines 不为 null）。
func appendHunkLine(hunk *Hunk, line HunkLine) {
	if hunk.Lines == nil {
		hunk.Lines = []HunkLine{}
	}
	hunk.Lines = append(hunk.Lines, line)
}

// filterHunksByLine 只保留起始行在行数上限之内的 hunks。
func filterHunksByLine(parsed *diffParse, maxLines int) []Hunk {
	kept := make([]Hunk, 0, len(parsed.hunks))
	for i, hunk := range parsed.hunks {
		if i < len(parsed.lineStarts) && parsed.lineStarts[i] >= maxLines {
			break
		}
		kept = append(kept, hunk)
	}
	return kept
}

// intPtr 返回行号指针（JSON 中输出为数字；nil 输出为 null）。
func intPtr(value int) *int {
	return &value
}

// truncateText 截断过长文本用于错误消息。
func truncateText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

// unquoteGitPath 去掉 git 在 diff 头部对特殊路径加的 C 风格引号。
func unquoteGitPath(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 2 || !strings.HasPrefix(trimmed, `"`) || !strings.HasSuffix(trimmed, `"`) {
		return trimmed
	}
	inner := trimmed[1 : len(trimmed)-1]
	var builder strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] != '\\' || i+1 >= len(inner) {
			builder.WriteByte(inner[i])
			continue
		}
		i++
		switch inner[i] {
		case 'n':
			builder.WriteByte('\n')
		case 't':
			builder.WriteByte('\t')
		default:
			builder.WriteByte(inner[i])
		}
	}
	return builder.String()
}

// joinRepoPath 把仓库相对 POSIX 路径还原为绝对路径。
func joinRepoPath(repoRoot, repoRel string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(repoRel))
}
