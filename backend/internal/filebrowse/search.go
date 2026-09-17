// 本文件实现 GET /fs/search：跨目录的**有界**模糊搜索（BFS 浅层优先 + 打分排序 + 游标分页）。
//
// 契约：docs/plan/composer-at-file-reference-workspace-search-plan.md
// §4.4（4.4.1 API / 4.4.2 扫描忽略 / 4.4.3 打分排序 / 4.4.4 游标 / 4.4.5 限额 / 4.4.6 安全）、
// §5-P1-1..P1-3、§6.1。
//
// 纪律（与 list.go 同口径）：
//   - scope/path 一律经 fsscope 解析与越界校验，接口层不接受裸绝对路径；
//   - 递归枚举不跟随符号链接目录；返回的 path 一律相对作用域根、分隔符统一 `/`；
//   - 错误统一用 *fsscope.Error 携带机器码，由 handler 映射 HTTP 状态与错误体；
//   - 三重上限兜底（深度 / 扫描量 / 时间预算）+ 单目录枚举上限，超限显式置 truncated；
//   - 只用标准库（不做索引、不做缓存、不做全局扫描锁）。
package filebrowse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// 搜索端点的默认值与硬上限（规划 §4.4.5）。
const (
	SearchLimitDefault = 20
	SearchLimitMax     = 50

	// 深度相对 base 计（base 自身 0，其直接子项 1）。
	SearchMaxDepthDefault = 8
	SearchMaxDepthMax     = 16

	// 扫描条目上限与 Limits.ScanMaxEntries 对齐。
	SearchMaxScanDefault = 20000
	SearchMaxScanMax     = 50000

	SearchBudgetDefaultMs = 250
	SearchBudgetMaxMs     = 1000

	// SearchShallowDepth 是空 q 首屏的设计性浅扫深度：只返回 depth ≤ 2 的条目，
	// 不做全量扫描（规划 §4.4.3）。它是**采样**而不是截断，因此不置 truncated。
	SearchShallowDepth = 2

	// SearchQueryMaxRunes 是 q 的最大长度（rune 计）；超长 400 query_too_long，不静默截断。
	SearchQueryMaxRunes = 256

	// searchDirEntriesMaxDefault 是单目录枚举上限：超过按名称序截断并置 dir_entries
	// （防单个巨量目录吃满预算，规划 §4.4.2）。
	searchDirEntriesMaxDefault = 2000

	searchCtxCheckEvery = 256
	// searchCursorVersion=2：游标位置扩展为与排序键一一对应的 score/path/depth/mtime/type；
	// v1 游标一律 400 cursor_invalid（前端按「重置到第一页」处理，见规划 §4.4.4）。
	searchCursorVersion  = 2
	searchCursorMaxBytes = 4096
)

// kinds 取值（未知值按 file 收口，规划 §4.4.1）。
const (
	SearchKindFile = "file"
	SearchKindDir  = "dir"
	SearchKindBoth = "both"
)

// truncated_reason 归因枚举（规划 §4.4.1）；未截断时字段整体省略。
const (
	SearchTruncatedDepth      = "depth"
	SearchTruncatedScan       = "scan"
	SearchTruncatedBudget     = "budget"
	SearchTruncatedDirEntries = "dir_entries"
)

// SearchRequest 是 GET /fs/search 的入参。
//
// Query 按**字面**匹配：不解释正则、通配符或路径 glob（规划 §4.4.1）。
type SearchRequest struct {
	Scope      string
	Query      string
	Path       string
	Cursor     string
	Limit      int
	ShowHidden bool
	Kinds      string
	MaxDepth   int
	MaxScan    int
	BudgetMs   int
}

