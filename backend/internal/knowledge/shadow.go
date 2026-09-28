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
func (o *ShadowObserver) Observe(ctx context.Context, call ObservedCall) (*entity.ExplorationAttribution, error) {
	if o == nil || o.cfg.Index == nil {
		return nil, nil
	}
	if o.cfg.Mode == ModeOff {
		return nil, nil
	}
	tool := strings.ToLower(strings.TrimSpace(call.Tool))
	if tool != "grep" && tool != "view" {
		return nil, nil
	}
	if strings.TrimSpace(call.Err) != "" {
		return nil, nil
	}

	var (
		rec  *entity.ExplorationAttribution
		hash string
	)
	switch tool {
	case "grep":
		rec, hash = o.observeGrep(ctx, call)
	case "view":
		rec, hash = o.observeView(ctx, call)
	}
	if rec == nil {
		return nil, nil
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
			return rec, fmt.Errorf("knowledge: append exploration attribution: %w", err)
		}
	}
	return rec, nil
}

// observeGrep 处理 `grep pattern`（带 path/glob 作用域）→ `Search(pattern)`。
func (o *ShadowObserver) observeGrep(ctx context.Context, call ObservedCall) (*entity.ExplorationAttribution, string) {
	pattern := argString(call.Args, "pattern")
	scope := firstNonEmpty(argString(call.Args, "path"), argString(call.Args, "glob"), argString(call.Args, "include"))
	baselineKeys, baselineTokens := parseGrepBaseline(call.Output)
	hash := queryHash("grep", pattern, scope)

	rec := &entity.ExplorationAttribution{
		BaselineN:      len(baselineKeys),
		BaselineTokens: baselineTokens,
	}
	if pattern == "" {
		return rec, hash
	}

	hits, err := o.cfg.Index.Search(ctx, SearchQuery{Text: pattern, Limit: shadowCandidateLimit})
	if err == nil {
		scopePrefix := normalizeShadowPath(scope)
		candidateKeys := make([]string, 0, len(hits))
		rendered := make([]string, 0, len(hits))
		for _, hit := range hits {
			path := normalizeShadowPath(hit.Path)
			if scopePrefix != "" && !strings.HasPrefix(path, scopePrefix) {
				continue
			}
			candidateKeys = append(candidateKeys, keyForLine(path, hit.Line))
			rendered = append(rendered, fmt.Sprintf("%s:%d:%s", path, hit.Line, hit.Name))
		}
		rec.CandidateN = len(candidateKeys)
		rec.OverlapN = overlapCount(baselineKeys, candidateKeys)
		rec.CandidateTokens = estimateTokens(strings.Join(rendered, "\n"))
	}
	fillShadowMetrics(rec, o.cfg.Alpha)
	return rec, hash
}

// observeView 处理 `view file offset/limit` → 该区间的符号视图。
//
// 区间映射不精确（ADR-0003 §6.2 已知偏差）：baseline 记 offset/limit 覆盖的
// 行号集合，candidate 记与该区间相交的符号 span；overlap 按"被符号覆盖的行数"
// 计。
func (o *ShadowObserver) observeView(ctx context.Context, call ObservedCall) (*entity.ExplorationAttribution, string) {
	file := normalizeShadowPath(argString(call.Args, "file_path"))
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

// parseGrepBaseline 从 grep 输出解析 (path,line) 条目。
//
// 只认形如 `path:line:` / `path-line-` 的行（含 Windows 盘符路径）；解析不到
// 条目时 baseline_n = 0，按 ADR-0003 §4.5 落库但不进 M1 分母。
func parseGrepBaseline(output string) ([]string, int) {
	if strings.TrimSpace(output) == "" {
		return nil, 0
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
		keys = append(keys, keyForLine(normalizeShadowPath(m[1]), lineNo))
	}
	return keys, estimateTokens(output)
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
