// 增量索引：让「全窗口」基线扫描不必每轮都从字节 0 重读整个 chat-logs 根。
//
// 背景（本机实测 2026-10-02）：根目录 ~2.3k 个 runtime-events.jsonl / ~75 万行 /
// ~400MB。按 mtime 的整文件跳过对 `days=1` 有效（跳 81%），但日志历史横跨 8 天，
// `days>=7` 与默认的 `all` 都会 100% 全量重读，端到端 3.4s——而同一批文件里
// 真正发生变化的只有当前会话那一两个。
//
// 做法：为每个文件记一条「已解析到哪个字节」的账（size / mtime / offset / 该文件
// 解析出的全部事实）。下次扫描：
//
//	条目与当前 (size, mtime) 完全一致 → 直接复用事实，一个字节都不读；
//	size 变大且 mtime 变新            → 只 seek 到 offset 读增量，事实与旧账相加；
//	size 变小 / mtime 倒退 / 找不到  → 视为轮转或截断，整文件重读。
//
// 两条硬纪律：
//
//  1. **不改变口径**。索引命中、增量、全量三条路径必须产出与「关掉索引的串行扫描」
//     逐字节相同的 Stats。靠两件事保证：合并顺序恒等于 collectScanTargets 的
//     WalkDir 自然顺序（requests 顺序会影响 closure 的 sort.SliceStable）；
//     账目里的事实就是该文件全量解析的结果本身，不做二次加工。
//  2. **索引只服务全窗口**（Since 为零值）。窗口查询的 ScanStats.SkippedOld 是
//     逐行按 since 过滤出来的计数，而预筛只解「可能相关」的行，无法从缓存事实里
//     复原这个数。宁可让窗口查询全量读，也不编一个看起来合理的 skipped_old。
//     窗口查询因此只**读**索引（见 exactWindowSkip），不写。
//
// 索引是纯缓存：写坏了、版本对不上、被别的进程写坏，都只是退回全量扫描，
// 绝不影响统计结果，也不该让请求失败——所以 loadIndex 对任何错误都静默降级。
package baseline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

const (
	// indexVersion 是账目格式版本。改动 factRecord 的字段语义时必须 +1：
	// 老账目里的数字含义变了却仍被当作新账目复用，就是静默的口径漂移。
	indexVersion = 1

	// indexFileName 是账目文件名。
	indexFileName = "lsp-baseline-index.json"

	// indexMaxBytes 是可加载账目的体积上限。超过就当作没有账目——
	// 宁可多读一次盘，也不要把一个几百 MB 的 JSON 拉进内存。
	indexMaxBytes = 256 << 20
)

// indexDisabled 报告是否关闭增量索引（排障用：设 AICLI_LSP_BASELINE_INDEX=off
// 即退回改动前的行为，方便对照口径）。
func indexDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_LSP_BASELINE_INDEX"))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// indexPathOverride 是**测试钩子**：把账本重定向到临时目录，使包内测试不会把
// 用户真实的 ~/.aicli/cache 账本换成测试数据。生产路径恒为 ""。
var indexPathOverride string

// indexPath 返回账目文件路径。落在 ~/.aicli/cache/ 下：与被扫描的 chat-logs 根
// 物理分离，绝不会被 collectScanTargets 的 WalkDir 扫到。
func indexPath() string {
	if override := strings.TrimSpace(indexPathOverride); override != "" {
		return override
	}
	return filepath.Join(aiclipaths.DefaultAICLIDir(), "cache", indexFileName)
}

// indexEntry 是单个文件的一条账。
//
// Offset 语义是「已经完整消费到的字节位置」，恒为某个换行符之后的位置，
// 因此它永远落在一行记录的边界上，不会把半行当成完整记录。
type indexEntry struct {
	// Path 是该文件的真实路径。键是小写化的（见 indexKey，为的是和遍历去重
	// 同口径），但小写路径在大小写敏感的文件系统上 stat 不到，所以另存一份
	// 原样路径供「这轮没扫到、但文件还在就续用账目」时使用。
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"` // UnixNano
	Offset  int64  `json:"offset"`   // 已完整消费的字节数
	// FirstTS / LastTS 是该文件里**通过预筛并成功解 JSON** 的事件时间戳的
	// 最小/最大值（UnixNano，0 表示没有）。只有这三类事件会影响输出，预筛恰好
	// 放行的也是这三类，所以用它们判断「整个文件都早于窗口」是精确的。
	FirstTS int64      `json:"first_ts"`
	LastTS  int64      `json:"last_ts"`
	Facts   factRecord `json:"facts"`
}

