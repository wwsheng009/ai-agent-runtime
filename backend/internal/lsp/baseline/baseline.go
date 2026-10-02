// Package baseline 把会话 runtime 事件日志归因成 LSP 基线报告（方案 §4.3）。
//
// 与 scripts/analyze-lsp-baseline.py（离线批量镜像）保持同一口径：
//   - 数据源：<root>/**/runtime-events.jsonl，逐行 runtime 事件对象；
//   - lsp.request.finished：一行 = 一次后写请求，载荷为标量；
//   - 覆盖率分母限定「LSP 活跃会话」内的编辑类 tool.completed（§4.3 反模式：
//     不得把未启用 LSP 的会话算进分母，也不得把未采集渲染成 0）；
//   - 未采集项输出 n/a + 原因；阈值一律「待标定」，本包不做阈值告警。
//
// 两侧实现由同一 fixture 数字互锁（3 请求 / 覆盖率 1.0 / P50 10ms /
// 追加比 100:1100），见 baseline_test.go 与脚本 --selftest。
package baseline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// EventLSPRequest Finished 是请求级埋点事件类型（A 通道落盘）。
	eventLSPRequest = "lsp.request.finished"
	// eventToolCompleted 是编辑调用分母来源（payload.logical_tool）。
	eventToolCompleted = "tool.completed"
	// eventLSPServerState 承载冷启动观测：首个发布时补发的状态事件带
	// first_publish_ms（启动→首个发布的延迟，plan §5.8 第三轮）。
	eventLSPServerState = "lsp.server.state"
)

// editingTools 与 §3.1 mutated_paths 覆盖面同口径。
var editingTools = map[string]struct{}{
	"apply_patch":        {},
	"write":              {},
	"edit":               {},
	"notebook_edit":      {},
	"str_replace_editor": {},
	"patch":              {},
}

// Options 控制扫描范围。
type Options struct {
	// Roots 是要扫描的 chat-logs 根目录（可多个）；默认调用方给
	// aiclipaths.DefaultChatLogsDir()。
	Roots []string
	// Since 只保留该时刻之后的事件（零值 = 全窗口）。
	Since time.Time
	// Now 供测试注入时钟；零值取 time.Now。
	Now time.Time
}

// ScanStats 记录扫描过程事实（报告头部展示）。
type ScanStats struct {
	Files      int `json:"files"`
	Lines      int `json:"lines"`
	Malformed  int `json:"malformed"`
	SkippedOld int `json:"skipped_old"`
	// SkippedFiles 是整文件跳过的数量（窗口过滤的快速路径）：文件最后写入早于
	// since、或账目显示其最后一条相关事件早于 since 时，它的全部事件必然早于
	// since，无需打开、不计行。两者都是「可证明不含窗口内事件」的充分条件。
	SkippedFiles int `json:"skipped_files"`
	// 以下三项是增量索引的可观测性（见 incremental.go）。它们只描述**这轮怎么
	// 读的**，不参与任何统计口径；老构建/关掉索引时全为 0，渲染为"未使用索引"。
	//   - ReusedFiles：账目与文件完全一致，一个字节都没读；
	//   - DeltaFiles：只 seek 读了增量尾部；
	//   - IndexedFiles：这轮结束后写进账本的文件数。
	ReusedFiles  int `json:"reused_files,omitempty"`
	DeltaFiles   int `json:"delta_files,omitempty"`
	IndexedFiles int `json:"indexed_files,omitempty"`
}

// DayBucket 是按日明细。
type DayBucket struct {
	Requests  int   `json:"requests"`
	Injected  int   `json:"injected"`
	Degraded  int   `json:"degraded"`
	Durations []int `json:"-"` // 百分位用；不序列化
	P95MS     *int  `json:"p95_ms,omitempty"`
}