// SearchMatch 描述命中位置，供前端高亮。
//
// Start 含、End 不含，单位是**原始字符串的 rune 偏移**（不是字节偏移），
// 与 items[].name / items[].path 的显示文本对应。
type SearchMatch struct {
	Field string `json:"field"` // name | path
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// SearchItem 是单个命中项；Path 相对作用域根，分隔符统一 `/`。
type SearchItem struct {
	Name  string       `json:"name"`
	Path  string       `json:"path"`
	Type  string       `json:"type"` // file | dir
	Size  int64        `json:"size"` // 目录为 -1（与 fs/list 同口径）
	Mtime int64        `json:"mtime"`
	Ext   string       `json:"ext,omitempty"`
	Score int          `json:"score"` // 空 q 时为 0（不做打分）
	Match *SearchMatch `json:"match,omitempty"`
}

// SearchResult 是 GET /fs/search 的响应体（规划 §4.4.1）。
//
// Truncated 与 HasMore 正交：Truncated 表示「本次结果可能不完备」（深度/扫描量/
// 预算/单目录枚举上限），HasMore 表示「按当前排序还有下一页」。
type SearchResult struct {
	Scope           string       `json:"scope"`
	Query           string       `json:"query"`
	Base            string       `json:"base"`
	Items           []SearchItem `json:"items"` // 永不为 null
	NextCursor      string       `json:"next_cursor,omitempty"`
	HasMore         bool         `json:"has_more"`
	Scanned         int          `json:"scanned"`
	Truncated       bool         `json:"truncated"`
	TruncatedReason []string     `json:"truncated_reason,omitempty"`
	ElapsedMs       int64        `json:"elapsed_ms"`
	Limit           int          `json:"limit"`
}

// searchCandidate 是扫描期候选：item 面向响应，depth 相对 base（base 的直接子项为 1）。
type searchCandidate struct {
	item  SearchItem
	depth int
}

// Search 在作用域内做 BFS 有界搜索（规划 §4.4）。
//
// 流程：参数校验与夹紧 → scope/path 解析（fsscope）→ 游标校验 → BFS 扫描
// （忽略名单/隐藏规则/符号链接/深度/扫描量/预算）→ 打分排序 → 游标分页切片。
func (s *Service) Search(ctx context.Context, req SearchRequest) (*SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscopeReadError("search", err)
	}
	if utf8.RuneCountInString(req.Query) > SearchQueryMaxRunes {
		return nil, fsscope.NewErrorf(CodeQueryTooLong, 400, "q must be at most %d characters", SearchQueryMaxRunes)
	}
	target, ferr := s.resolver.ResolvePath(ctx, req.Scope, req.Path)
	if ferr != nil {
		return nil, ferr
	}
	if ferr := s.requireDir(target); ferr != nil {
		return nil, ferr
	}

	limit := clampSearchParam(req.Limit, SearchLimitDefault, SearchLimitMax)
	maxDepth := clampSearchParam(req.MaxDepth, SearchMaxDepthDefault, SearchMaxDepthMax)
	maxScanCap := SearchMaxScanMax
	if s.limits.ScanMaxEntries > 0 && s.limits.ScanMaxEntries < maxScanCap {
		maxScanCap = s.limits.ScanMaxEntries
	}
	maxScan := clampSearchParam(req.MaxScan, SearchMaxScanDefault, maxScanCap)
	budgetMs := clampSearchParam(req.BudgetMs, SearchBudgetDefaultMs, SearchBudgetMaxMs)

	last, cursorErr := decodeSearchCursor(req.Cursor, target.Root.Scope.Raw, req.Query, target.Rel)
	if cursorErr != nil {
		return nil, cursorErr
	}

	// 空 q 首屏只做浅扫采样；达到用户显式 max_depth 才算 depth 截断。
	depthLimit := maxDepth
	depthIsHard := true
	emptyQuery := req.Query == ""
	if emptyQuery && depthLimit > SearchShallowDepth {
		depthLimit = SearchShallowDepth
		depthIsHard = false
	}

	started := s.currentTime()
	state := &searchScanState{
		service:       s,
		depthLimit:    depthLimit,
		depthIsHard:   depthIsHard,
		maxScan:       maxScan,
		dirEntriesMax: s.searchDirEntriesMax(),
		kinds:         normalizeSearchKinds(req.Kinds),
		showHidden:    req.ShowHidden,
		queryRunes:    lowerRunes([]rune(req.Query)),
	}
	if emptyQuery {
		state.collectCap = limit * 4 // 空 q：收集满 limit×4 即停，不触发全量扫描
	}
	deadline := started.Add(time.Duration(budgetMs) * time.Millisecond)
	if ferr := state.run(ctx, target, deadline); ferr != nil {
		return nil, ferr
	}

	sortSearchCandidates(state.candidates, emptyQuery)
	candidates := state.candidates
	if last != nil {
		// 翻页 = 重扫 + 跳过 last 之前的已返回项（不承诺快照一致性，规划 §4.4.4）。
		candidates = afterSearchCursor(candidates, *last, emptyQuery)
	}

	result := &SearchResult{
		Scope:           target.Root.Scope.Raw,
		Query:           req.Query,
		Base:            target.Rel,
		Items:           make([]SearchItem, 0, limit),
		Scanned:         state.scanned,
		Truncated:       len(state.reasons) > 0,
		TruncatedReason: state.reasons,
		Limit:           limit,
	}
	if len(candidates) > limit { // 多读一项判定 has_more，天然避免 len==limit 的歧义
		candidates = candidates[:limit]
		last := candidates[len(candidates)-1]
		if cursor := encodeSearchCursor(target.Root.Scope.Raw, req.Query, target.Rel, last); cursor != "" {
			result.HasMore = true
			result.NextCursor = cursor
		}
		// 编码失败（理论不可达）时不置 has_more：对外不产生「有下一页但没有游标」的矛盾组合。
	}
	for _, candidate := range candidates {
		result.Items = append(result.Items, candidate.item)
	}
	elapsed := s.currentTime().Sub(started).Milliseconds()
	if elapsed < 0 { // 测试注入的时钟可能回拨；对外只报非负值
		elapsed = 0
	}
	result.ElapsedMs = elapsed
	return result, nil
}

