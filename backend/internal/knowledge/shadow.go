package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// DefaultShadowAlpha 是覆盖率阈值 α 的联调初值。
//
// ADR-0003 §4.2 明确规定：α 由 Phase 1 shadow 实测产出，本值**不得**作为
// 验收门槛，只用于让管线先跑通。
const DefaultShadowAlpha = 0.8

// ShadowIndex 是 shadow 对比所需的索引侧只读面。
//
// 由 *Layer（或底层 store）实现；shadow 观察器只依赖这两个查询，
// 不依赖任何写路径，因此 reader 角色也能安全参与对比。
type ShadowIndex interface {
	Search(ctx context.Context, q SearchQuery) ([]SearchHit, error)
	FindSymbols(ctx context.Context, q SymbolQuery) ([]Symbol, error)
}

// AttributionSink 是 exploration_attribution 的落库口（由 usageledger 实现）。
type AttributionSink interface {
	AppendExplorationAttribution(ctx context.Context, rec *entity.ExplorationAttribution) error
}

// ObservedCall 是一次被观察的 grep / view 调用。
type ObservedCall struct {
	SessionID string
	TurnID    string
	Tool      string
	Args      map[string]any
	// Output 是工具实际返回给模型的文本（baseline 的唯一来源）。
	Output string
	// Err 非空表示调用失败；失败调用没有可信 baseline，不落行。
	Err string
}

// ShadowConfig 组装一个 ShadowObserver。
type ShadowConfig struct {
	// Alpha 覆盖率阈值；<= 0 时用 DefaultShadowAlpha。
	Alpha float64
	// Mode 产生本批数据的知识层模式（shadow | on）；off 时观察器整体 no-op。
	Mode Mode
	// Workspace 是工作区根目录（绝对路径）。调用参数里的绝对 path / file_path
	// 必须先折叠为 workspace 相对路径才能与索引的 files.path 对齐；不折叠会让
	// 带绝对作用域的调用候选恒为空（Phase1-shadow 实测发现的缺陷）。
	Workspace string
	// ProjectID 工作区稳定键（哈希前缀），由接入方计算。
	ProjectID string
	// Index 索引侧只读面；nil 时观察器 no-op（防御分支）。
	Index ShadowIndex
	// Sink 落库口；nil 时只计算不落库（测试与只读探测用）。
	Sink AttributionSink
	// Now 便于测试注入时钟；nil 时用 time.Now。
	Now func() time.Time
}

// ShadowObserver 计算并记录一次被拦截调用的对比口径（ADR-0003 §4.2/§4.4）。
//
// 契约：
//   - 尽力而为：Observe 的错误由调用方决定是否记录，**不得**冒泡为 turn 失败；
//   - 只读：不修改任何工具结果，不影响工具返回值；
//   - 隐私：只落 query_hash，不落 pattern 明文（§4.6）。
type ShadowObserver struct {
	cfg ShadowConfig
}

// NewShadowObserver 创建观察器并填充默认值。
func NewShadowObserver(cfg ShadowConfig) *ShadowObserver {
	if cfg.Alpha <= 0 {
		cfg.Alpha = DefaultShadowAlpha
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &ShadowObserver{cfg: cfg}
}

// ShadowObserverFor 从知识层 Activation 组装 Phase 1 shadow 观察器。
//
// 仅当知识层以 shadow 模式启用、Layer 可用且提供落库口时返回非 nil；
// 其余情况（含 act == nil）返回 nil，调用方据此保持"无知识层"行为不变。
// ADR-0003 §4.4：观察器只读索引、旁路落库，不得影响工具结果与 turn 结果。
func ShadowObserverFor(act *Activation, sink AttributionSink) *ShadowObserver {
	if act == nil || sink == nil || act.Mode() != ModeShadow {
		return nil
	}
	layer := act.Layer()
	if layer == nil {
		return nil
	}
	return NewShadowObserver(ShadowConfig{
		Mode:      ModeShadow,
		Alpha:     act.Config().Alpha,
		Workspace: act.Workspace(),
		ProjectID: ProjectIDForWorkspace(act.Workspace()),
		Index:     layer,
		Sink:      sink,
	})
}

// ProjectIDForWorkspace 把工作区根目录折叠成稳定短键：同一工作区永远得到同一
// project_id，但落库值不含绝对路径（ADR-0003 §4.6 隐私约束）。
func ProjectIDForWorkspace(workspace string) string {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		return ""
	}
	normalized := strings.ToLower(filepath.ToSlash(ws))
	sum := sha256.Sum256([]byte(normalized))
	return "ws-" + hex.EncodeToString(sum[:8])
}