// Stats 是聚合结果（JSON 键与离线脚本 --json 对齐）。
type Stats struct {
	Requests       int            `json:"requests"`
	Triggers       map[string]int `json:"triggers"`
	Outcomes       map[string]int `json:"outcomes"`
	Servers        map[string]int `json:"servers"`
	Injected       int            `json:"injected"`
	DiagHit        int            `json:"diag_hit"`
	Clean          int            `json:"clean"`
	NoServer       int            `json:"no_server"`
	Degraded       int            `json:"degraded"`
	LatencyP50MS   *int           `json:"latency_p50_ms"`
	LatencyP95MS   *int           `json:"latency_p95_ms"`
	AppendedBytes  int            `json:"appended_bytes"`
	DiagCount      int            `json:"diag_count"`
	Truncated      int            `json:"truncated_requests"`
	OmittedItems   int            `json:"omitted_items"`
	OmittedByChars int            `json:"omitted_by_chars"`
	// Append breakdown（O2，2026-10-01）：事件携带的诊断正文/提示/空块字节拆分。
	// 旧构建无字段时保持 0，渲染为"未采集"（不伪造 0）。
	AppendedDiagBytes  int `json:"appended_diag_bytes,omitempty"`
	AppendedNoteBytes  int `json:"appended_note_bytes,omitempty"`
	AppendedEmptyBytes int `json:"appended_empty_bytes,omitempty"`
	// A6 decision data（O9）：全量诊断条数与其中"编辑前不存在"的新增条数
	// （scope 过滤前统计；旧构建无字段时保持 0，渲染为未采集）。
	TotalDiagCount int `json:"total_diag_count,omitempty"`
	NewDiagCount   int `json:"new_diag_count,omitempty"`
	// ColdFirstProbe / ColdRepeat：no_fresh 请求中"首探针"（路径未标记已知冷，
	// 按完整预算等待）与"重复探针"（cold_fast_fail=true，已被路径级快速失败
	// 覆盖）的拆分；前者是下一轮 cold_probe 预算的判据（O4）。
	ColdFirstProbe int `json:"cold_first_probe,omitempty"`
	ColdRepeat     int `json:"cold_repeat,omitempty"`
	// MultiMemberRequests / AttemptedMembersMax：一次请求尝试 >1 个成员的次数与
	// 最大成员数（多成员工作区是否出现叠加等待的判据，O5）。
	MultiMemberRequests int `json:"multi_member_requests,omitempty"`
	AttemptedMembersMax int `json:"attempted_members_max,omitempty"`
	// DegradeReasons breaks degraded requests down by the low-sensitivity
	// reason_category enum (new-build events only; legacy events lack it).
	DegradeReasons map[string]int `json:"degrade_reasons,omitempty"`
	// ColdFirstPublish*：服务启动→首个诊断发布的延迟（毫秒）。按 (session, server)
	// 取首个带 first_publish_ms 的状态事件；旧构建无该字段时输出 n/a（不把未采集
	// 渲染成 0）。
	ColdFirstPublishP50MS *int `json:"cold_first_publish_p50_ms,omitempty"`
	ColdFirstPublishP95MS *int `json:"cold_first_publish_p95_ms,omitempty"`
	coldStarts            []int
	// Closure: injected requests whose diagnostic set disappeared on the next
	// edit of the same file (outcome=clean). Requires fingerprint-bearing
	// events (plan §3.3); zero eligible means "not collected", not "0".
	ClosureEligible int                   `json:"closure_eligible"`
	ClosureClosed   int                   `json:"closure_closed"`
	Sessions        int                   `json:"sessions"`
	FirstAt         string                `json:"first_at"`
	LastAt          string                `json:"last_at"`
	EditCalls       int                   `json:"edit_calls"`
	EditOutput      int                   `json:"edit_output_bytes"`
	EditOutputEvs   int                   `json:"edit_output_events"`
	ActiveEdit      int                   `json:"active_edit_calls"`
	ActiveOutput    int                   `json:"active_edit_output_bytes"`
	ByDay           map[string]*DayBucket `json:"by_day"`
	Scan            ScanStats             `json:"scan"`
	// GeneratedAt 是本次归因时刻（报告日期列）。
	GeneratedAt string `json:"generated_at"`

	durations []int
}