// currentTime 读取 Service 的时钟（NewService 注入 time.Now；测试可替换）。
func (s *Service) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// searchDirEntriesMax 返回单目录枚举上限（Limits 未配置时用默认 2000）。
func (s *Service) searchDirEntriesMax() int {
	if s != nil && s.limits.SearchDirEntriesMax > 0 {
		return s.limits.SearchDirEntriesMax
	}
	return searchDirEntriesMaxDefault
}

// clampSearchParam：非正数用默认值，然后夹紧到 [1, upper]（规划 §4.4.5 的「服务端夹紧」）。
func clampSearchParam(value, fallback, upper int) int {
	if value <= 0 {
		value = fallback
	}
	if value < 1 {
		value = 1
	}
	if upper > 0 && value > upper {
		value = upper
	}
	return value
}

// normalizeSearchKinds 归一化 kinds；未知值按 file 收口（规划 §4.4.1）。
func normalizeSearchKinds(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SearchKindDir:
		return SearchKindDir
	case SearchKindBoth:
		return SearchKindBoth
	default:
		return SearchKindFile
	}
}

// searchIgnoredDirs 是固定忽略目录（大小写不敏感，不返回也不进入；规划 §4.4.2）。
// 后续可改为配置或读 .gitignore（§5-P2），本期保持固定名单。
var searchIgnoredDirs = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {},
	"node_modules": {}, "vendor": {}, "dist": {}, "build": {}, "out": {}, "target": {},
	".venv": {}, "venv": {}, "__pycache__": {}, ".next": {}, ".turbo": {}, ".cache": {},
	"coverage": {}, ".aicli-uploads": {},
}

