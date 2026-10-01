package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// IndexResult 汇总一次索引运行，供状态面与 shadow 指标消费。
type IndexResult struct {
	// Scanned 是遍历到的候选代码文件数。
	Scanned int `json:"scanned"`
	// Indexed 是本次真正解析并写入的文件数。
	Indexed int `json:"indexed"`
	// Skipped 是 content_hash 未变而跳过的文件数（增量索引的收益）。
	Skipped int `json:"skipped"`
	// Deleted 是本次对账中新标记为软删除的文件数（04 §5 Phase 1 交付 3）。
	Deleted int `json:"deleted"`
	// Errors 是读取/解析失败的文件数；这些文件被标记为 index_state=error。
	Errors int `json:"errors"`
	// Symbols / Refs 是本次写入的行数。
	Symbols int `json:"symbols"`
	Refs    int `json:"refs"`
	// Truncated 表示预算上限提前终止；工具面据此标记结果可能陈旧（ADR-0004）。
	Truncated bool          `json:"truncated"`
	Duration  time.Duration `json:"duration_ms"`
	// Phase 4：本次运行使用的 adapter 及其版本/降级信息（可观测性，不参与判定）。
	Adapter              string `json:"adapter,omitempty"`
	AdapterVersion       string `json:"adapter_version,omitempty"`
	AdapterDegraded      bool   `json:"adapter_degraded,omitempty"`
	AdapterDegradeReason string `json:"adapter_degrade_reason,omitempty"`
	// FullRebuild 表示本次因 adapter 版本变化而强制重解析全部文件。
	FullRebuild bool `json:"full_rebuild,omitempty"`
}

// ignoreDirs 是索引永不进入的目录（与 workspace scanner 的忽略集保持一致的语义）。
// .gitignore 是叠加其上的第二层（见 gitignore.go）：内置集在 .gitignore 缺失或
// 未声明依赖目录（node_modules 等）时兜底，且不被取反规则重新包含。
var ignoreDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "__pycache__": true, ".venv": true, "venv": true,
	".idea": true, ".vscode": true, "bin": true, "obj": true,
	".aicli": true, ".next": true, ".cache": true,
}

// codeExtensions 是内置 adapter 能识别的代码文件后缀。
var codeExtensions = map[string]string{
	".go": "go", ".ts": "typescript", ".tsx": "typescript", ".js": "javascript",
	".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".py": "python", ".rs": "rust", ".java": "java", ".c": "c", ".h": "c",
	".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "cpp",
	".cs": "csharp", ".rb": "ruby", ".php": "php", ".kt": "kotlin",
	".swift": "swift", ".scala": "scala", ".sh": "shell", ".sql": "sql",
}

// maxIndexFiles 是一次运行的文件数上限，防止超大仓库把一次运行拖成不可控的长任务。
const maxIndexFiles = 20000