// Row 是 §4.3 登记表的一行。
type Row struct {
	Metric     string `json:"metric"`
	Value      string `json:"value"`
	Window     string `json:"window"`
	Samples    string `json:"samples"`
	Conclusion string `json:"conclusion"`
	Date       string `json:"date"`
}

// rawEvent 只解出归因需要的字段，避免为载荷建全量结构。
type rawEvent struct {
	Type      string                 `json:"type"`
	SessionID string                 `json:"session_id"`
	Timestamp time.Time              `json:"timestamp"`
	Payload   map[string]interface{} `json:"payload"`
}

// requestFact 是单条 lsp.request.finished 的归因事实。
//
// 字段保持包内可见：扫描与增量索引有 50+ 处按小写名构造/读取，导出它们只会
// 平白改动整条扫描路径、放大回归面。跨包的数据源（分析库）经 facts.go 的
// FactInput 构造，不直接依赖字段名。
type requestFact struct {
	sessionID           string
	day                 string
	trigger             string
	outcome             string
	durationMS          int
	diagCount           int
	appendedBytes       int
	appendedDiagBytes   int
	appendedNoteBytes   int
	appendedEmptyBytes  int
	totalDiagCount      int
	newDiagCount        int
	omittedItems        int
	omittedByChars      int
	server              string
	ts                  time.Time
	pathFingerprint     string
	diagFingerprint     string
	reasonCategory      string
	coldFastFail        bool
	coldProbeClassified bool
	attemptedMembers    int
}

// coldFact 是 (session, server) 的首发布延迟观测。
type coldFact struct {
	ts time.Time
	ms int
}

// scanOutcome 是单个 runtime-events.jsonl 的私有解析结果。
//
// 不变量（并发安全的前提）：它只承载「本文件自己的计数与事实」，绝不写入任何
// 跨文件累加器。并发阶段每个 goroutine 只写自己下标对应的那一格 scanOutcome，
// 因此没有共享可变状态、无需锁；所有累加都推迟到 Analyze 里按遍历顺序串行发生。
type scanOutcome struct {
	// opened 表示文件成功打开；打不开时整个 outcome 被忽略——既不计入
	// Scan.Files，也不中断整次统计，与串行版「跳过并继续」一致。
	opened              bool
	lines               int
	malformed           int
	skippedOld          int
	editCalls           int
	editOutput          int
	editOutputEvents    int
	requests            []requestFact
	timestamps          []time.Time
	sessionRequests     map[string]int
	editCallsBySession  map[string]int
	editOutputBySession map[string]int
	coldFirst           map[string]coldFact
	// 增量索引的读法标记（不参与统计口径，只用于 ScanStats 的可观测性）。
	// reused=true 表示本文件命中账目、一个字节都没读；resumed=true 表示只读了
	// 增量尾部。两者都为 false 才是整文件从头解析。
	reused  bool
	resumed bool
	// committed 是本轮完整消费到的字节位置（恒在换行符之后）。它是写回账本的
	// offset；下次扫描从这里续读。半行（尾部未闭合的写入）刻意不计入。
	committed int64
	// firstTS / lastTS 是本文件通过预筛且成功解 JSON 的事件时间戳极值，用于
	// 窗口查询的精确整文件跳过。
	firstTS time.Time
	lastTS  time.Time
}

// scanWorkerOverride 是**测试钩子**：置为 1 可把 worker pool 退化成串行执行，
// 用来证明"并发结果与串行结果逐字节一致"（TestAnalyzeParallelMatchesSerial）。
// 生产路径恒为 0（按 NumCPU 定档）。
var scanWorkerOverride int