// Observe 对比一次调用并（尽力）落库，返回计算出的行。
//
// 返回 (nil, nil) 的场合：观察器/index 未配置、mode=off、非 grep/view、
// 调用失败。这些场合都不是错误，调用方无需分支。
// ObservationDetail 是 observeDetailed 的附带信息：两侧文件集合。
//
// 不落库、不改变 Observe 语义；只供 Phase1-shadow 的口径裁决测量使用
// （行级覆盖 vs file-level 覆盖，见 reports/phase1_shadow_report.md）。
type ObservationDetail struct {
	BaselineFiles  map[string]struct{}
	CandidateFiles map[string]struct{}
}

func (o *ShadowObserver) Observe(ctx context.Context, call ObservedCall) (*entity.ExplorationAttribution, error) {
	rec, _, err := o.observeDetailed(ctx, call)
	return rec, err
}

// observeDetailed 与 Observe 同流程，额外返回两侧文件集合。
func (o *ShadowObserver) observeDetailed(ctx context.Context, call ObservedCall) (*entity.ExplorationAttribution, ObservationDetail, error) {
	var detail ObservationDetail
	if o == nil || o.cfg.Index == nil {
		return nil, detail, nil
	}
	if o.cfg.Mode == ModeOff {
		return nil, detail, nil
	}
	tool := strings.ToLower(strings.TrimSpace(call.Tool))
	if tool != "grep" && tool != "view" {
		return nil, detail, nil
	}
	if strings.TrimSpace(call.Err) != "" {
		return nil, detail, nil
	}

	var (
		rec  *entity.ExplorationAttribution
		hash string
	)
	switch tool {
	case "grep":
		grepObs, grepHash := o.observeGrepDetailed(ctx, call)
		rec, hash = grepObs.Record, grepHash
		detail.BaselineFiles = grepObs.BaselineFiles
		detail.CandidateFiles = grepObs.CandidateFiles
	case "view":
		rec, hash = o.observeView(ctx, call)
		if file := relativizeWorkspacePath(argString(call.Args, "file_path"), o.cfg.Workspace); file != "" {
			detail.BaselineFiles = map[string]struct{}{file: {}}
			if rec != nil && rec.CandidateN > 0 {
				detail.CandidateFiles = map[string]struct{}{file: {}}
			}
		}
	}
	if rec == nil {
		return nil, detail, nil
	}
	rec.ID = uuid.NewString()
	rec.SessionID = call.SessionID
	rec.TurnID = call.TurnID
	rec.Tool = tool
	rec.QueryHash = hash
	rec.ProjectID = o.cfg.ProjectID
	rec.Source = "heuristic"
	rec.KnowledgeMode = string(o.cfg.Mode)
	rec.CreatedAt = o.cfg.Now().UTC()

	if o.cfg.Sink != nil {
		if err := o.cfg.Sink.AppendExplorationAttribution(ctx, rec); err != nil {
			return rec, detail, fmt.Errorf("knowledge: append exploration attribution: %w", err)
		}
	}
	return rec, detail, nil
}

// observeGrep 处理 `grep pattern`（带 path/glob 作用域）→ 索引侧候选。
func (o *ShadowObserver) observeGrep(ctx context.Context, call ObservedCall) (*entity.ExplorationAttribution, string) {
	obs, hash := o.observeGrepDetailed(ctx, call)
	return obs.Record, hash
}

// shadowGrepObservation 是 grep 观测的完整结果：落库行 + 两侧文件集合。
//
// 文件集合不落库（ADR-0003 §4.1 冻结了 exploration_attribution 的列集），
// 只供 Phase1-shadow 的口径裁决测量使用（行级覆盖 vs file-level 覆盖）。
type shadowGrepObservation struct {
	Record         *entity.ExplorationAttribution
	BaselineFiles  map[string]struct{}
	CandidateFiles map[string]struct{}
}

