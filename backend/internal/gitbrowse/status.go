package gitbrowse

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RepoStatus 是仓库元信息（§5.5 的 repo 对象）。
type RepoStatus struct {
	Root     string `json:"root"`
	Branch   string `json:"branch"`
	Detached bool   `json:"detached"`
	Head     string `json:"head"`      // 短 sha（7 位），detached/未提交时可能为空
	HeadFull string `json:"head_full"` // 完整 sha（附加字段，便于精确跳转）
	Upstream string `json:"upstream"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	IsBare   bool   `json:"is_bare"`
}

// ChangeEntry 是变更列表中的一行；path 为作用域相对 POSIX 路径（与 /git/diff 的 file 参数一致）。
type ChangeEntry struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	XY         string `json:"xy,omitempty"` // porcelain v2 原始 XY（附加字段，不丢信息）
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	Binary     bool   `json:"binary"`
	From       string `json:"from,omitempty"` // 重命名的旧路径
}

// RenameEntry 是重命名/复制条目的成对信息（§5.5 的 renames 分组）。
type RenameEntry struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status"`
}

// StatusResult 是 GET /git/status 的响应（§5.5）。
type StatusResult struct {
	Repo        RepoStatus    `json:"repo"`
	Clean       bool          `json:"clean"`
	Staged      []ChangeEntry `json:"staged"`
	Unstaged    []ChangeEntry `json:"unstaged"`
	Untracked   []ChangeEntry `json:"untracked"`
	Conflicts   []ChangeEntry `json:"conflicts"`
	Renames     []RenameEntry `json:"renames"`
	Warnings    []string      `json:"warnings"`
	GeneratedAt int64         `json:"generated_at"`
}

// Status 执行 `git status --porcelain=v2 --branch -z --untracked-files=all` 并合并 numstat 行数。
func (s *Service) Status(ctx context.Context, req RepoRequest) (*StatusResult, error) {
	gitCtx, err := s.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.status(ctx, gitCtx)
}

// status 是已解析作用域/仓库后的状态查询（Stage 复用它返回更新后的摘要）。
func (s *Service) status(ctx context.Context, gitCtx *gitContext) (*StatusResult, error) {
	result, err := s.runner.git(ctx, gitCtx.RepoRoot, "git status", s.limits.StatusTimeout, 0,
		"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}

	parsed, err := parsePorcelainV2(result.stdout)
	if err != nil {
		return nil, err
	}
	parsed.Repo.Root = gitCtx.RepoRoot

	// 行数来自 numstat；失败的降级为 warning（不隐藏变更列表本身）。
	working, err := s.numstat(ctx, gitCtx, false)
	if err != nil {
		parsed.Warnings = append(parsed.Warnings, "line counts (working tree) unavailable: "+err.Error())
	}
	staged, err := s.numstat(ctx, gitCtx, true)
	if err != nil {
		parsed.Warnings = append(parsed.Warnings, "line counts (staged) unavailable: "+err.Error())
	}
	for i := range parsed.Staged {
		fillCounts(&parsed.Staged[i], staged)
	}
	for i := range parsed.Unstaged {
		fillCounts(&parsed.Unstaged[i], working)
	}

	// 仓库相对路径 → 作用域相对路径；作用域之外的行必须显式计入 warnings，不静默丢弃。
	hidden := 0
	parsed.Staged, hidden = toScopeEntries(parsed.Staged, gitCtx, hidden)
	parsed.Unstaged, hidden = toScopeEntries(parsed.Unstaged, gitCtx, hidden)
	parsed.Untracked, hidden = toScopeEntries(parsed.Untracked, gitCtx, hidden)
	parsed.Conflicts, hidden = toScopeEntries(parsed.Conflicts, gitCtx, hidden)
	parsed.Renames, hidden = toScopeRenames(parsed.Renames, gitCtx, hidden)
	if hidden > 0 {
		parsed.Warnings = append(parsed.Warnings, fmt.Sprintf(
			"%d changed path(s) outside the selected root were hidden", hidden))
	}

	sortEntries(parsed.Staged)
	sortEntries(parsed.Unstaged)
	sortEntries(parsed.Untracked)
	sortEntries(parsed.Conflicts)
	sort.SliceStable(parsed.Renames, func(i, j int) bool {
		return strings.ToLower(parsed.Renames[i].To) < strings.ToLower(parsed.Renames[j].To)
	})

	parsed.Clean = len(parsed.Staged) == 0 && len(parsed.Unstaged) == 0 &&
		len(parsed.Untracked) == 0 && len(parsed.Conflicts) == 0
	parsed.GeneratedAt = time.Now().Unix()
	return parsed, nil
}

// parsePorcelainV2 解析 `--porcelain=v2 --branch -z` 输出。
// 纪律：未知记录类型只记 warning 并跳过，不猜测语义（§5.5）。
func parsePorcelainV2(out []byte) (*StatusResult, error) {
	result := &StatusResult{
		Staged:    []ChangeEntry{},
		Unstaged:  []ChangeEntry{},
		Untracked: []ChangeEntry{},
		Conflicts: []ChangeEntry{},
		Renames:   []RenameEntry{},
		Warnings:  []string{},
	}
	tokens := splitNul(out)
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		switch token[0] {
		case '#':
			parseBranchHeader(token, &result.Repo)
		case '1':
			fields := strings.SplitN(token, " ", 9)
			if len(fields) != 9 {
				result.Warnings = append(result.Warnings, warnRecord("1", token))
				continue
			}
			xy := fields[1]
			appendXY(result, xy, fields[8], "")
		case '2':
			fields := strings.SplitN(token, " ", 10)
			if len(fields) != 10 || i+1 >= len(tokens) {
				result.Warnings = append(result.Warnings, warnRecord("2", token))
				continue
			}
			xy := fields[1]
			newPath := fields[9]
			oldPath := tokens[i+1]
			i++
			appendXY(result, xy, newPath, oldPath)
			if status := renameStatus(xy); status != "" {
				result.Renames = append(result.Renames, RenameEntry{From: oldPath, To: newPath, Status: status})
			}
		case 'u':
			fields := strings.SplitN(token, " ", 11)
			if len(fields) != 11 {
				result.Warnings = append(result.Warnings, warnRecord("u", token))
				continue
			}
			result.Conflicts = append(result.Conflicts, ChangeEntry{Path: fields[10], Status: "!", XY: fields[1]})
		case '?':
			path := strings.TrimPrefix(token, "? ")
			if strings.TrimSpace(path) == "" {
				result.Warnings = append(result.Warnings, warnRecord("?", token))
				continue
			}
			result.Untracked = append(result.Untracked, ChangeEntry{Path: path, Status: "U", XY: "?"})
		default:
			// `!`（被忽略项，本请求未开启）与任何未知类型都只记 warning。
			result.Warnings = append(result.Warnings, warnRecord(string(token[0]), token))
		}
	}
	return result, nil
}