// scanWorkerCount 决定并发解析的文件数。
//
// 瓶颈是「随机读 + JSON 解码」：一次全量扫描要打开 2000+ 文件、逐行扫 70 万行，
// 既不是纯 CPU 也不是纯网络延迟，所以按 NumCPU 给一档基线，再夹到 [4,16]。
// 下界 4：少于 4 个并发读覆盖不住机械盘/网络盘的寻道延迟；上界 16：再高只会
// 让读队列互相抢占，对机械盘甚至更慢（而本机 16 核的上界正好落在实测甜点）。
func scanWorkerCount(files int) int {
	if scanWorkerOverride > 0 {
		workers := scanWorkerOverride
		if files > 0 && workers > files {
			workers = files
		}
		return workers
	}
	workers := runtime.NumCPU()
	if workers < 4 {
		workers = 4
	}
	if workers > 16 {
		workers = 16
	}
	if files > 0 && workers > files {
		workers = files
	}
	return workers
}

// scanTarget 是一个待解析文件连同它的 stat 事实。size / modTime 在遍历时就取好
// （WalkDir 的 DirEntry.Info 通常直接来自已有目录项，不额外 syscall），供增量
// 索引判定「该复用、该续读、还是该重读」。
type scanTarget struct {
	path    string
	size    int64
	modTime time.Time
}

// scanTargetsParallel 并发解析目标文件，返回与 targets 同序同长的结果切片。
//
// 每个 goroutine 用原子游标领取下标、只写 outcomes[index]，互不覆盖；返回顺序
// 由下标决定而非完成顺序，因此结果与 goroutine 调度无关（可重复、可测试）。
// previous 是本轮开始前加载的账本，**全程只读**：多个 worker 并发查它是安全的；
// 各自的新条目随 scanOutcome 按序带回，由 Analyze 串行汇总后统一落盘。
func scanTargetsParallel(targets []scanTarget, since time.Time, previous *indexFile) []scanOutcome {
	outcomes := make([]scanOutcome, len(targets))
	if len(targets) == 0 {
		return outcomes
	}
	workers := scanWorkerCount(len(targets))
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wg.Done()
			for {
				index := int(next.Add(1)) - 1
				if index >= len(targets) {
					return
				}
				target := targets[index]
				outcomes[index] = scanTargetWithIndex(target, previous.Entries[indexKey(target.path)], since)
			}
		}()
	}
	wg.Wait()
	return outcomes
}

// scanTargetWithIndex 按账本状态挑一条最省的路扫这个文件。三条路径产出的
// scanOutcome 必须完全等价（这是增量索引不改变口径的唯一要求）：
//
//	账目与当前 (size, mtime) 一致 → 不打开文件，直接复用账目里的事实；
//	size 变大且 mtime 变新        → seek 到 offset 只读增量，事实累加到旧账目上；
//	其余（轮转 / 截断 / 无账目） → 整文件从头解析。
func scanTargetWithIndex(target scanTarget, entry *indexEntry, since time.Time) scanOutcome {
	if entry != nil && entry.Size == target.size && entry.ModTime == target.modTime.UnixNano() {
		reused := entry.Facts.toOutcome()
		reused.reused = true
		reused.committed = entry.Offset
		return reused
	}
	var startOffset int64
	var base scanOutcome
	if entry != nil && entry.Offset > 0 && target.size > entry.Offset &&
		target.modTime.UnixNano() >= entry.ModTime {
		startOffset = entry.Offset
		base = entry.Facts.toOutcome()
	}
	return scanFile(target.path, startOffset, base, since, target.size)
}