// indexFile 是账本文件。
type indexFile struct {
	Version int                    `json:"version"`
	SavedAt string                 `json:"saved_at"`
	Entries map[string]*indexEntry `json:"entries"`
}

func emptyIndex() *indexFile {
	return &indexFile{Version: indexVersion, Entries: map[string]*indexEntry{}}
}

// indexKey 是账目的键。用绝对清理路径的小写形式，与 collectScanTargets 去重用的
// key 同口径，保证「多 root 交叠时只处理一次」的语义在账目里同样成立。
func indexKey(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	return strings.ToLower(filepath.Clean(absolute))
}

// loadIndex 读取账本。任何异常（不存在 / 读不了 / JSON 坏了 / 版本不认识 /
// 超过体积上限）都退回空账本——索引只是缓存，不值得为它让统计失败。
func loadIndex() *indexFile {
	if indexDisabled() {
		return emptyIndex()
	}
	path := indexPath()
	if info, err := os.Stat(path); err != nil || info.Size() > indexMaxBytes {
		return emptyIndex()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return emptyIndex()
	}
	var file indexFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return emptyIndex()
	}
	if file.Version != indexVersion {
		return emptyIndex()
	}
	if file.Entries == nil {
		file.Entries = map[string]*indexEntry{}
	}
	return &file
}

// saveIndex 原子落盘：先写同目录临时文件再 rename。同目录是 rename 原子的前提
// （跨卷会退化成 copy）。失败静默——丢一次缓存只是下次慢一点。
func saveIndex(index *indexFile) {
	if indexDisabled() || index == nil || len(index.Entries) == 0 {
		return
	}
	index.Version = indexVersion
	index.SavedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.Marshal(index)
	if err != nil {
		return
	}
	directory := filepath.Dir(indexPath())
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return
	}
	temp, err := os.CreateTemp(directory, indexFileName+".*.tmp")
	if err != nil {
		return
	}
	tempName := temp.Name()
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		os.Remove(tempName)
		return
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return
	}
	// Go 在 Windows 上用 MoveFileEx(MOVEFILE_REPLACE_EXISTING) 实现 Rename，
	// 目标已存在时同样替换。
	if err := os.Rename(tempName, indexPath()); err != nil {
		os.Remove(tempName)
	}
}

// exactWindowSkip 判断一个文件能否在窗口查询里整文件跳过。
//
// 返回 true 的前提是「该文件里所有会影响输出的事件都早于 since」——也就是
// 账目里最后一条相关事件的时间戳仍早于窗口起点。这是精确的（不是启发式）：
// 旧的条件 mtime < since 只是它的宽松版本（mtime 会被 append 刷新，且与事件
// 时间无关），新条件在 mtime 说「要读」时仍可能说「不用读」。
//
// 只在账目确实覆盖了当前文件时才有结论；没有账目返回 false（照常读）。
func exactWindowSkip(entry *indexEntry, since time.Time) bool {
	if since.IsZero() || entry == nil || entry.LastTS == 0 {
		return false
	}
	return time.Unix(0, entry.LastTS).UTC().Before(since)
}

// resolveEntry 决定单个文件这一轮怎么读，并给出该轮开始前要复用的旧事实。
//
// 三条路径，见包注释。base 非 nil 表示这次是「在旧事实上续读增量」，
// 调用方必须把 base 交给 scanFile 做累加式解析。
func resolveEntry(entry *indexEntry, size int64, modTime time.Time) (reuse bool, base scanOutcome, resumable bool) {
	if entry == nil {
		return false, scanOutcome{}, false
	}
	// 账目与现状完全一致：文件没被动过，一个字节都不用读。
	if entry.Size == size && entry.ModTime == modTime.UnixNano() && entry.Offset > 0 {
		return false, entry.Facts.toOutcome(), false
	}
	// 只增不减且账目落在一行边界上：只读增量。offset 越界或已经读到今天的文件末尾
	// （offset == size）时没什么可读，退化成全量重读更省事也更好推理。
	if entry.Offset > 0 && size > entry.Offset && modTime.UnixNano() >= entry.ModTime {
		return false, entry.Facts.toOutcome(), true
	}
	return false, scanOutcome{}, false
}