// observeGrepDetailed 是 observeGrep 的完整实现；Observe 只用 Record。
func (o *ShadowObserver) observeGrepDetailed(ctx context.Context, call ObservedCall) (shadowGrepObservation, string) {
	rawPatterns := grepPatternList(call.Args)
	pattern := strings.Join(rawPatterns, " | ")
	// path/paths 与 glob/include 可能同时给出（rg 语义：根目录集合 + 文件名过滤），
	// 约束都要参与候选过滤：多路径前缀是 OR、glob 是 AND；baseline 输出的相对
	// 前缀按"已属于某作用域则原样、否则回退首项"补全（与历史单路径语义一致）。
	pathScopes := grepPathScopes(call.Args)
	globScope := firstNonEmpty(argString(call.Args, "glob"), argString(call.Args, "include"))
	filter := newScopeFilterSpecMulti(pathScopes, globScope, o.cfg.Workspace)
	scopeBases := make([]string, 0, len(pathScopes))
	for _, scope := range pathScopes {
		if base := relativizeWorkspacePath(scope, o.cfg.Workspace); base != "" {
			scopeBases = append(scopeBases, base)
		}
	}
	baselineKeys, baselineTokens := parseGrepBaselineMulti(call.Output, scopeBases)
	hash := queryHash("grep", pattern, strings.TrimSpace(strings.Join(pathScopes, " ")+" "+globScope))

	rec := &entity.ExplorationAttribution{
		BaselineN:      len(baselineKeys),
		BaselineTokens: baselineTokens,
	}
	out := shadowGrepObservation{
		Record:         rec,
		BaselineFiles:  make(map[string]struct{}, len(baselineKeys)),
		CandidateFiles: make(map[string]struct{}, shadowCandidateLimit),
	}
	for _, key := range baselineKeys {
		out.BaselineFiles[pathFromKey(key)] = struct{}{}
	}
	if len(rawPatterns) == 0 {
		return out, hash
	}

	// 候选映射：regex 交替拆成多个字面 token，逐个检索并按 (path,line) 去重，
	// 再截断到 shadowCandidateLimit。直接拿 regex 检索 FTS 会恒为空。
	// 两条候选通道并用：symbols（定义行）+ refs（使用点行）——后者让候选集
	// 覆盖"非定义行"，逼近文本 grep 的命中原象（Phase1-shadow 实测驱动）。
	tokens := collectShadowTokens(rawPatterns)
	if len(tokens) == 0 {
		tokens = rawPatterns
	}
	candidateKeys := make([]string, 0, shadowCandidateLimit)
	rendered := make([]string, 0, shadowCandidateLimit)
	seenCandidates := make(map[string]bool)
	appendCandidate := func(path string, line int, name string) {
		if len(candidateKeys) >= shadowCandidateLimit {
			return
		}
		path = normalizeShadowPath(path)
		if path == "" || !filter.matches(path) {
			return
		}
		key := keyForLine(path, line)
		if seenCandidates[key] {
			return
		}
		seenCandidates[key] = true
		out.CandidateFiles[path] = struct{}{}
		candidateKeys = append(candidateKeys, key)
		rendered = append(rendered, fmt.Sprintf("%s:%d:%s", path, line, name))
	}
	for _, token := range tokens {
		if len(candidateKeys) >= shadowCandidateLimit {
			break
		}
		hits, err := o.cfg.Index.Search(ctx, SearchQuery{Text: token, Limit: shadowCandidateLimit})
		if err == nil {
			for _, hit := range hits {
				appendCandidate(hit.Path, hit.Line, hit.Name)
			}
		}
		if len(candidateKeys) >= shadowCandidateLimit {
			break
		}
		if refIndex, ok := o.cfg.Index.(refCandidateIndex); ok {
			refs, err := refIndex.FindRefs(ctx, RefQuery{ToSymbolName: token, Limit: shadowRefCandidateLimit})
			if err == nil {
				for _, ref := range refs {
					appendCandidate(ref.Path, ref.Line, ref.ToSymbolName)
				}
			}
		}
	}
	rec.CandidateN = len(candidateKeys)
	rec.OverlapN = overlapCount(baselineKeys, candidateKeys)
	rec.CandidateTokens = estimateTokens(strings.Join(rendered, "\n"))
	fillShadowMetrics(rec, o.cfg.Alpha)
	return out, hash
}