// RunIndex 对 cfg.Workspace 执行一次增量索引。
//
// 流程（Phase 1 的 light 通道）：
//  1. 遍历工作区，收集候选代码文件；
//  2. 以 content_hash 判增量：哈希未变的文件整体跳过（不解析、不写库）；
//  3. 解析变更文件得到符号与引用候选；
//  4. 用"本次符号 + 库内既有符号"解析引用目标，落库。
//
// 解析器当前是 regex_builtin（confidence=heuristic）；tree-sitter / LSP 通道
// 属于后续阶段，接入时只替换 LanguageAdapter，本流程不变。
func RunIndex(ctx context.Context, store Store, cfg Config) (result IndexResult, err error) {
	started := time.Now()
	if store == nil {
		return result, fmt.Errorf("knowledge: index requires a store")
	}
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return result, err
	}

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: cfg.Workspace})
	if err != nil {
		return result, err
	}

	// 先写 index_jobs 再执行（ADR-0007 §4.3 / 04 §4.1）：状态面据此回答
	// "最近一次索引跑没跑完"；失败路径也必须留下终态，否则会永远显示 running。
	jobID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindLight})
	if err != nil {
		return result, err
	}
	defer func() {
		status := IndexJobStatusDone
		message := ""
		if err != nil {
			status, message = IndexJobStatusFailed, err.Error()
		}
		// 终态上报尽力而为且不依赖调用方 ctx：索引失败常伴随 ctx 取消，
		// 而"这次运行失败了"恰恰是最需要落库的事实。账本写失败不改变索引结论。
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = store.FinishIndexJob(finishCtx, jobID, status, result.Scanned,
			result.Indexed+result.Skipped+result.Errors, message)
	}()

	files, truncated, err := collectIndexableFiles(cfg)
	if err != nil {
		return result, err
	}
	result.Truncated = truncated
	result.Scanned = len(files)

	// Phase 4 交付 1/2：adapter 由配置选择；不可用的选择降级 builtin（可观测）。
	selection := SelectIndexAdapter(cfg)
	adapter := selection.Adapter
	result.Adapter = string(adapter.Name())
	result.AdapterVersion = adapter.Version()
	result.AdapterDegraded = selection.Degraded
	result.AdapterDegradeReason = selection.Reason

	// Phase 4 交付 4：adapter 版本与库内记录不一致 → 按配置全量重建。
	// 版本读取失败不阻断索引（Degrade-Not-Fail）：按"需要重建"处理。
	storedAdapterVersion, verErr := store.WorkspaceAdapterVersion(ctx, wsID)
	if verErr != nil {
		storedAdapterVersion = ""
	}
	fullRebuild := storedAdapterVersion != "" && storedAdapterVersion != adapter.Version() &&
		cfg.Index.FullRebuildOnAdapterChangeEnabled()
	result.FullRebuild = fullRebuild

	var pending []pendingFile

	for i, path := range files {
		if i > 0 && i%256 == 0 {
			// 中途进度：长索引（万级文件）期间状态面不应只看到 0/总数。
			_ = store.UpdateIndexJob(ctx, jobID, i)
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		pf, unchanged, recorded, err := stageFile(ctx, store, cfg, wsID, adapter, path, fullRebuild)
		switch {
		case err != nil:
			result.Errors++
			continue
		case unchanged:
			result.Skipped++
			continue
		case recorded:
			// 已按 index_state=error 显式登记（超限文件），不再解析。
			result.Errors++
			continue
		}
		pending = append(pending, pf)
	}

	// 库内既有符号：为跨文件的"旧文件 → 新文件"引用解析提供目标。
	// 排除本轮重写文件的旧符号——否则同名新旧并存，解析会因歧义失败（见 loadKnownSymbols）。
	known, err := loadKnownSymbols(ctx, store, pendingFileIDs(pending))
	if err != nil {
		return result, err
	}
	if err := writePendingFiles(ctx, store, wsID, adapter.Name(), known, pending, &result); err != nil {
		return result, err
	}

	// 删除对账：库内登记但磁盘已不存在的文件标记 deleted_at（交付 3）。
	// truncated 时绝不能执行——本轮没有走完工作区，"缺失"不等于"删除"。
	if !result.Truncated {
		deleted, err := reconcileDeletedFiles(ctx, store, wsID, cfg.Workspace, files)
		if err != nil {
			return result, err
		}
		result.Deleted = deleted
	}

	// adapter 版本落库：只有"完整且无错误"的运行才记账——否则状态面会宣称
	// 索引已按新 adapter 重建，而事实并非如此（下轮会再触发一次重建）。
	if !result.Truncated && result.Errors == 0 && storedAdapterVersion != adapter.Version() {
		if setErr := store.SetWorkspaceAdapterVersion(ctx, wsID, adapter.Version()); setErr != nil {
			result.AdapterDegraded = true
			result.AdapterDegradeReason = "记录 adapter 版本失败: " + setErr.Error()
		}
	}

	result.Duration = time.Since(started)
	return result, nil
}

// pendingFile 是一个文件本轮的解析结果（尚未落库）。
type pendingFile struct {
	rec     FileRecord
	symbols []Symbol
	refs    []pendingRef
}

// stageFile 对单个文件完成"登记检查 → 读盘 → 抽取"三步（RunIndex 与 IndexPaths 共用）。
//
// 返回语义：
//   - unchanged=true：content_hash 未变，本轮不写库（增量收益）；
//   - recorded=true：文件已被显式登记为 index_state=error（超限），不再解析；
//   - err != nil：文件级失败（读盘/抽取），已尽力把状态登记为 error。
func stageFile(ctx context.Context, store Store, cfg Config, wsID string, adapter LanguageAdapter, absPath string, forceRebuild bool) (pf pendingFile, unchanged, recorded bool, err error) {
	rec, decision, err := inspectFile(ctx, store, cfg, wsID, absPath, forceRebuild)
	if err != nil {
		return pendingFile{}, false, false, err
	}
	switch decision {
	case fileUnchanged:
		return pendingFile{}, true, false, nil
	case fileRecorded:
		return pendingFile{}, false, true, nil
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		_, _ = store.UpsertFile(ctx, rec.withState(IndexError))
		return pendingFile{}, false, false, err
	}
	extraction, err := adapter.Extract(ctx, rec, content)
	if err != nil {
		_, _ = store.UpsertFile(ctx, rec.withState(IndexError))
		return pendingFile{}, false, false, err
	}
	for i := range extraction.Symbols {
		extraction.Symbols[i].WorkspaceID = wsID
		extraction.Symbols[i].FileID = rec.ID
	}
	return pendingFile{rec: rec, symbols: extraction.Symbols, refs: extraction.Refs}, false, false, nil
}