// factRecord 是 scanOutcome 的可持久化镜像。
//
// 为什么不用 json 直接打 scanOutcome：requestFact / coldFact 的字段全是包内
// 非导出的，encoding/json 会静默跳过它们，产出一份「看着有、其实全空」的账目。
// 显式镜像既是正确性保证，也让磁盘格式不随内部字段重命名而漂移。
type factRecord struct {
	Lines               int                 `json:"lines"`
	Malformed           int                 `json:"malformed"`
	SkippedOld          int                 `json:"skipped_old"`
	EditCalls           int                 `json:"edit_calls"`
	EditOutput          int                 `json:"edit_output"`
	EditOutputEvents    int                 `json:"edit_output_events"`
	Requests            []factRequest       `json:"requests,omitempty"`
	Timestamps          []time.Time         `json:"timestamps,omitempty"`
	SessionRequests     map[string]int      `json:"session_requests,omitempty"`
	EditCallsBySession  map[string]int      `json:"edit_calls_by_session,omitempty"`
	EditOutputBySession map[string]int      `json:"edit_output_by_session,omitempty"`
	ColdFirst           map[string]factCold `json:"cold_first,omitempty"`
}

type factCold struct {
	TS time.Time `json:"ts"`
	MS int       `json:"ms"`
}

type factRequest struct {
	SessionID           string    `json:"sid,omitempty"`
	Day                 string    `json:"day,omitempty"`
	Trigger             string    `json:"trigger,omitempty"`
	Outcome             string    `json:"outcome,omitempty"`
	DurationMS          int       `json:"duration_ms,omitempty"`
	DiagCount           int       `json:"diag_count,omitempty"`
	AppendedBytes       int       `json:"appended_bytes,omitempty"`
	AppendedDiagBytes   int       `json:"appended_diag_bytes,omitempty"`
	AppendedNoteBytes   int       `json:"appended_note_bytes,omitempty"`
	AppendedEmptyBytes  int       `json:"appended_empty_bytes,omitempty"`
	TotalDiagCount      int       `json:"total_diag_count,omitempty"`
	NewDiagCount        int       `json:"new_diag_count,omitempty"`
	OmittedItems        int       `json:"omitted_items,omitempty"`
	OmittedByChars      int       `json:"omitted_by_chars,omitempty"`
	Server              string    `json:"server,omitempty"`
	TS                  time.Time `json:"ts"`
	PathFingerprint     string    `json:"path_fingerprint,omitempty"`
	DiagFingerprint     string    `json:"diag_fingerprint,omitempty"`
	ReasonCategory      string    `json:"reason_category,omitempty"`
	ColdFastFail        bool      `json:"cold_fast_fail,omitempty"`
	ColdProbeClassified bool      `json:"cold_probe_classified,omitempty"`
	AttemptedMembers    int       `json:"attempted_members,omitempty"`
}

func newFactRecord(outcome scanOutcome) factRecord {
	record := factRecord{
		Lines:               outcome.lines,
		Malformed:           outcome.malformed,
		SkippedOld:          outcome.skippedOld,
		EditCalls:           outcome.editCalls,
		EditOutput:          outcome.editOutput,
		EditOutputEvents:    outcome.editOutputEvents,
		SessionRequests:     outcome.sessionRequests,
		EditCallsBySession:  outcome.editCallsBySession,
		EditOutputBySession: outcome.editOutputBySession,
	}
	if len(outcome.requests) > 0 {
		record.Requests = make([]factRequest, 0, len(outcome.requests))
		for _, fact := range outcome.requests {
			record.Requests = append(record.Requests, factRequest{
				SessionID:           fact.sessionID,
				Day:                 fact.day,
				Trigger:             fact.trigger,
				Outcome:             fact.outcome,
				DurationMS:          fact.durationMS,
				DiagCount:           fact.diagCount,
				AppendedBytes:       fact.appendedBytes,
				AppendedDiagBytes:   fact.appendedDiagBytes,
				AppendedNoteBytes:   fact.appendedNoteBytes,
				AppendedEmptyBytes:  fact.appendedEmptyBytes,
				TotalDiagCount:      fact.totalDiagCount,
				NewDiagCount:        fact.newDiagCount,
				OmittedItems:        fact.omittedItems,
				OmittedByChars:      fact.omittedByChars,
				Server:              fact.server,
				TS:                  fact.ts,
				PathFingerprint:     fact.pathFingerprint,
				DiagFingerprint:     fact.diagFingerprint,
				ReasonCategory:      fact.reasonCategory,
				ColdFastFail:        fact.coldFastFail,
				ColdProbeClassified: fact.coldProbeClassified,
				AttemptedMembers:    fact.attemptedMembers,
			})
		}
	}
	if len(outcome.timestamps) > 0 {
		record.Timestamps = append([]time.Time(nil), outcome.timestamps...)
	}
	if len(outcome.coldFirst) > 0 {
		record.ColdFirst = make(map[string]factCold, len(outcome.coldFirst))
		for key, fact := range outcome.coldFirst {
			record.ColdFirst[key] = factCold{TS: fact.ts, MS: fact.ms}
		}
	}
	return record
}