// appendXY 按 porcelain v2 的 XY 语义分组：X = 索引（已暂存），Y = 工作区（未暂存）。
func appendXY(result *StatusResult, xy, path, oldPath string) {
	if len(xy) != 2 || strings.TrimSpace(path) == "" {
		result.Warnings = append(result.Warnings, warnRecord("XY", xy+" "+path))
		return
	}
	if xy[0] != '.' {
		result.Staged = append(result.Staged, ChangeEntry{Path: path, Status: string(xy[0]), XY: xy, From: oldPath})
	}
	if xy[1] != '.' {
		result.Unstaged = append(result.Unstaged, ChangeEntry{Path: path, Status: string(xy[1]), XY: xy, From: oldPath})
	}
}

// renameStatus 从 XY 中取重命名/复制标记（R/C），无则返回空串。
func renameStatus(xy string) string {
	for _, ch := range []byte{xy[0], xy[1]} {
		if ch == 'R' || ch == 'C' {
			return string(ch)
		}
	}
	return ""
}

// warnRecord 生成「无法识别记录」的 warning 文本（截断原文，避免把长路径灌进响应）。
func warnRecord(kind, token string) string {
	if len(token) > 160 {
		token = token[:160] + "…"
	}
	return fmt.Sprintf("unrecognized porcelain v2 record (%s) skipped: %q", kind, token)
}

// parseBranchHeader 解析 `# branch.*` 头。
func parseBranchHeader(token string, repo *RepoStatus) {
	fields := strings.SplitN(token, " ", 3)
	if len(fields) < 3 {
		return
	}
	value := fields[2]
	switch fields[1] {
	case "branch.oid":
		if value != "(initial)" {
			repo.HeadFull = value
			repo.Head = shortSHA(value)
		}
	case "branch.head":
		if value == "(detached)" {
			repo.Detached = true
			return
		}
		repo.Branch = value
	case "branch.upstream":
		repo.Upstream = value
	case "branch.ab":
		parts := strings.Fields(value)
		if len(parts) != 2 {
			return
		}
		if ahead, err := strconv.Atoi(strings.TrimPrefix(parts[0], "+")); err == nil {
			repo.Ahead = ahead
		}
		if behind, err := strconv.Atoi(strings.TrimPrefix(parts[1], "-")); err == nil {
			repo.Behind = behind
		}
	}
}

// splitNul 按 NUL 切分 -z 输出并丢弃空段（路径可能含空格/换行，因此不能按行切分）。
func splitNul(out []byte) []string {
	parts := strings.Split(string(out), "\x00")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			tokens = append(tokens, part)
		}
	}
	return tokens
}