// writePendingFiles 把解析结果落库（RunIndex 与 IndexPaths 共用）。
//
// 两阶段：先把本轮解析出的符号并入名字表，再做引用解析——同一轮内新增的跨文件
// 引用因此也能解析（旧文件里的引用则依赖库内既有符号表）。
func writePendingFiles(ctx context.Context, store Store, wsID string, adapterName RefSource, known map[string][]string, pending []pendingFile, result *IndexResult) error {
	for _, pf := range pending {
		for _, sym := range pf.symbols {
			known[sym.Name] = append(known[sym.Name], sym.ID)
		}
	}
	for _, pf := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := store.UpsertFile(ctx, pf.rec.withState(IndexLight)); err != nil {
			result.Errors++
			continue
		}
		if err := store.ReplaceSymbols(ctx, pf.rec.ID, pf.symbols); err != nil {
			result.Errors++
			continue
		}
		refs := resolveRefs(wsID, pf.rec.ID, pf.refs, known, adapterName)
		if err := store.ReplaceRefs(ctx, pf.rec.ID, refs); err != nil {
			result.Errors++
			continue
		}
		result.Indexed++
		result.Symbols += len(pf.symbols)
		result.Refs += len(refs)
	}
	return nil
}

// IndexPaths 对指定文件做**定向增量**索引（04 §5 Phase 5 交付 1/2 的执行端）。
//
// 与 RunIndex 的差别都是刻意的：
//   - 不遍历工作区、不做删除对账（除调用方显式给出的路径）、不写 adapter 版本、
//     没有 truncated 语义——调用方已经知道"哪些文件变了"；
//   - 路径可以是绝对路径或工作区相对路径；越界 / 后缀不可索引 / 被排除规则
//     （内置忽略目录、隐藏目录、.gitignore）命中 → 计入 Errors（不静默）；
//   - 磁盘上已不存在的路径按软删除处理（MarkFilesDeleted），与全量对账同语义；
//   - adapter 版本与库内不一致且开启 full_rebuild_on_adapter_change 时**整体跳过**：
//     定向增量只重解析少数文件，写下去会让库处于新旧 adapter 混装状态，
//     正确动作是等全量重建（结果里 FullRebuild=true + AdapterDegradeReason 说明）。
func IndexPaths(ctx context.Context, store Store, cfg Config, paths []string) (result IndexResult, err error) {
	started := time.Now()
	if store == nil {
		return result, fmt.Errorf("knowledge: index requires a store")
	}
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return result, err
	}

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: cfg.Workspace})
	if err != nil {
		return result, err
	}

	jobID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindIncremental})
	if err != nil {
		return result, err
	}
	defer func() {
		status := IndexJobStatusDone
		message := ""
		if err != nil {
			status, message = IndexJobStatusFailed, err.Error()
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = store.FinishIndexJob(finishCtx, jobID, status, result.Scanned,
			result.Indexed+result.Skipped+result.Errors, message)
	}()

	targets, rejected := resolveIndexTargets(cfg, paths)
	result.Scanned = len(targets)
	result.Errors = rejected

	selection := SelectIndexAdapter(cfg)
	adapter := selection.Adapter
	result.Adapter = string(adapter.Name())
	result.AdapterVersion = adapter.Version()
	result.AdapterDegraded = selection.Degraded
	result.AdapterDegradeReason = selection.Reason

	storedAdapterVersion, verErr := store.WorkspaceAdapterVersion(ctx, wsID)
	if verErr != nil {
		storedAdapterVersion = ""
	}
	if storedAdapterVersion != "" && storedAdapterVersion != adapter.Version() &&
		cfg.Index.FullRebuildOnAdapterChangeEnabled() {
		result.FullRebuild = true
		result.AdapterDegraded = true
		result.AdapterDegradeReason = "adapter 版本变化：定向增量整体跳过，等待全量重建"
		result.Skipped = len(targets)
		result.Duration = time.Since(started)
		return result, nil
	}

	var pending []pendingFile
	var missing []string
	for _, abs := range targets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		pf, unchanged, recorded, err := stageFile(ctx, store, cfg, wsID, adapter, abs, false)
		switch {
		case err != nil:
			if errors.Is(err, os.ErrNotExist) {
				// 文件在标记之后被删除：按软删除处理（与全量对账同语义）。
				if rel, relErr := filepath.Rel(cfg.Workspace, abs); relErr == nil {
					missing = append(missing, normalizeRelPath(rel))
				}
				continue
			}
			result.Errors++
			continue
		case unchanged:
			result.Skipped++
			continue
		case recorded:
			result.Errors++
			continue
		}
		pending = append(pending, pf)
	}

	known, err := loadKnownSymbols(ctx, store, pendingFileIDs(pending))
	if err != nil {
		return result, err
	}
	if len(missing) > 0 {
		deleted, markErr := store.MarkFilesDeleted(ctx, wsID, missing, time.Now())
		if markErr != nil {
			return result, markErr
		}
		result.Deleted = deleted
	}

	if err := writePendingFiles(ctx, store, wsID, adapter.Name(), known, pending, &result); err != nil {
		return result, err
	}
	result.Duration = time.Since(started)
	return result, nil
}