// toOutcome 把账目还原成该文件的事实。它必须与「从零全量解析该文件」的结果
// 完全一致，否则索引命中就会悄悄改口径——这是本文件唯一需要盯住的往返不变式，
// 由 TestIndexRoundTripPreservesOutcome 逐字段钉死。
func (record factRecord) toOutcome() scanOutcome {
	outcome := scanOutcome{
		opened:              true,
		lines:               record.Lines,
		malformed:           record.Malformed,
		skippedOld:          record.SkippedOld,
		editCalls:           record.EditCalls,
		editOutput:          record.EditOutput,
		editOutputEvents:    record.EditOutputEvents,
		sessionRequests:     record.SessionRequests,
		editCallsBySession:  record.EditCallsBySession,
		editOutputBySession: record.EditOutputBySession,
	}
	if len(record.Requests) > 0 {
		outcome.requests = make([]requestFact, 0, len(record.Requests))
		for _, fact := range record.Requests {
			outcome.requests = append(outcome.requests, requestFact{
				sessionID:           fact.SessionID,
				day:                 fact.Day,
				trigger:             fact.Trigger,
				outcome:             fact.Outcome,
				durationMS:          fact.DurationMS,
				diagCount:           fact.DiagCount,
				appendedBytes:       fact.AppendedBytes,
				appendedDiagBytes:   fact.AppendedDiagBytes,
				appendedNoteBytes:   fact.AppendedNoteBytes,
				appendedEmptyBytes:  fact.AppendedEmptyBytes,
				totalDiagCount:      fact.TotalDiagCount,
				newDiagCount:        fact.NewDiagCount,
				omittedItems:        fact.OmittedItems,
				omittedByChars:      fact.OmittedByChars,
				server:              fact.Server,
				ts:                  fact.TS,
				pathFingerprint:     fact.PathFingerprint,
				diagFingerprint:     fact.DiagFingerprint,
				reasonCategory:      fact.ReasonCategory,
				coldFastFail:        fact.ColdFastFail,
				coldProbeClassified: fact.ColdProbeClassified,
				attemptedMembers:    fact.AttemptedMembers,
			})
		}
	}
	if len(record.Timestamps) > 0 {
		outcome.timestamps = append([]time.Time(nil), record.Timestamps...)
	}
	if len(record.ColdFirst) > 0 {
		outcome.coldFirst = make(map[string]coldFact, len(record.ColdFirst))
		for key, fact := range record.ColdFirst {
			outcome.coldFirst[key] = coldFact{ts: fact.TS, ms: fact.MS}
		}
	}
	return outcome
}

// unixNanoOrZero 把零值时间映射成 0，让「该文件没有任何相关事件」在账目里是
// 可区分的（0 而不是 0001-01-01 那样的合法纳秒数）。
func unixNanoOrZero(moment time.Time) int64 {
	if moment.IsZero() {
		return 0
	}
	return moment.UnixNano()
}

// carryForwardIndexEntries 把「上一轮有账、这轮没扫到」且**文件仍在磁盘上**的条目
// 搬进新账本。
//
// 为什么需要：Analyze 的 Roots 是调用方给的，同一份账本可能被不同根交替使用；
// 会话目录轮转后旧的 key 会自动消失。两种情况下，若一律丢弃，这轮没被扫到的
// 文件下次就得从头重读——账本每轮都白建。
//
// 判定用文件大小而不是「mtime 也要一致」：这轮没扫到意味着我们没读过它，
// 它在这期间完全可能又追加过；而账目里的 Offset 仍然指向一个正确位置
// （追加只往后长），下次扫到时 scanTargetWithIndex 会因为 size 对不上而走增量
// 分支，语义完全正确。文件被删则 stat 失败，条目随之丢弃。
func carryForwardIndexEntries(previous, next *indexFile) {
	for key, entry := range previous.Entries {
		if entry == nil {
			continue
		}
		if _, already := next.Entries[key]; already {
			continue
		}
		path := entry.Path
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err != nil || info.Size() != entry.Size {
			continue
		}
		next.Entries[key] = entry
	}
}
