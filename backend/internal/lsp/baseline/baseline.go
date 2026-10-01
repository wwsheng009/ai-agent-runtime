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
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	// SkippedFiles 是按文件修改时间整文件跳过的数量（窗口过滤的快速路径：
	// 文件最后写入不晚于 since 时，其全部事件必然早于 since）。
	SkippedFiles int `json:"skipped_files"`
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

// Analyze 扫描日志并聚合。任何单个文件/行的读取问题都不中断整次统计。
func Analyze(opts Options) (Stats, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	stats := Stats{
		Triggers: map[string]int{},
		Outcomes: map[string]int{},
		Servers:  map[string]int{},
		ByDay:    map[string]*DayBucket{},
	}
	editCallsBySession := map[string]int{}
	editOutputBySession := map[string]int{}
	sessionRequests := map[string]int{}
	type coldFact struct {
		ts time.Time
		ms int
	}
	coldFirst := map[string]coldFact{}
	var timestamps []time.Time
	seen := map[string]struct{}{}

	type requestFact struct {
		sessionID       string
		day             string
		trigger         string
		outcome         string
		durationMS      int
		diagCount       int
		appendedBytes   int
		omittedItems    int
		omittedByChars  int
		server          string
		ts              time.Time
		pathFingerprint string
		diagFingerprint string
		reasonCategory  string
	}
	var requests []requestFact

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
			if !opts.Since.IsZero() {
				if info, infoErr := entry.Info(); infoErr == nil && info.ModTime().Before(opts.Since) {
					stats.Scan.SkippedFiles++
					return nil
				}
			}
			file, openErr := os.Open(path)
			if openErr != nil {
				return nil
			}
			defer file.Close()
			stats.Scan.Files++
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.TrimSpace(line) == "" {
					continue
				}
				stats.Scan.Lines++
				// 便宜预筛：只解可能相关的事件行（写入端为紧凑 JSON，但按
				// 「类型值是否出现在行内」判断，兼容美化格式）。
				if !strings.Contains(line, eventLSPRequest) && !strings.Contains(line, eventToolCompleted) &&
					!strings.Contains(line, eventLSPServerState) {
					continue
				}
				var event rawEvent
				if unmarshalErr := json.Unmarshal([]byte(line), &event); unmarshalErr != nil {
					stats.Scan.Malformed++
					continue
				}
				if !opts.Since.IsZero() && (event.Timestamp.IsZero() || event.Timestamp.Before(opts.Since)) {
					stats.Scan.SkippedOld++
					continue
				}
				switch event.Type {
				case eventLSPRequest:
					payload := event.Payload
					day := "unknown"
					if !event.Timestamp.IsZero() {
						day = event.Timestamp.UTC().Format("2006-01-02")
						timestamps = append(timestamps, event.Timestamp)
					}
					fact := requestFact{
						sessionID:       strings.TrimSpace(event.SessionID),
						day:             day,
						trigger:         payloadString(payload, "trigger"),
						outcome:         payloadString(payload, "outcome"),
						durationMS:      payloadInt(payload, "duration_ms"),
						diagCount:       payloadInt(payload, "diag_count"),
						appendedBytes:   payloadInt(payload, "appended_bytes"),
						omittedItems:    payloadInt(payload, "omitted_items"),
						omittedByChars:  payloadInt(payload, "omitted_by_chars"),
						server:          payloadString(payload, "server"),
						ts:              event.Timestamp,
						pathFingerprint: payloadString(payload, "path_fingerprint"),
						diagFingerprint: payloadString(payload, "diag_fingerprint"),
						reasonCategory:  payloadString(payload, "reason_category"),
					}
					requests = append(requests, fact)
					if fact.sessionID != "" {
						sessionRequests[fact.sessionID]++
					}
				case eventToolCompleted:
					tool := payloadString(event.Payload, "logical_tool")
					if _, ok := editingTools[tool]; !ok {
						continue
					}
					stats.EditCalls++
					sessionID := strings.TrimSpace(event.SessionID)
					editCallsBySession[sessionID]++
					outputBytes := payloadInt(event.Payload, "output_model_visible_bytes")
					if outputBytes <= 0 {
						outputBytes = payloadInt(event.Payload, "output_original_bytes")
					}
					if outputBytes > 0 {
						stats.EditOutput += outputBytes
						stats.EditOutputEvs++
						editOutputBySession[sessionID] += outputBytes
					}
				case eventLSPServerState:
					firstPublishMS := payloadInt(event.Payload, "first_publish_ms")
					if firstPublishMS <= 0 {
						continue
					}
					// 同一 (session, server) 只取最早一条：首个发布即该连接的
					// 冷启动延迟；同会话内重启会再发一条，取首次即可。
					key := strings.TrimSpace(event.SessionID) + "\x00" + payloadString(event.Payload, "server")
					if prev, ok := coldFirst[key]; !ok || event.Timestamp.Before(prev.ts) {
						coldFirst[key] = coldFact{ts: event.Timestamp, ms: firstPublishMS}
					}
				}
			}
			return nil
		})
	}

	stats.Requests = len(requests)
	stats.Sessions = len(sessionRequests)
	for _, fact := range requests {
		stats.Triggers[fact.trigger]++
		stats.Outcomes[fact.outcome]++
		if fact.server != "" {
			stats.Servers[fact.server]++
		}
		stats.AppendedBytes += fact.appendedBytes
		stats.DiagCount += fact.diagCount
		if fact.omittedItems > 0 || fact.omittedByChars > 0 {
			stats.Truncated++
		}
		stats.OmittedItems += fact.omittedItems
		stats.OmittedByChars += fact.omittedByChars
		stats.durations = append(stats.durations, fact.durationMS)
		if fact.outcome == "injected" {
			stats.Injected++
			if fact.diagCount > 0 {
				stats.DiagHit++
			}
		}
		if fact.outcome == "clean" {
			stats.Clean++
		}
		if fact.outcome == "no_server" {
			stats.NoServer++
		}
		if strings.HasPrefix(fact.outcome, "degraded") {
			stats.Degraded++
		}
		if fact.reasonCategory != "" {
			if stats.DegradeReasons == nil {
				stats.DegradeReasons = map[string]int{}
			}
			stats.DegradeReasons[fact.reasonCategory]++
		}
		bucket := stats.ByDay[fact.day]
		if bucket == nil {
			bucket = &DayBucket{}
			stats.ByDay[fact.day] = bucket
		}
		bucket.Requests++
		bucket.Durations = append(bucket.Durations, fact.durationMS)
		if fact.outcome == "injected" {
			bucket.Injected++
		}
		if strings.HasPrefix(fact.outcome, "degraded") {
			bucket.Degraded++
		}
	}
	sort.Ints(stats.durations)
	stats.LatencyP50MS = Percentile(stats.durations, 0.50)
	stats.LatencyP95MS = Percentile(stats.durations, 0.95)
	if len(timestamps) > 0 {
		sort.Slice(timestamps, func(i, j int) bool { return timestamps[i].Before(timestamps[j]) })
		stats.FirstAt = timestamps[0].UTC().Format(time.RFC3339)
		stats.LastAt = timestamps[len(timestamps)-1].UTC().Format(time.RFC3339)
	}
	for sessionID, count := range editCallsBySession {
		if _, active := sessionRequests[sessionID]; active {
			stats.ActiveEdit += count
		}
	}
	for sessionID, total := range editOutputBySession {
		if _, active := sessionRequests[sessionID]; active {
			stats.ActiveOutput += total
		}
	}
	for _, fact := range coldFirst {
		stats.coldStarts = append(stats.coldStarts, fact.ms)
	}
	sort.Ints(stats.coldStarts)
	stats.ColdFirstPublishP50MS = Percentile(stats.coldStarts, 0.50)
	stats.ColdFirstPublishP95MS = Percentile(stats.coldStarts, 0.95)
	// lsp_closure_ratio（plan §3.3）：同一会话内，被注入的诊断集合是否在下一次
	// 同文件编辑后消失（下一次请求 outcome=clean）。只统计带 fingerprint 的
	// injected 请求；无 eligible 样本输出 n/a（不把未采集渲染成 0）。
	bySessionPath := map[string][]requestFact{}
	for _, fact := range requests {
		if fact.sessionID == "" || fact.pathFingerprint == "" {
			continue
		}
		key := fact.sessionID + "\x00" + fact.pathFingerprint
		bySessionPath[key] = append(bySessionPath[key], fact)
	}
	for _, facts := range bySessionPath {
		sort.SliceStable(facts, func(i, j int) bool { return facts[i].ts.Before(facts[j].ts) })
		for index, fact := range facts {
			if fact.outcome != "injected" || fact.diagFingerprint == "" {
				continue
			}
			stats.ClosureEligible++
			if index+1 < len(facts) && facts[index+1].outcome == "clean" {
				stats.ClosureClosed++
			}
		}
	}
	for _, bucket := range stats.ByDay {
		sort.Ints(bucket.Durations)
		bucket.P95MS = Percentile(bucket.Durations, 0.95)
	}
	stats.GeneratedAt = now.UTC().Format(time.RFC3339)
	return stats, nil
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

// ratioText 渲染比值；分母为 0 时按反模式纪律输出 n/a 与原因。
func ratioText(numerator, denominator int, reason string) string {
	if denominator == 0 {
		return "n/a（" + reason + "）"
	}
	return fmt.Sprintf("%.4f", float64(numerator)/float64(denominator))
}