// observeView 处理 `view file offset/limit` → 该区间的符号视图。
//
// 区间映射不精确（ADR-0003 §6.2 已知偏差）：baseline 记 offset/limit 覆盖的
// 行号集合，candidate 记与该区间相交的符号 span；overlap 按"被符号覆盖的行数"
// 计。
func (o *ShadowObserver) observeView(ctx context.Context, call ObservedCall) (*entity.ExplorationAttribution, string) {
	file := relativizeWorkspacePath(argString(call.Args, "file_path"), o.cfg.Workspace)
	offset := intArg(call.Args, "offset")
	if offset <= 0 {
		offset = 1
	}
	limit := intArg(call.Args, "limit")
	if limit <= 0 {
		limit = countNonEmptyLines(call.Output)
	}
	scope := fmt.Sprintf("%s:%d+%d", file, offset, limit)
	hash := queryHash("view", file, scope)

	baselineTokens := estimateTokens(call.Output)
	baselineN := limit
	if file == "" || baselineN <= 0 {
		return &entity.ExplorationAttribution{BaselineTokens: baselineTokens}, hash
	}

	rec := &entity.ExplorationAttribution{
		BaselineN:      baselineN,
		BaselineTokens: baselineTokens,
	}
	syms, err := o.cfg.Index.FindSymbols(ctx, SymbolQuery{PathPrefix: file, Limit: shadowCandidateLimit})
	if err == nil {
		end := offset + limit - 1
		candidateN := 0
		covered := 0
		rendered := make([]string, 0, len(syms))
		for _, sym := range syms {
			start, stop := sym.Range.Start.Line, sym.Range.End.Line
			if stop < start {
				stop = start
			}
			if stop < offset || start > end {
				continue
			}
			candidateN++
			lo, hi := maxInt(start, offset), minInt(stop, end)
			if hi >= lo {
				covered += hi - lo + 1
			}
			rendered = append(rendered, fmt.Sprintf("%s:%d-%d:%s", file, start, stop, sym.Name))
		}
		rec.CandidateN = candidateN
		rec.OverlapN = minInt(covered, baselineN)
		rec.CandidateTokens = estimateTokens(strings.Join(rendered, "\n"))
	}
	fillShadowMetrics(rec, o.cfg.Alpha)
	return rec, hash
}

// fillShadowMetrics 按 ADR-0003 §4.2 计算 coverage / economy / usable。
//
// coverage := overlap_n / baseline_n（baseline_n > 0）
// economy  := candidate_tokens / baseline_tokens
// usable   := (coverage >= α) AND (economy <= 1.0)
//
// baseline_tokens == 0 且 baseline_n > 0 时 economy 记 NULL，usable 只由
// coverage 决定（两侧都是零成本，等价于通过经济性）。
func fillShadowMetrics(rec *entity.ExplorationAttribution, alpha float64) {
	if rec.BaselineN > 0 {
		coverage := float64(rec.OverlapN) / float64(rec.BaselineN)
		rec.Coverage = &coverage
	}
	if rec.BaselineTokens > 0 {
		economy := float64(rec.CandidateTokens) / float64(rec.BaselineTokens)
		rec.Economy = &economy
	}
	usable := rec.Coverage != nil && *rec.Coverage >= alpha
	if usable && rec.Economy != nil {
		usable = *rec.Economy <= 1.0
	}
	rec.Usable = usable
}

const shadowCandidateLimit = 100

// shadowRefCandidateLimit 限制单个 token 的引用点候选数；refs 表按名字查
// 常见标识符可能返回很多使用点，需要与符号候选共享同一截断预算。
const shadowRefCandidateLimit = 100

// refCandidateIndex 是 ShadowIndex 的**可选**扩展：按名字解析使用点（refs）。
//
// 生产 Layer / sqliteStore 都实现它；最小 fake 只实现 Search + FindSymbols 时，
// 观察器自动退化为"仅符号定义行"的候选集，不破坏既有实现与测试。
type refCandidateIndex interface {
	FindRefs(ctx context.Context, q RefQuery) ([]Reference, error)
}