// collectScanTargets 串行遍历各 root，按 filepath.WalkDir 的自然（字典）顺序收集
// runtime-events.jsonl 的路径与 stat 事实，并在派发给 worker 之前完成三件必须
// 串行的前置工作：
//
//   - 路径去重（filepath.Clean 后小写），多 root 交叠时同一文件只处理一次；
//   - mtime 快速路径：since 非零且文件最后写入早于 since 时整文件跳过；
//   - 账目快速路径：since 非零且账目里最后一条相关事件也早于 since 时整文件跳过。
//
// 后两条都是「可证明该文件不含窗口内事件」的充分条件，故跳过等价于不读。后者
// 比前者更紧（mtime 会被后续 append 刷新，且与事件时间本就无关），两者取并集。
//
// 返回顺序即后续合并顺序，必须保持 WalkDir 的自然顺序：requests 的顺序经由
// bySessionPath 的 sort.SliceStable(ts) 影响 closure 计数。
func collectScanTargets(opts Options, previous *indexFile) ([]scanTarget, int) {
	var targets []scanTarget
	skippedFiles := 0
	seen := map[string]struct{}{}
	for _, root := range opts.Roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() || entry.Name() != "runtime-events.jsonl" {
				return nil
			}
			key := strings.ToLower(filepath.Clean(path))
			if _, exists := seen[key]; exists {
				return nil
			}
			seen[key] = struct{}{}
			info, infoErr := entry.Info()
			if !opts.Since.IsZero() {
				if infoErr == nil && info.ModTime().Before(opts.Since) {
					skippedFiles++
					return nil
				}
				if exactWindowSkip(previous.Entries[indexKey(path)], opts.Since) {
					skippedFiles++
					return nil
				}
			}
			target := scanTarget{path: path}
			if infoErr == nil {
				target.size = info.Size()
				target.modTime = info.ModTime()
			}
			targets = append(targets, target)
			return nil
		})
	}
	return targets, skippedFiles
}

// maxScanLineBytes 是单行长度上限，与改动前 bufio.Scanner.Buffer 的上限一致。
// 超长行让旧 Scanner 报 ErrTooLong 并结束本文件的循环（scanner.Err() 从未被
// 检查，结果被忽略），这里保持同样的终止语义。
const maxScanLineBytes = 4 * 1024 * 1024

// 预筛用的字节常量。走 bytes.Contains 而不是 strings.Contains，是为了在每行
// 解析时不额外分配一份 string（旧实现每行 scanner.Text() 都分配一次）。
var (
	bytesLSPRequest     = []byte(eventLSPRequest)
	bytesToolCompleted  = []byte(eventToolCompleted)
	bytesLSPServerState = []byte(eventLSPServerState)
)

// scanFile 解析单个 runtime-events.jsonl，产出该文件私有的归因事实。
//
// startOffset > 0 时从该字节续读（增量扫描），base 是此前已解析出的事实，
// 本函数在其上**累加**——于是「旧事实 + 增量」与「整文件重解析」严格等价，
// 这正是增量索引不改变口径的依据。
//
// 逐行口径与改动前一致：空行跳过且不计损坏、行数照记、便宜预筛、窗口过滤、
// JSON 损坏计 malformed、单文件读取失败非致命。内存边界：只保留该文件解析出的
// 事实（绝大多数文件是 0 行），逐行扫描不缓冲原始文件内容。
//
// 唯一的口径收紧是**只消费完整行**（以 \n 结尾）。文件正被追加时尾部可能只写了
// 半行，那半行不是一条记录：算进去既可能把撕裂写误记成 malformed，也会让人
// 下一轮从 committed 处续读时把它**再计一次**。半行不消费也不计数，等下一轮
// 写完自然被读到。代价是「恰好在追加瞬间扫描」时会晚一轮看到尾部那行，这与
// 「没写完的日志不该被统计」的本分一致；fixture 与离线脚本都以 \n 结尾，
// baseline_test.go 钉死的数字不受影响。
func scanFile(path string, startOffset int64, base scanOutcome, since time.Time, size int64) scanOutcome {
	outcome := base
	outcome.opened = true
	outcome.reused = false
	outcome.resumed = startOffset > 0
	file, openErr := os.Open(path)
	if openErr != nil {
		return scanOutcome{}
	}
	defer file.Close()
	if startOffset > 0 {
		if _, seekErr := file.Seek(startOffset, io.SeekStart); seekErr != nil {
			return scanOutcome{}
		}
	}
	windowed := !since.IsZero()
	reader := bufio.NewReaderSize(file, 64*1024)
	consumed := startOffset
	var pending []byte
	for {
		chunk, readErr := reader.ReadSlice('\n')
		switch {
		case readErr == nil:
			// 找到换行：这是一条完整记录。
			pending = append(pending, chunk...)
			consumed += int64(len(pending))
			consumeScanLine(pending, &outcome, since, windowed)
			pending = pending[:0]
		case readErr == io.EOF:
			// 尾部没有换行：半行，既不消费也不计数。
			return finishScan(outcome, consumed)
		case readErr == bufio.ErrBufferFull:
			pending = append(pending, chunk...)
			if len(pending) > maxScanLineBytes {
				return finishScan(outcome, consumed)
			}
		default:
			// 真正的读错误：非致命，结束本文件（与旧 Scanner 的忽略错误同策略）。
			return finishScan(outcome, consumed)
		}
	}
}