// shortSHA 取短 sha（7 位）。
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// fillCounts 把 numstat 的行数/二进制标记填进条目（numstat 的 `-\t-` 必须体现为 binary）。
func fillCounts(entry *ChangeEntry, counts map[string]numstatEntry) {
	value, ok := counts[entry.Path]
	if !ok {
		return
	}
	entry.Insertions = value.insertions
	entry.Deletions = value.deletions
	entry.Binary = value.binary
}

// toScopeEntries 把仓库相对路径批量转换为作用域相对路径，并统计被隐藏的行数。
func toScopeEntries(entries []ChangeEntry, gitCtx *gitContext, hidden int) ([]ChangeEntry, int) {
	converted := make([]ChangeEntry, 0, len(entries))
	for _, entry := range entries {
		rel, ok := scopeRelative(gitCtx.ScopeRoot, joinRepoPath(gitCtx.RepoRoot, entry.Path))
		if !ok {
			hidden++
			continue
		}
		entry.Path = rel
		if entry.From != "" {
			if fromRel, ok := scopeRelative(gitCtx.ScopeRoot, joinRepoPath(gitCtx.RepoRoot, entry.From)); ok {
				entry.From = fromRel
			} else {
				entry.From = ""
			}
		}
		converted = append(converted, entry)
	}
	return converted, hidden
}

// toScopeRenames 转换重命名分组，任一端越界则整条隐藏。
func toScopeRenames(entries []RenameEntry, gitCtx *gitContext, hidden int) ([]RenameEntry, int) {
	converted := make([]RenameEntry, 0, len(entries))
	for _, entry := range entries {
		from, fromOK := scopeRelative(gitCtx.ScopeRoot, joinRepoPath(gitCtx.RepoRoot, entry.From))
		to, toOK := scopeRelative(gitCtx.ScopeRoot, joinRepoPath(gitCtx.RepoRoot, entry.To))
		if !fromOK || !toOK {
			hidden++
			continue
		}
		entry.From = from
		entry.To = to
		converted = append(converted, entry)
	}
	return converted, hidden
}

// sortEntries 按路径稳定排序（大小写不敏感，同名不同大小写时再按原串比较）。
func sortEntries(entries []ChangeEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := strings.ToLower(entries[i].Path), strings.ToLower(entries[j].Path)
		if left == right {
			return entries[i].Path < entries[j].Path
		}
		return left < right
	})
}

// numstatEntry 是单个文件的行数统计；binary 来自 `-\t-`（不能当成 0/0）。
type numstatEntry struct {
	insertions int
	deletions  int
	binary     bool
}

// numstat 执行 `git diff [--cached] --numstat -z`，返回「仓库相对路径 → 行数」。
// 与 status 共用 StatusTimeout（三条命令同属一次状态查询）。
func (s *Service) numstat(ctx context.Context, gitCtx *gitContext, cached bool) (map[string]numstatEntry, error) {
	args := []string{"diff", "--numstat", "-z"}
	if cached {
		args = append(args, "--cached")
	}
	result, err := s.runner.git(ctx, gitCtx.RepoRoot, "git diff --numstat", s.limits.StatusTimeout, 0, args...)
	if err != nil {
		return nil, err
	}
	return parseNumstat(result.stdout), nil
}

// parseNumstat 解析 `--numstat -z` 输出。记录形态（-z 下路径是独立的 NUL 字段）：
//
//	<add>\t<del>\t<path>\0         普通条目
//	<add>\t<del>\t\0<old>\0<new>\0  重命名/复制（旧、新路径各占一个字段，顺序为旧→新）
func parseNumstat(out []byte) map[string]numstatEntry {
	fields := strings.Split(string(out), "\x00")
	entries := make(map[string]numstatEntry)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "" {
			continue
		}
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) < 3 {
			continue // 未知形态：跳过而不是猜测
		}
		entry, ok := numstatCounts(parts[0], parts[1])
		if !ok {
			continue
		}
		if parts[2] != "" {
			entries[parts[2]] = entry
			continue
		}
		// 重命名：紧随其后的两个字段是旧/新路径，两个键都登记（不依赖顺序猜测）。
		if i+2 < len(fields) {
			for _, path := range []string{fields[i+1], fields[i+2]} {
				if path != "" {
					entries[path] = entry
				}
			}
			i += 2
		}
	}
	return entries
}

// numstatCounts 解析 numstat 的两个计数列，`-` 表示二进制。
func numstatCounts(added, deleted string) (numstatEntry, bool) {
	if added == "-" || deleted == "-" {
		return numstatEntry{binary: true}, true
	}
	insertions, err := strconv.Atoi(added)
	if err != nil {
		return numstatEntry{}, false
	}
	deletions, err := strconv.Atoi(deleted)
	if err != nil {
		return numstatEntry{}, false
	}
	return numstatEntry{insertions: insertions, deletions: deletions}, true
}