// parseGrepBaseline 从 grep 输出解析 (path,line) 条目。
//
// 只认形如 `path:line:` / `path-line-` 的行（含 Windows 盘符路径）；解析不到
// 条目时 baseline_n = 0，按 ADR-0003 §4.5 落库但不进 M1 分母。
//
// scopeBase 是折叠后的作用域（目录或文件）。rg 以 path 参数为根输出相对路径
// （`rg pattern backend` 输出 `internal/...`），而候选来自 files.path 的
// workspace 相对路径（`backend/internal/...`）——不补全前缀，两侧 (path,line)
// 永远无法相交（Phase1-shadow 重放实测发现的缺陷）。glob 作用域不参与前缀。
func parseGrepBaseline(output, scopeBase string) ([]string, int) {
	return parseGrepBaselineMulti(output, []string{scopeBase})
}

// parseGrepBaselineMulti 同 parseGrepBaseline，但接受多个作用域基准（rg 多路径）：
// 逐行先判"已属于某基准"（原样保留），否则按首个可用基准补前缀（历史单路径语义）。
func parseGrepBaselineMulti(output string, scopeBases []string) ([]string, int) {
	if strings.TrimSpace(output) == "" {
		return nil, 0
	}
	bases := make([]string, 0, len(scopeBases))
	for _, base := range scopeBases {
		if base = strings.TrimSuffix(strings.TrimSpace(base), "/"); base != "" {
			bases = append(bases, base)
		}
	}
	var keys []string
	for _, line := range strings.Split(output, "\n") {
		m := grepLinePattern.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		lineNo, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		keys = append(keys, keyForLine(prefixScopePathMulti(normalizeShadowPath(m[1]), bases), lineNo))
	}
	return keys, estimateTokens(output)
}

// prefixScopePath 把"相对作用域目录"的 grep 输出路径补全成 workspace 相对路径。
func prefixScopePath(path, scopeBase string) string {
	return prefixScopePathMulti(path, []string{scopeBase})
}

// prefixScopePathMulti 在多作用域基准下补全输出路径：先看它是否已属于某个基准
// （属于则原样，避免把 `docs/b.md` 错缀成 `backend/docs/b.md`）；绝对路径与 glob
// 基准不参与补全；都不匹配时回退首个可用基准（历史单路径行为）。
func prefixScopePathMulti(path string, scopeBases []string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	lowerPath := strings.ToLower(path)
	for _, base := range scopeBases {
		base = strings.TrimSuffix(strings.TrimSpace(base), "/")
		if base == "" || base == "." || strings.ContainsAny(base, "*?[") {
			continue
		}
		if strings.HasPrefix(lowerPath, strings.ToLower(base)+"/") {
			return path
		}
	}
	if isAbsoluteLikePath(path) {
		return path
	}
	for _, base := range scopeBases {
		base = strings.TrimSuffix(strings.TrimSpace(base), "/")
		if base == "" || base == "." || strings.ContainsAny(base, "*?[") {
			continue
		}
		return base + "/" + path
	}
	return path
}

// isAbsoluteLikePath 报告路径是否形如 `/x` 或 `C:/x`（已归一化为 '/'）。
func isAbsoluteLikePath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && p[2] == '/'
}

var grepLinePattern = regexp.MustCompile(`^(.+?):(\d+)[:-]`)

func queryHash(tool, pattern, scope string) string {
	sum := sha256.Sum256([]byte(tool + "\x00" + pattern + "\x00" + scope))
	return hex.EncodeToString(sum[:])
}

func keyForLine(path string, line int) string {
	return path + ":" + strconv.Itoa(line)
}

func overlapCount(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(b))
	for _, item := range b {
		set[item] = struct{}{}
	}
	count := 0
	for _, item := range a {
		if _, ok := set[item]; ok {
			count++
		}
	}
	return count
}

func normalizeShadowPath(p string) string {
	return strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (utf8.RuneCountInString(s) + 3) / 4
}

func countNonEmptyLines(s string) int {
	count := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// grepPathScopes 收集 grep 调用的全部路径作用域：`path` 单值在前，`paths`
// （数组或单字符串）逐项追加，按大小写不敏感去重。取值语义与工具端
// resolveSearchPathListParam 一致：字符串即单路径，不做分隔符拆分。
func grepPathScopes(args map[string]any) []string {
	if args == nil {
		return nil
	}
	out := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			return
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	add(argString(args, "path"))
	switch raw := args["paths"].(type) {
	case string:
		add(raw)
	case []string:
		for _, v := range raw {
			add(v)
		}
	case []any:
		for _, v := range raw {
			if s, ok := v.(string); ok {
				add(s)
			}
		}
	}
	return out
}

func intArg(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
