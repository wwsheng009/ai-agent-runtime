package gitbrowse

import (
	"bytes"
	"context"
	"fmt"
	"os"
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
	File   FileInfo `json:"file"`
	Target string   `json:"target"`
	// EffectiveTarget 是本次 diff **实际**使用的目标；TargetFallback=true 表示请求目标对这条路径
	// 没有任何改动、而另一侧有改动，服务端据此回退到另一侧（UI 必须说明「显示的是哪一侧的改动」）。
	EffectiveTarget string `json:"effective_target"`
	TargetFallback  bool   `json:"target_fallback"`
	Context         int    `json:"context"`
	Whitespace      string `json:"whitespace"`
	Insertions      int    `json:"insertions"`
	Deletions       int    `json:"deletions"`
	Hunks           []Hunk `json:"hunks"`
	Raw             string `json:"raw"`
	ParseError      string `json:"parse_error"`
	Truncated       bool   `json:"truncated"`
	TruncatedReason string `json:"truncated_reason"`
	GeneratedAt     int64  `json:"generated_at"`
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

	result, parsed, effective, fallback, err := s.resolveDiffOutput(
		ctx, gitCtx, target, repoRel, scopeRel, contextLines, whitespace,
	)
	if err != nil {
		return nil, err
	}

	raw := string(result.stdout)
	if result.synthesizedNew && parsed.status == "M" {
		// 空的新文件没有任何 hunk（--no-index 无输出），但结论仍然是「新增」。
		parsed.status = "A"
	}
	diff := &DiffResult{
		File: FileInfo{
			Path:        scopeRel,
			AbsPath:     filepath.Join(gitCtx.ScopeRoot, filepath.FromSlash(scopeRel)),
			OldPath:     parsed.oldPath,
			Status:      parsed.status,
			IsBinary:    parsed.isBinary,
			IsSubmodule: parsed.isSubmodule,
		},
		Target:          target.label(),
		EffectiveTarget: effective.label(),
		TargetFallback:  fallback,
		Context:         contextLines,
		Whitespace:      whitespace,
		Insertions:      parsed.insertions,
		Deletions:       parsed.deletions,
		Hunks:           parsed.hunks,
		Raw:             raw,
		ParseError:      parsed.err,
		GeneratedAt:     time.Now().Unix(),
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

// devNullPath 是 `--no-index` 合成新增文件 diff 时的空侧路径；
// Git for Windows 同样识别该路径（POSIX 语义由 git 自身处理）。
const devNullPath = "/dev/null"

// diffOutput 是一次 diff 的原始输出及其口径标记。
type diffOutput struct {
	stdout         []byte
	truncated      bool
	synthesizedNew bool // 是否由 `--no-index` 为未跟踪文件合成
}

// diffOutput 执行目标 diff 并返回原始输出。
//
// 未跟踪文件不在索引里，`git diff -- <path>` 对它没有任何输出：直接照抄会在 diff 视图里
// 显示成「没有改动」，与事实相反（新文件的内容恰恰是用户要看的东西）。因此「工作区目标 +
// 未跟踪文件」改用 `git diff --no-index -- /dev/null <file>` 合成新增文件 diff：
//   - 进程根取作用域根，输出头部即作用域相对路径，与 FileInfo.Path / file 参数同一口径；
//   - 两侧存在差异时退出码为 1，必须按正常结果处理（不是失败）；
//   - 目录 / 已消失 / 非常规文件不合成，退回常规 git diff，让它如实给出空结果或真实错误。
func (s *Service) diffOutput(
	ctx context.Context,
	gitCtx *gitContext,
	target diffTarget,
	repoRel, scopeRel string,
	contextLines int,
	whitespace string,
) (*diffOutput, error) {
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

	if target.name == "working" {
		untracked, err := s.isUntracked(ctx, gitCtx, repoRel)
		if err != nil {
			return nil, err
		}
		absPath := filepath.Join(gitCtx.ScopeRoot, filepath.FromSlash(scopeRel))
		if untracked && isDiffableFile(absPath) {
			args = append(args, "--no-index", "--", devNullPath, scopeRel)
			result, err := s.runner.gitAllowingExit(
				ctx, gitCtx.ScopeRoot, "git diff --no-index", s.limits.DiffTimeout, 0,
				map[int]bool{1: true}, args...,
			)
			if err != nil {
				return nil, err
			}
			if failure := noIndexFailure(result); failure != nil {
				return nil, failure
			}
			return &diffOutput{stdout: result.stdout, truncated: result.truncated, synthesizedNew: true}, nil
		}
	}

	args = append(args, "--", repoRel)
	result, err := s.runner.git(ctx, gitCtx.RepoRoot, "git diff", s.limits.DiffTimeout, 0, args...)
	if err != nil {
		return nil, err
	}
	return &diffOutput{stdout: result.stdout, truncated: result.truncated}, nil
}

// isUntracked 判断仓库相对路径是否为未跟踪文件；口径与 status 的未跟踪分组一致
// （`--others --exclude-standard`，被 .gitignore 覆盖的路径不会误判）。
func (s *Service) isUntracked(ctx context.Context, gitCtx *gitContext, repoRel string) (bool, error) {
	result, err := s.runner.git(ctx, gitCtx.RepoRoot, "git ls-files --others", s.limits.StatusTimeout,
		probeMaxOutputBytes, "ls-files", "--others", "--exclude-standard", "-z", "--", repoRel)
	if err != nil {
		return false, err
	}
	return len(result.stdout) > 0, nil
}

// isDiffableFile 判断未跟踪路径能否用 `--no-index` 合成 diff（普通文件或符号链接）。
func isDiffableFile(absPath string) bool {
	info, err := os.Lstat(absPath)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0
}

// noIndexFailure 区分 `--no-index` 退出码 1 的两种含义：
// 两侧存在差异时 stdout 一定有内容；stdout 为空而 stderr 有内容说明 git 根本没读到路径
// （典型是「状态里还是未跟踪、点开前已被删除」）。这时绝不能当成「空的新文件」，
// 必须如实报错——git_failed 而非 not_found，避免前端把文件消失误显示成「仓库不可用」。
func noIndexFailure(result *cmdResult) error {
	if len(bytes.TrimSpace(result.stdout)) > 0 || len(bytes.TrimSpace(result.stderr)) == 0 {
		return nil
	}
	failure := newError(CodeGitFailed, fmt.Sprintf(
		"git diff --no-index could not read the file: %s",
		truncateText(strings.TrimSpace(string(result.stderr)), 200),
	))
	failure.ExitCode = result.exitCode
	return failure
}

// label 返回目标的对外名字（commit 目标带 sha 前缀）。
func (t diffTarget) label() string {
	if t.name == "commit" {
		return "commit:" + t.sha
	}
	return t.name
}

// otherSide 返回工作区 / 暂存区中的另一侧；commit 目标没有「另一侧」（用户显式选了历史版本）。
func (t diffTarget) otherSide() (diffTarget, bool) {
	switch t.name {
	case "working":
		return diffTarget{name: "staged"}, true
	case "staged":
		return diffTarget{name: "working"}, true
	default:
		return diffTarget{}, false
	}
}

// hasParsedChange 判断解析结果本身是否足以断定「该目标对这条路径有变更」。
// 空的新文件、空文件删除在 unified diff 里可能连文件头与 hunk 都没有，因此还要看
// synthesizedNew（未跟踪文件合成路径）与 status（A/D/R 只有真读到对应文件头才可能出现）。
func hasParsedChange(parsed *diffParse, out *diffOutput) bool {
	return out.synthesizedNew || len(parsed.hunks) > 0 || parsed.status != "M" || parsed.err != ""
}

// diffStatus 用 `--name-status` 判定「该目标对这条路径有没有变更」并取状态首字母（A/D/M/R/T/C）。
// 它是 unified diff 解析不出结论时的权威补充：空的新文件、被删除的空文件、仅模式变更在
// unified diff 里都可能没有任何可供解析的行。
func (s *Service) diffStatus(
	ctx context.Context,
	gitCtx *gitContext,
	target diffTarget,
	repoRel string,
) (string, bool, error) {
	args := []string{"diff", "--name-status", "-z", "--no-color", "--no-ext-diff"}
	switch target.name {
	case "staged":
		args = append(args, "--cached")
	case "commit":
		args = append(args, target.sha+"^!")
	}
	args = append(args, "--", repoRel)
	result, err := s.runner.git(ctx, gitCtx.RepoRoot, "git diff --name-status", s.limits.DiffTimeout, 0, args...)
	if err != nil {
		return "", false, err
	}
	// -z 输出：`<status>\0<path>\0`（重命名/复制为 `<status>\0<old>\0<new>\0`），状态可能是 R100 / C75。
	fields := strings.Split(strings.TrimRight(string(result.stdout), "\x00"), "\x00")
	if len(fields) == 0 {
		return "", false, nil
	}
	status := strings.ToUpper(strings.TrimSpace(fields[0]))
	status = strings.TrimRight(status, "0123456789")
	if status == "" {
		return "", false, nil
	}
	return status[:1], true, nil
}

// resolveDiffOutput 计算最终要展示的 diff：请求目标没有这条路径的改动、而另一侧有时自动回退到
// 另一侧（effective/fallback 如实回报，UI 据此说明「显示的哪一侧」）。
//
// 动机：变更列表按「改动在哪一侧」分组（未暂存 / 未跟踪 / 已暂存），而 diff 目标由用户选的
// 「工作区 / 已暂存」决定。用户点一个只在另一侧有改动的文件（典型：未跟踪新文件 + 目标=已暂存，
// 或已暂存删除 + 目标=工作区）时，直接照抄该目标的空输出会把「改动就在那儿、只是不在这一侧」
// 显示成「没有差异」。回退判定只使用「有没有改动」的事实，不猜测内容。
func (s *Service) resolveDiffOutput(
	ctx context.Context,
	gitCtx *gitContext,
	target diffTarget,
	repoRel, scopeRel string,
	contextLines int,
	whitespace string,
) (*diffOutput, *diffParse, diffTarget, bool, error) {
	out, err := s.diffOutput(ctx, gitCtx, target, repoRel, scopeRel, contextLines, whitespace)
	if err != nil {
		return nil, nil, target, false, err
	}
	parsed := parseUnifiedDiff(string(out.stdout), out.truncated)
	if hasParsedChange(parsed, out) {
		return out, parsed, target, false, nil
	}

	// 该目标对这条路径到底有没有改动：空文件、仅模式变更等在 diff 文本里可能什么行都没有。
	status, present, err := s.diffStatus(ctx, gitCtx, target, repoRel)
	if err != nil {
		return nil, nil, target, false, err
	}
	if present {
		applyNameStatus(parsed, status)
		return out, parsed, target, false, nil
	}

	other, ok := target.otherSide()
	if !ok {
		return out, parsed, target, false, nil
	}
	altOut, altErr := s.diffOutput(ctx, gitCtx, other, repoRel, scopeRel, contextLines, whitespace)
	if altErr != nil {
		// 回退探测失败不影响主结论：按「该目标没有差异」如实返回（错误不冒充别的事实）。
		return out, parsed, target, false, nil
	}
	altParsed := parseUnifiedDiff(string(altOut.stdout), altOut.truncated)
	if hasParsedChange(altParsed, altOut) {
		return altOut, altParsed, other, true, nil
	}
	if status, present, err := s.diffStatus(ctx, gitCtx, other, repoRel); err == nil && present {
		applyNameStatus(altParsed, status)
		return altOut, altParsed, other, true, nil
	}
	return out, parsed, target, false, nil
}

// applyNameStatus 在解析结果没有任何文件头时用 `--name-status` 的结论补状态字母。
func applyNameStatus(parsed *diffParse, status string) {
	if parsed.status == "M" && status != "" && status != "M" {
		parsed.status = status
	}
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