// resolveIndexTargets 把调用方给出的路径（绝对或工作区相对）归一为工作区内的
// 绝对路径，去重，并拒绝越界 / 不可索引（后缀不在 codeExtensions）/ 被索引
// 排除规则命中的路径。
// 拒绝数由调用方计入 Errors——静默丢弃会让"为什么这个文件没更新"无法回答。
func resolveIndexTargets(cfg Config, paths []string) (targets []string, rejected int) {
	root := cfg.Workspace
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			rejected++
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, filepath.FromSlash(p))
		}
		abs := filepath.Clean(p)
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			rejected++
			continue
		}
		if _, ok := codeExtensions[strings.ToLower(filepath.Ext(abs))]; !ok {
			rejected++
			continue
		}
		key := normalizeRelPath(rel)
		if indexPathExcluded(cfg, key) {
			rejected++
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, abs)
	}
	return targets, rejected
}

// indexPathExcluded 报告显式给定的工作区相对路径是否落在索引排除范围内：
// 祖先目录命中内置 ignoreDirs / 隐藏目录，或路径命中 .gitignore（可配置关闭）。
//
// 定向增量与全量索引必须同口径：否则编辑一个被忽略的文件会把"全量重建时
// 本应不存在"的行写进库，下一次全量对账又把它软删——同一路径两套结论。
func indexPathExcluded(cfg Config, rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 0; i < len(parts)-1; i++ {
		name := parts[i]
		if ignoreDirs[name] || strings.HasPrefix(name, ".") {
			return true
		}
	}
	return cfg.Index.UseGitignoreEnabled() && isIgnoredByGitignore(cfg.Workspace, rel, false)
}

// reconcileDeletedFiles 把"库内登记、磁盘已不存在"的文件标记为软删除（交付 3）。
//
// 只信本轮完整遍历的结果：调用方在 truncated（触及 maxIndexFiles 上限）时必须
// 跳过，否则未遍历到的文件会被误标为删除。文件重新出现时由 UpsertFile 复活
// （清空 deleted_at），符号随 ReplaceSymbols 重建。
func reconcileDeletedFiles(ctx context.Context, store Store, wsID, root string, seen []string) (int, error) {
	seenSet := make(map[string]struct{}, len(seen))
	for _, abs := range seen {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		seenSet[normalizeRelPath(rel)] = struct{}{}
	}
	active, err := store.ListActiveFiles(ctx, wsID)
	if err != nil {
		return 0, err
	}
	var missing []string
	for _, rec := range active {
		if _, ok := seenSet[rec.Path]; !ok {
			missing = append(missing, rec.Path)
		}
	}
	if len(missing) == 0 {
		return 0, nil
	}
	return store.MarkFilesDeleted(ctx, wsID, missing, time.Now())
}