// searchIgnoredDir 判定目录名是否在固定忽略名单里（大小写不敏感）。
func searchIgnoredDir(name string) bool {
	_, ok := searchIgnoredDirs[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// dirHasEntries 只读目录的首批条目判断目录是否非空（轻量探针，用于 depth 归因去假阳性）。
// 空目录（ReadDir(1) 返回 io.EOF 且无条目）返回 false；打不开/读失败时返回 true：
// 无法证明为空，按保守方向计入截断。
func dirHasEntries(abs string) bool {
	dir, err := os.Open(abs)
	if err != nil {
		return true
	}
	defer dir.Close()
	entries, err := dir.ReadDir(1)
	if len(entries) > 0 {
		return true
	}
	if err == nil || errors.Is(err, io.EOF) {
		return false
	}
	return true
}

// searchScanState 是单次搜索的扫描状态（无全局状态：多标签搜索互不影响）。
type searchScanState struct {
	service       *Service
	depthLimit    int    // 相对 base 的最大深度（含）
	depthIsHard   bool   // false 表示 depthLimit 是空 q 的设计性浅扫，不归因为 depth 截断
	maxScan       int    // 扫描条目上限
	dirEntriesMax int    // 单目录枚举上限
	kinds         string // file | dir | both
	showHidden    bool
	queryRunes    []rune // 小写化后的 q（rune 1:1 对应原串，供高亮偏移）
	collectCap    int    // >0 时空 q 的收集上限（limit×4）

	scanned    int
	ctxCounter int
	candidates []searchCandidate
	reasons    []string // truncated_reason（去重，发现顺序）
	stop       bool
}

// run 执行 BFS 扫描；ctx 取消返回错误（前端 abort 后尽快退出），
// 预算/扫描量/深度/单目录上限只收尾并记录归因，不报错（规划 §4.4.2）。
//
// base 本身读失败是「搜索无法开始」，按 /fs/list 同一口径报错（含 fs_search_failed）；
// 更深层的目录读失败只跳过该目录，保证部分结果可用。
func (st *searchScanState) run(ctx context.Context, base *fsscope.Target, deadline time.Time) *fsscope.Error {
	type pendingDir struct {
		abs   string // 绝对路径（来自 fsscope 解析，未跟随符号链接）
		rel   string // 相对作用域根
		depth int    // 相对 base
	}
	baseAbs, baseRel := base.Abs, base.Rel
	queue := []pendingDir{{abs: baseAbs, rel: baseRel, depth: 0}}
	for len(queue) > 0 && !st.stop {
		current := queue[0]
		queue = queue[1:]
		entries, err := os.ReadDir(current.abs)
		if err != nil {
			if current.depth == 0 {
				return searchReadDirError(err, base)
			}
			// 单个目录读失败（权限、竞态删除）：跳过该目录，不让整次搜索失败。
			continue
		}
		if len(entries) > st.dirEntriesMax {
			dropped := entries[st.dirEntriesMax:]
			entries = entries[:st.dirEntriesMax] // os.ReadDir 已按名称序排序
			// 仅当被丢弃的条目里存在按名称即可判定可见的条目时才归因，避免整目录都是
			// 隐藏/忽略项时把「没有遗漏」误报成 dir_entries 截断。
			if st.anyPossiblyVisibleDropped(dropped) {
				st.markTruncated(SearchTruncatedDirEntries)
			}
		}
		for _, entry := range entries {
			if st.stop {
				break
			}
			if st.scanned >= st.maxScan {
				st.markTruncated(SearchTruncatedScan)
				st.stop = true
				break
			}
			st.scanned++
			if ferr := st.checkContext(ctx); ferr != nil {
				return ferr
			}
			if st.budgetExceeded(deadline) {
				st.markTruncated(SearchTruncatedBudget)
				st.stop = true
				break
			}

			name := entry.Name()
			if isInternalName(name) {
				continue // .aicli-uploads：内部项始终隐藏，即使 show_hidden=true
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue // 单项 stat 失败：跳过，不返回 inaccessible（搜索只返回可用条目）
			}
			hidden := IsHiddenName(name)
			if !hidden && fileAttributesHidden(info) {
				hidden = true
			}
			if hidden && !st.showHidden {
				// 隐藏项跳过；隐藏目录因此也不会被下钻（父目录不可见则子文件不可见）。
				continue
			}

			entryDepth := current.depth + 1
			isSymlink := entry.Type()&os.ModeSymlink != 0
			if info.IsDir() {
				if searchIgnoredDir(name) {
					continue // 固定忽略名单：不返回、不进入
				}
				if entryDepth < st.depthLimit {
					queue = append(queue, pendingDir{
						abs:   filepath.Join(current.abs, name),
						rel:   listPath(current.rel, name),
						depth: entryDepth,
					})
				} else if st.depthIsHard && !st.hasTruncated(SearchTruncatedDepth) {
					// 该目录的子项会超过 max_depth：只有目录里确实有内容才归因 depth
					// （边界层的空目录不会遗漏条目，标了反而是假阳性）。
					if dirHasEntries(filepath.Join(current.abs, name)) {
						st.markTruncated(SearchTruncatedDepth)
					}
				}
				if st.collects(SearchKindDir) {
					st.append(searchCandidate{
						item:  SearchItem{Name: name, Path: listPath(current.rel, name), Type: SearchKindDir, Size: -1, Mtime: info.ModTime().Unix()},
						depth: entryDepth,
					})
				}
				continue
			}
			if isSymlink {
				// 符号链接目录一律不跟随（避免环、避免越过 fsscope 语义边界），也不作为目录结果返回；
				// 符号链接文件按解析结果返回（与 list.go 同口径）。
				resolved, statErr := os.Stat(filepath.Join(current.abs, name))
				if statErr != nil || resolved.IsDir() || !resolved.Mode().IsRegular() {
					continue
				}
				if st.collects(SearchKindFile) {
					st.append(searchCandidate{
						item: SearchItem{
							Name: name, Path: listPath(current.rel, name), Type: SearchKindFile,
							Size: resolved.Size(), Mtime: resolved.ModTime().Unix(),
							Ext: strings.ToLower(filepath.Ext(name)),
						},
						depth: entryDepth,
					})
				}
				continue
			}
			if !info.Mode().IsRegular() {
				continue // 设备文件/FIFO/socket 等不参与搜索
			}
			if st.collects(SearchKindFile) {
				st.append(searchCandidate{
					item: SearchItem{
						Name: name, Path: listPath(current.rel, name), Type: SearchKindFile,
						Size: info.Size(), Mtime: info.ModTime().Unix(),
						Ext: strings.ToLower(filepath.Ext(name)),
					},
					depth: entryDepth,
				})
			}
		}
	}
	return nil
}

// collects 判断当前 kinds 是否收集该类型。
func (st *searchScanState) collects(kind string) bool {
	return st.kinds == SearchKindBoth || st.kinds == kind
}

// append 收集候选：非空 q 时先打分，无命中直接丢弃（不做「猜」，规划 §4.4.3）。
func (st *searchScanState) append(candidate searchCandidate) {
	if len(st.queryRunes) > 0 {
		score, match := scoreSearchCandidate(candidate.item, st.queryRunes)
		if match == nil || score <= 0 {
			return
		}
		candidate.item.Score = score
		candidate.item.Match = match
	}
	st.candidates = append(st.candidates, candidate)
	if st.collectCap > 0 && len(st.candidates) >= st.collectCap {
		st.stop = true // 空 q 首屏：收集满 limit×4 即停（设计性采样，不置 truncated）
	}
}

// markTruncated 记录截断归因（去重；枚举仅 depth|scan|budget|dir_entries）。
func (st *searchScanState) markTruncated(reason string) {
	if st.hasTruncated(reason) {
		return
	}
	st.reasons = append(st.reasons, reason)
}

// hasTruncated 判断某归因是否已记录（也用于避免重复的探测开销）。
func (st *searchScanState) hasTruncated(reason string) bool {
	for _, existing := range st.reasons {
		if existing == reason {
			return true
		}
	}
	return false
}

// anyPossiblyVisibleDropped 判断被 dirEntriesMax 丢弃的条目里是否存在「按名称即可判定可见」
// 的条目：内部项与（show_hidden=false 时的）隐藏项不算，忽略名单里的目录不算。
// 属性隐藏与符号链接目标类型需要额外 stat，这里按保守方向计入可见。
func (st *searchScanState) anyPossiblyVisibleDropped(entries []os.DirEntry) bool {
	for _, entry := range entries {
		name := entry.Name()
		if isInternalName(name) {
			continue
		}
		if !st.showHidden && IsHiddenName(name) {
			continue
		}
		if entry.IsDir() && searchIgnoredDir(name) {
			continue
		}
		return true
	}
	return false
}

// checkContext 每 256 个条目检查一次 ctx.Err()（规划 §4.4.2）。
func (st *searchScanState) checkContext(ctx context.Context) *fsscope.Error {
	st.ctxCounter++
	if st.ctxCounter < searchCtxCheckEvery {
		return nil
	}
	st.ctxCounter = 0
	if err := ctx.Err(); err != nil {
		return fsscopeReadError("search", err)
	}
	return nil
}

// budgetExceeded：budget_ms 自枚举开始计时（不含路径校验，规划 §4.4.2）。
func (st *searchScanState) budgetExceeded(deadline time.Time) bool {
	if deadline.IsZero() {
		return false
	}
	return st.now().After(deadline)
}

// now 读取 Service 时钟（测试可注入假时钟让预算截断可复现）。
func (st *searchScanState) now() time.Time {
	if st.service != nil && st.service.now != nil {
		return st.service.now()
	}
	return time.Now()
}

// searchReadDirError 映射 base 目录读取失败：404/403 复用既有 path 码，
// 其余（IO 错误等）用 fs_search_failed(500)，命名对齐 list.go 的 readDirError/fs_list_failed。
func searchReadDirError(err error, target *fsscope.Target) *fsscope.Error {
	switch {
	case os.IsNotExist(err):
		return fsscope.NewErrorf(fsscope.CodePathNotFound, 404, "directory does not exist: %s", displayPath(target))
	case os.IsPermission(err):
		return fsscope.NewErrorf(fsscope.CodePathPermission, 403, "permission denied: %s", displayPath(target))
	default:
		return fsscope.NewErrorf(CodeSearchFailed, 500, "search %s failed: %v", displayPath(target), err)
	}
}

// sortSearchCandidates 固化排序键（规划 §4.4.3）：
//   - 非空 q：score desc → 文件优先 → path asc；
//   - 空 q：(depth asc, mtime desc, path asc)，浅层优先。
func sortSearchCandidates(candidates []searchCandidate, emptyQuery bool) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if emptyQuery {
			if a.depth != b.depth {
				return a.depth < b.depth
			}
			if a.item.Mtime != b.item.Mtime {
				return a.item.Mtime > b.item.Mtime
			}
			return compareSearchPath(a.item.Path, b.item.Path) < 0
		}
		if a.item.Score != b.item.Score {
			return a.item.Score > b.item.Score
		}
		aFile, bFile := a.item.Type != SearchKindDir, b.item.Type != SearchKindDir
		if aFile != bFile {
			return aFile // 文件优先
		}
		return compareSearchPath(a.item.Path, b.item.Path) < 0
	})
}