// finishScan 收尾：记录本轮完整消费到的字节位置。
func finishScan(outcome scanOutcome, consumed int64) scanOutcome {
	outcome.committed = consumed
	return outcome
}

// consumeScanLine 处理一条**完整**的行（chunk 含行尾换行），把它的贡献累加进
// outcome。口径与改动前的循环体逐条一致。
func consumeScanLine(chunk []byte, outcome *scanOutcome, since time.Time, windowed bool) {
	line := bytes.TrimSuffix(chunk, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'}) // 与 bufio.ScanLines 一致
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	outcome.lines++
	// 便宜预筛：只解可能相关的事件行（写入端为紧凑 JSON，但按
	// 「类型值是否出现在行内」判断，兼容美化格式）。
	if !bytes.Contains(line, bytesLSPRequest) && !bytes.Contains(line, bytesToolCompleted) &&
		!bytes.Contains(line, bytesLSPServerState) {
		return
	}
	var event rawEvent
	if unmarshalErr := json.Unmarshal(line, &event); unmarshalErr != nil {
		outcome.malformed++
		return
	}
	// 时间戳极值只统计「过了预筛且解成功」的事件——预筛放行的恰好就是下面这个
	// switch 会消费的三类事件，所以据此判断「整文件都早于窗口」是精确的。
	if !event.Timestamp.IsZero() {
		if outcome.firstTS.IsZero() || event.Timestamp.Before(outcome.firstTS) {
			outcome.firstTS = event.Timestamp
		}
		if outcome.lastTS.IsZero() || event.Timestamp.After(outcome.lastTS) {
			outcome.lastTS = event.Timestamp
		}
	}
	if windowed && (event.Timestamp.IsZero() || event.Timestamp.Before(since)) {
		outcome.skippedOld++
		return
	}
	switch event.Type {
	case eventLSPRequest:
		payload := event.Payload
		day := "unknown"
		if !event.Timestamp.IsZero() {
			day = event.Timestamp.UTC().Format("2006-01-02")
			outcome.timestamps = append(outcome.timestamps, event.Timestamp)
		}
		fact := requestFact{
			sessionID:           strings.TrimSpace(event.SessionID),
			day:                 day,
			trigger:             payloadString(payload, "trigger"),
			outcome:             payloadString(payload, "outcome"),
			durationMS:          payloadInt(payload, "duration_ms"),
			diagCount:           payloadInt(payload, "diag_count"),
			appendedBytes:       payloadInt(payload, "appended_bytes"),
			appendedDiagBytes:   payloadInt(payload, "appended_diag_bytes"),
			appendedNoteBytes:   payloadInt(payload, "appended_note_bytes"),
			appendedEmptyBytes:  payloadInt(payload, "appended_empty_bytes"),
			totalDiagCount:      payloadInt(payload, "total_diag_count"),
			newDiagCount:        payloadInt(payload, "new_diag_count"),
			omittedItems:        payloadInt(payload, "omitted_items"),
			omittedByChars:      payloadInt(payload, "omitted_by_chars"),
			server:              payloadString(payload, "server"),
			ts:                  event.Timestamp,
			pathFingerprint:     payloadString(payload, "path_fingerprint"),
			diagFingerprint:     payloadString(payload, "diag_fingerprint"),
			reasonCategory:      payloadString(payload, "reason_category"),
			coldFastFail:        payloadBool(payload, "cold_fast_fail"),
			coldProbeClassified: payloadHas(payload, "cold_fast_fail"),
			attemptedMembers:    payloadInt(payload, "attempted_members"),
		}
		outcome.requests = append(outcome.requests, fact)
		if fact.sessionID != "" {
			if outcome.sessionRequests == nil {
				outcome.sessionRequests = map[string]int{}
			}
			outcome.sessionRequests[fact.sessionID]++
		}
	case eventToolCompleted:
		tool := payloadString(event.Payload, "logical_tool")
		if _, ok := editingTools[tool]; !ok {
			return
		}
		outcome.editCalls++
		sessionID := strings.TrimSpace(event.SessionID)
		if outcome.editCallsBySession == nil {
			outcome.editCallsBySession = map[string]int{}
		}
		outcome.editCallsBySession[sessionID]++
		outputBytes := payloadInt(event.Payload, "output_model_visible_bytes")
		if outputBytes <= 0 {
			outputBytes = payloadInt(event.Payload, "output_original_bytes")
		}
		if outputBytes > 0 {
			outcome.editOutput += outputBytes
			outcome.editOutputEvents++
			if outcome.editOutputBySession == nil {
				outcome.editOutputBySession = map[string]int{}
			}
			outcome.editOutputBySession[sessionID] += outputBytes
		}
	case eventLSPServerState:
		firstPublishMS := payloadInt(event.Payload, "first_publish_ms")
		if firstPublishMS <= 0 {
			return
		}
		// 同一 (session, server) 只取最早一条：首个发布即该连接的
		// 冷启动延迟；同会话内重启会再发一条，取首次即可。
		key := strings.TrimSpace(event.SessionID) + "\x00" + payloadString(event.Payload, "server")
		if outcome.coldFirst == nil {
			outcome.coldFirst = map[string]coldFact{}
		}
		if prev, ok := outcome.coldFirst[key]; !ok || event.Timestamp.Before(prev.ts) {
			outcome.coldFirst[key] = coldFact{ts: event.Timestamp, ms: firstPublishMS}
		}
	}
}

// Analyze 扫描日志并聚合。任何单个文件/行的读取问题都不中断整次统计。
func Analyze(opts Options) (Stats, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	facts := NewFacts()
	scan := ScanStats{}

	// 账本先加载一次，全程只读；本轮产生的新条目在合并之后串行汇总、原子落盘。
	previous := loadIndex()

	// 扫描拆成三段，口径不变，只是把「读盘 + 解 JSON」搬到并发执行：
	//
	//  1. collectScanTargets：仍串行 WalkDir，按自然顺序收集目标并完成路径去重
	//     与两条整文件快速路径。它输出的顺序是下游 requests/timestamps 的唯一
	//     顺序来源。
	//  2. scanTargetsParallel：有界 worker pool 并发解析文件，每格只写自己的
	//     scanOutcome（无共享累加器、无锁）。
	//  3. 下面的串行归并：严格按第 1 段的顺序拼接每份文件事实。
	//
	// 第 3 步是「输出口径不变」的关键：requests 的顺序会经 bySessionPath 的
	// sort.SliceStable(ts) 影响 closure 计数（时间戳并列时保留原序），而
	// baseline_test.go 把这组数字钉死了。并发只发生在结果按下标写回、彼此不覆盖
	// 的阶段，合并顺序恒等于遍历顺序，故输出与串行版逐字节一致、与调度无关。
	// 增量索引同样不参与排序：它只决定「每个文件的事实从哪来」，不改变事实本身。
	targets, skippedFiles := collectScanTargets(opts, previous)
	scan.SkippedFiles = skippedFiles
	outcomes := scanTargetsParallel(targets, opts.Since, previous)
	// 只有全窗口扫描产出的是「与窗口无关」的事实，可以入账。窗口查询的
	// skippedOld 是按 since 逐行过滤出来的，混进账目会让下次全窗口扫描复用到
	// 被窗口裁剪过的数字——那是静默的口径漂移，必须避免。
	next := emptyIndex()
	for position, target := range targets {
		outcome := outcomes[position]
		if !outcome.opened {
			continue
		}
		if outcome.reused {
			scan.ReusedFiles++
		} else if outcome.resumed {
			scan.DeltaFiles++
		}
		if opts.Since.IsZero() && outcome.committed > 0 {
			next.Entries[indexKey(target.path)] = &indexEntry{
				Path:    target.path,
				Size:    target.size,
				ModTime: target.modTime.UnixNano(),
				Offset:  outcome.committed,
				FirstTS: unixNanoOrZero(outcome.firstTS),
				LastTS:  unixNanoOrZero(outcome.lastTS),
				Facts:   newFactRecord(outcome),
			}
		}
	}
	if opts.Since.IsZero() {
		carryForwardIndexEntries(previous, next)
		scan.IndexedFiles = len(next.Entries)
		saveIndex(next)
	}
	for _, outcome := range outcomes {
		if !outcome.opened {
			continue
		}
		// 打不开的文件不计入 Scan.Files（与串行版一致），且不中断整次统计。
		scan.Files++
		scan.Lines += outcome.lines
		scan.Malformed += outcome.malformed
		scan.SkippedOld += outcome.skippedOld
		for _, fact := range outcome.requests {
			facts.AddFact(FactInput{
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
		facts.AddEditStats(
			outcome.editCalls, outcome.editOutput, outcome.editOutputEvents,
			outcome.editCallsBySession, outcome.editOutputBySession)
		for key, fact := range outcome.coldFirst {
			facts.PutColdFirst(key, fact.ts, fact.ms)
		}
	}
	facts.SetScan(scan)
	return Aggregate(facts, now), nil
}

// Percentile 与离线脚本/运行期 Metrics 同口径：ceil(p*n) 的 1-based 序号。
func Percentile(sortedValues []int, fraction float64) *int {
	if len(sortedValues) == 0 {
		return nil
	}
	index := int(math.Ceil(fraction*float64(len(sortedValues)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sortedValues) {
		index = len(sortedValues) - 1
	}
	value := sortedValues[index]
	return &value
}

func payloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func payloadInt(payload map[string]interface{}, key string) int {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	default:
		return 0
	}
}

// payloadBool 解析布尔载荷（JSON true/false 或字符串 "true"）。
func payloadBool(payload map[string]interface{}, key string) bool {
	if payload == nil {
		return false
	}
	switch value := payload[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

// payloadHas 报告键是否存在（区分"字段缺失"与"显式 false"）。
func payloadHas(payload map[string]interface{}, key string) bool {
	if payload == nil {
		return false
	}
	_, ok := payload[key]
	return ok
}

// ratioText 渲染比值；分母为 0 时按反模式纪律输出 n/a 与原因。
func ratioText(numerator, denominator int, reason string) string {
	if denominator == 0 {
		return "n/a（" + reason + "）"
	}
	return fmt.Sprintf("%.4f", float64(numerator)/float64(denominator))
}