// collectIndexableFiles 返回按路径排序的候选文件绝对路径列表。
//
// 过滤分三层（成本从低到高）：
//  1. 内置 ignoreDirs + 隐藏目录：整棵子树跳过，是缓存/依赖目录的主防线；
//  2. 目录内 .gitignore（按目录栈叠加；knowledge.index.use_gitignore 可关闭）：
//     项目自己声明什么不是源码（构建产物 / 生成文件 / 本地配置）；
//  3. codeExtensions：只收内置 adapter 能解析的后缀。
func collectIndexableFiles(cfg Config) ([]string, bool, error) {
	root := cfg.Workspace
	useGitignore := cfg.Index.UseGitignoreEnabled()
	var (
		files     []string
		truncated bool
		ignores   gitignoreSet
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 单个目录不可读不应中止整次索引。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel := relPathWithin(root, path)
		if useGitignore {
			// 深度优先：弹出不再覆盖当前路径的规则文件，保证只看祖先链。
			ignores.retainAncestors(rel)
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				if useGitignore {
					if f := loadGitignoreFile(root, ""); f != nil {
						ignores.push(*f)
					}
				}
				return nil
			}
			if ignoreDirs[name] || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			if useGitignore && ignores.isIgnored(rel, true) {
				return fs.SkipDir
			}
			if useGitignore {
				if f := loadGitignoreFile(path, rel); f != nil {
					ignores.push(*f)
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if _, ok := codeExtensions[strings.ToLower(filepath.Ext(name))]; !ok {
			return nil
		}
		if useGitignore && ignores.isIgnored(rel, false) {
			return nil
		}
		if len(files) >= maxIndexFiles {
			truncated = true
			return fs.SkipAll
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("knowledge: walk workspace: %w", err)
	}
	sort.Strings(files)
	return files, truncated, nil
}

// relPathWithin 返回 p 相对 root 的 '/' 分隔路径；root 自身返回 ""。
func relPathWithin(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return ""
	}
	rel = normalizeRelPath(rel)
	if rel == "." {
		return ""
	}
	return rel
}

// fileDecision 是 inspectFile 对单个文件的处置结论。
type fileDecision int

const (
	// fileProcess 表示需要读取并解析该文件。
	fileProcess fileDecision = iota
	// fileUnchanged 表示内容哈希未变，整体跳过。
	fileUnchanged
	// fileRecorded 表示文件已被显式登记为不可索引（超限），无需再解析。
	fileRecorded
)

// inspectFile 读取文件元数据并与库内记录比对，判断是否可以跳过解析。
//
// 只有 mtime_ns + size 都相同才信任既有 content_hash；否则读文件算哈希——
// 这是"廉价预筛 → 精确判据"的两级策略（04 §4.3 files.content_hash 注释）。
// forceRebuild=true 时跳过"未变化"短路：adapter 版本变化后必须重解析全部文件
// （Phase 4 交付 4 的 full_rebuild_on_adapter_change）。
func inspectFile(ctx context.Context, store Store, cfg Config, wsID, absPath string, forceRebuild bool) (FileRecord, fileDecision, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return FileRecord{}, fileProcess, err
	}
	rel, err := filepath.Rel(cfg.Workspace, absPath)
	if err != nil {
		return FileRecord{}, fileProcess, err
	}
	rel = normalizeRelPath(rel)
	lang := codeExtensions[strings.ToLower(filepath.Ext(absPath))]

	rec := FileRecord{
		ID:          FileID(wsID, rel),
		WorkspaceID: wsID,
		Path:        rel,
		Language:    lang,
		Size:        info.Size(),
		MTimeNS:     info.ModTime().UnixNano(),
		IsTest:      isTestPath(rel),
		IsGenerated: isGeneratedPath(rel),
		IndexState:  IndexLight,
		IndexedAt:   time.Now(),
	}
	if info.Size() > cfg.MaxFileBytes {
		// 超限文件不进解析：显式记为 error，让状态面能解释"为什么没有它的符号"。
		rec.IndexState = IndexError
		_, err := store.UpsertFile(ctx, rec)
		return rec, fileRecorded, err
	}

	existing, ok, err := store.FileByPath(ctx, wsID, rel)
	if err != nil {
		return rec, fileProcess, err
	}
	if ok && existing.DeletedAt == 0 &&
		existing.Size == rec.Size && existing.MTimeNS == rec.MTimeNS && existing.ContentHash != "" {
		rec.ContentHash = existing.ContentHash
		rec.IndexState = existing.IndexState
		if !forceRebuild && (rec.IndexState == IndexLight || rec.IndexState == IndexDeep) {
			return rec, fileUnchanged, nil
		}
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return rec, fileProcess, err
	}
	sum := sha256.Sum256(content)
	rec.ContentHash = hex.EncodeToString(sum[:])
	if ok && existing.DeletedAt == 0 && existing.ContentHash == rec.ContentHash &&
		!forceRebuild &&
		(existing.IndexState == IndexLight || existing.IndexState == IndexDeep) {
		// mtime 变了但内容没变（checkout / touch）：跳过解析，仅刷新元数据。
		return rec, fileUnchanged, nil
	}
	return rec, fileProcess, nil
}

// loadKnownSymbols 载入库内既有符号的名字 → id 映射，供跨文件引用解析。
//
// excludeFileIDs 排除"本轮正在重写的文件"的旧符号：不排除的话，同一个名字会
// 同时命中旧符号与新符号，resolveRefs 按"歧义即不绑定"处理，引用会静默丢绑定
// ——增量与全量因此不等价（Phase 5 交付 5 的等价性测试捕获的第一类缺陷，
// 全量重建对"文件改后仍定义同名符号"的自遮蔽同样中招）。
func loadKnownSymbols(ctx context.Context, store Store, excludeFileIDs map[string]struct{}) (map[string][]string, error) {
	syms, err := store.FindSymbols(ctx, SymbolQuery{Limit: maxKnownSymbols})
	if err != nil {
		return nil, err
	}
	known := make(map[string][]string, len(syms))
	for _, sym := range syms {
		if _, skip := excludeFileIDs[sym.FileID]; skip {
			continue
		}
		known[sym.Name] = append(known[sym.Name], sym.ID)
	}
	return known, nil
}

// pendingFileIDs 返回本轮待重写文件的 id 集合（loadKnownSymbols 的排除集）。
func pendingFileIDs(pending []pendingFile) map[string]struct{} {
	if len(pending) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(pending))
	for _, pf := range pending {
		out[pf.rec.ID] = struct{}{}
	}
	return out
}