// compareSearchPath 大小写不敏感的路径升序（同序时用原串兜底，保证全序）。
func compareSearchPath(a, b string) int {
	lowerA, lowerB := strings.ToLower(a), strings.ToLower(b)
	switch {
	case lowerA < lowerB:
		return -1
	case lowerA > lowerB:
		return 1
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// afterSearchCursor 跳过 last 及其之前的已返回项。比较键与 sortSearchCandidates 完全一致，
// 因此游标项即使因树变化消失（不承诺快照一致性，规划 §4.4.4），也能按同一全序定位续页起点，
// 不会把「排在 last 之后、但次要键更小」的条目误判成已返回而漏掉。
func afterSearchCursor(candidates []searchCandidate, last searchCursorPos, emptyQuery bool) []searchCandidate {
	for index, candidate := range candidates {
		if searchCandidateAfterCursor(candidate, last, emptyQuery) {
			return candidates[index:]
		}
	}
	return nil
}

// searchCandidateAfterCursor 判断候选是否严格排在游标位置之后（与 sortSearchCandidates 同一比较键）。
func searchCandidateAfterCursor(candidate searchCandidate, last searchCursorPos, emptyQuery bool) bool {
	if emptyQuery {
		if candidate.depth != last.Depth {
			return candidate.depth > last.Depth
		}
		if candidate.item.Mtime != last.Mtime {
			return candidate.item.Mtime < last.Mtime // mtime 降序：更旧者排后
		}
		return compareSearchPath(candidate.item.Path, last.Path) > 0
	}
	if candidate.item.Score != last.Score {
		return candidate.item.Score < last.Score // score 降序：更低者排后
	}
	candidateIsFile := candidate.item.Type != SearchKindDir
	lastIsFile := last.Type != SearchKindDir
	if candidateIsFile != lastIsFile {
		return lastIsFile // 文件优先：last 是文件、候选是目录时，候选排在后面
	}
	return compareSearchPath(candidate.item.Path, last.Path) > 0
}

// searchCursor 是不透明游标内容（规划 §4.4.4）：base64url(JSON) 的版本化复合键，
// 与当前请求的 scope/q/base 不匹配（换查询、换作用域、篡改）时返回 400 cursor_invalid。
type searchCursor struct {
	Version int             `json:"v"`
	Scope   string          `json:"scope"`
	Query   string          `json:"q"`
	Base    string          `json:"base"`
	Last    searchCursorPos `json:"last"`
}

// searchCursorPos 是上一页最后一项的排序位置：字段与 sortSearchCandidates 的比较键一一对应
// （非空 q：score/type/path；空 q：depth/mtime/path），游标项因树变化消失时仍能精确定位续页。
type searchCursorPos struct {
	Score int    `json:"score"`
	Path  string `json:"path"`
	Depth int    `json:"depth"`
	Mtime int64  `json:"mtime"`
	Type  string `json:"type"` // file | dir
}

// encodeSearchCursor 编码不透明游标；失败返回空串（调用方不置 has_more，不产出矛盾组合）。
func encodeSearchCursor(scope, query, base string, last searchCandidate) string {
	payload, err := json.Marshal(searchCursor{
		Version: searchCursorVersion,
		Scope:   scope,
		Query:   query,
		Base:    base,
		Last: searchCursorPos{
			Score: last.item.Score, Path: last.item.Path,
			Depth: last.depth, Mtime: last.item.Mtime, Type: last.item.Type,
		},
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// decodeSearchCursor 解析游标；失败一律 400 cursor_invalid（不静默回退第一页，避免前端死循环）。
func decodeSearchCursor(raw, scope, query, base string) (*searchCursorPos, *fsscope.Error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	invalid := func(message string) *fsscope.Error {
		return fsscope.NewError(CodeCursorInvalid, 400, message)
	}
	if len(raw) > searchCursorMaxBytes {
		return nil, invalid("cursor is too long")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, invalid("cursor is not valid base64url")
	}
	var cursor searchCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, invalid("cursor payload is not valid JSON")
	}
	if cursor.Version != searchCursorVersion {
		return nil, invalid("unsupported cursor version")
	}
	if cursor.Scope != scope || cursor.Query != query || cursor.Base != base {
		return nil, invalid("cursor does not match the current search")
	}
	if cursor.Last.Score < 0 {
		return nil, invalid("cursor score is invalid")
	}
	if cursor.Last.Depth < 0 {
		return nil, invalid("cursor depth is invalid")
	}
	if cursor.Last.Type != SearchKindFile && cursor.Last.Type != SearchKindDir {
		return nil, invalid("cursor item type is invalid")
	}
	if !validCursorPath(cursor.Last.Path) {
		return nil, invalid("cursor path is invalid")
	}
	return &cursor.Last, nil
}

// validCursorPath 校验游标里的 last.path 是干净的相对路径（防篡改后的越界/绝对路径回灌）。
func validCursorPath(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	rel, ferr := fsscope.NormalizeRelPath(value)
	if ferr != nil || rel == "" {
		return false
	}
	return rel == value // 归一化前后必须一致：不允许编码绕过/`.`/`..` 改写
}

// scoreSearchCandidate 按规划 §4.4.3 打分；match == nil 表示不命中（丢弃）。
//
// 大小写不敏感用逐 rune 的 unicode.ToLower 实现（不是 strings.ToLower），以保证
// match 的 rune 偏移与原始字符串一一对应（前端按 rune 切片高亮）。
//
// 已知限制（规划 §7-R9）：不做 Unicode NFC/NFD 归一化——标准库不提供该能力，
// 且本包纪律不允许引入 x/text；macOS 等 NFD 文件名可能与 NFC 查询串不匹配。
func scoreSearchCandidate(item SearchItem, query []rune) (int, *SearchMatch) {
	if len(query) == 0 {
		return 0, nil
	}
	nameRunes := []rune(item.Name)
	nameLower := lowerRunes(nameRunes)
	pathRunes := []rune(item.Path)
	pathLower := lowerRunes(pathRunes)

	// 1) 文件名完整相等。
	if equalRunes(nameLower, query) {
		return 1000, &SearchMatch{Field: "name", Start: 0, End: len(nameRunes)}
	}
	// 2) 文件名前缀：900 − 位置惩罚（前缀位置恒为 0）。
	if hasRunePrefix(nameLower, query) {
		return 900, &SearchMatch{Field: "name", Start: 0, End: len(query)}
	}
	// 3) 文件名词边界前缀（camelCase / `-` `_` `.` 分段）。
	if index := boundaryIndex(nameRunes, nameLower, query); index >= 0 {
		return 850, &SearchMatch{Field: "name", Start: index, End: index + len(query)}
	}
	// 4) 文件名子串：700 − 位置惩罚（封顶 100，避免长名字把子串压到路径命中之下）。
	if index := indexRunes(nameLower, query); index >= 0 {
		return 700 - minSearchInt(index, 100), &SearchMatch{Field: "name", Start: index, End: index + len(query)}
	}
	// 5) 路径段前缀/段相等（中间目录名命中，不含文件名自身）。
	if start, ok := pathSegmentMatch(pathLower, query); ok {
		return 600, &SearchMatch{Field: "path", Start: start, End: start + len(query)}
	}
	// 6) 路径子串。
	if index := indexRunes(pathLower, query); index >= 0 {
		return 500, &SearchMatch{Field: "path", Start: index, End: index + len(query)}
	}
	// 7) 文件名子序列（fzf 式）：300 × 覆盖率 − 间隔惩罚。
	if first, last, ok := subsequenceRunes(nameLower, query); ok {
		return subsequenceScore(300, len(query), len(nameLower), first, last),
			&SearchMatch{Field: "name", Start: first, End: last + 1}
	}
	// 8) 路径子序列：200 × 覆盖率 − 间隔惩罚。
	if first, last, ok := subsequenceRunes(pathLower, query); ok {
		return subsequenceScore(200, len(query), len(pathLower), first, last),
			&SearchMatch{Field: "path", Start: first, End: last + 1}
	}
	return 0, nil
}

// subsequenceScore 计算子序列得分；覆盖率 = len(q)/len(haystack)，间隔惩罚 = 跨度 − len(q)。
// 结果至少为 1，保证命中项与「丢弃」区分开。
func subsequenceScore(weight, queryLen, haystackLen, first, last int) int {
	if haystackLen <= 0 {
		return 1
	}
	gap := last - first + 1 - queryLen
	score := weight*queryLen/haystackLen - gap
	if score < 1 {
		return 1
	}
	return score
}

// pathSegmentMatch 在路径的目录段里找「段相等 / 段前缀」命中，返回命中起点（完整路径的 rune 偏移）。
// 最后一段是条目名本身，已由文件名规则覆盖，这里不重复计分。
func pathSegmentMatch(pathLower, query []rune) (int, bool) {
	if len(pathLower) == 0 || len(query) == 0 {
		return 0, false
	}
	start := 0
	for index := 0; index <= len(pathLower); index++ {
		if index != len(pathLower) && pathLower[index] != '/' {
			continue
		}
		if index != len(pathLower) { // 中间段（目录）
			segment := pathLower[start:index]
			if len(segment) > 0 && (equalRunes(segment, query) || hasRunePrefix(segment, query)) {
				return start, true
			}
		}
		start = index + 1
	}
	return 0, false
}

// lowerRunes 逐 rune 小写化，保持索引 1:1（与 strings.ToLower 不同：不改变 rune 数量）。
func lowerRunes(input []rune) []rune {
	output := make([]rune, len(input))
	for index, char := range input {
		output[index] = unicode.ToLower(char)
	}
	return output
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func hasRunePrefix(haystack, prefix []rune) bool {
	if len(prefix) > len(haystack) {
		return false
	}
	for index := range prefix {
		if haystack[index] != prefix[index] {
			return false
		}
	}
	return true
}

func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if hasRunePrefix(haystack[index:], needle) {
			return index
		}
	}
	return -1
}

// boundaryIndex 找「词边界前缀」命中：index 处是词边界且从这里开始等于 q。
func boundaryIndex(original, lowered, query []rune) int {
	for index := 0; index+len(query) <= len(lowered); index++ {
		if !isWordBoundary(original, index) {
			continue
		}
		if hasRunePrefix(lowered[index:], query) {
			return index
		}
	}
	return -1
}

// isWordBoundary 判定 rune 下标是否为词边界：串首、`/` `-` `_` `.` 之后、camelCase 小写→大写。
func isWordBoundary(runes []rune, index int) bool {
	if index <= 0 {
		return true
	}
	if index >= len(runes) {
		return false
	}
	switch runes[index-1] {
	case '/', '-', '_', '.':
		return true
	}
	return unicode.IsLower(runes[index-1]) && unicode.IsUpper(runes[index])
}

// subsequenceRunes 双指针子序列匹配，返回首/末命中下标（均为闭区间）。
func subsequenceRunes(haystack, needle []rune) (int, int, bool) {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return 0, 0, false
	}
	first, last := -1, -1
	cursor := 0
	for index, char := range haystack {
		if char != needle[cursor] {
			continue
		}
		if first < 0 {
			first = index
		}
		last = index
		cursor++
		if cursor == len(needle) {
			return first, last, true
		}
	}
	return 0, 0, false
}

func minSearchInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