// maxKnownSymbols 是引用解析名字表的规模上限，防止超大仓库把内存吃满。
const maxKnownSymbols = 200000

// resolveRefs 把引用候选解析成 refs 行：目标名唯一时才绑定 to_symbol_id，
// 歧义或未知名保持未解析（ToSymbolID 为空），避免把猜测写成事实。
func resolveRefs(wsID, fileID string, pending []pendingRef, known map[string][]string, source RefSource) []Reference {
	out := make([]Reference, 0, len(pending))
	for _, ref := range pending {
		row := Reference{
			WorkspaceID:  wsID,
			FileID:       fileID,
			FromSymbolID: ref.FromSymbolID,
			// 名字始终记录：目标未解析时它是唯一线索。
			ToSymbolName: ref.Name,
			Kind:         ref.Kind,
			Line:         ref.Line,
			Col:          ref.Col,
			Snippet:      ref.Snippet,
			Source:       source,
			Confidence:   ConfidenceFromSource(source).Score(),
		}
		if ids := known[ref.Name]; len(ids) == 1 {
			row.ToSymbolID = ids[0]
		}
		row.ID = RefID(wsID, fileID, row.Line, row.Col, row.Kind)
		out = append(out, row)
	}
	return out
}

// withState 返回替换了 index_state 的副本。
func (f FileRecord) withState(state IndexState) FileRecord {
	f.IndexState = state
	return f
}

// isTestPath 按路径判定测试文件（与 04 §4.3 files.is_test 的用途一致）。
func isTestPath(rel string) bool {
	lower := strings.ToLower(rel)
	base := strings.ToLower(filepath.Base(rel))
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.HasSuffix(base, "_test.py"),
		strings.HasSuffix(base, ".test.ts"), strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".test.js"), strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, ".spec.js"):
		return true
	case strings.Contains(lower, "/test/"), strings.Contains(lower, "/tests/"),
		strings.Contains(lower, "/__tests__/"):
		return true
	default:
		return false
	}
}

// isGeneratedPath 按路径/文件名判定生成代码。
func isGeneratedPath(rel string) bool {
	lower := strings.ToLower(rel)
	base := strings.ToLower(filepath.Base(rel))
	return strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, "_generated.go") ||
		strings.HasSuffix(base, ".gen.ts") || strings.HasSuffix(base, ".g.dart") ||
		strings.Contains(lower, "/generated/") || strings.Contains(lower, "/gen/")
}
