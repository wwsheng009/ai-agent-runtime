package runtimeapi

// 舰队级 LSP 基线端点（方案 §5.5 比率区块 / §3.3 口径）：
//
//	GET /api/runtime/analytics/lsp/baseline?days=N | ?since=RFC3339
//
// 口径：**直接复用 internal/lsp/baseline.Analyze**——与 TUI `/lsp baseline`、
// 方案 §4.3 基线登记表、scripts/analyze-lsp-baseline.py 是同一实现（两侧由同一
// fixture 数字互锁），因此不存在第二套数字。未采集项按基线约定输出 n/a + 原因，
// 不伪造 0；阈值一律"待标定"，本端点不做告警。
//
// 代价与边界：Analyze 是纯函数，但每次实打实要重扫整个 chat-logs 根（本机实测
// ~2.3k 文件 / 70 万行），因此这里加一层**进程内 TTL 缓存 + 在途请求合并**
// （singleflight 等价物），让面板刷新/切页/重复点击不必每次都付秒级扫描。
// 缓存不改变数字，只改变"多久重算一次"——并且必须在响应里**如实**标出
// cache.hit / cache.age_seconds / cache.ttl_seconds：本仓库有明确的
// "不伪造新鲜度"纪律，缓存报表不能假装是刚算出来的。

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp/baseline"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp/baselinesql"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

const lspBaselineSchemaVersion = "runtime.analytics.lsp.v1"

// schema 版本说明：本次新增的 cache 字段是**可选的只读附加信息**
// （hit / age_seconds / ttl_seconds），既有字段的语义与取值一律未变，
// 老客户端忽略它即可继续解析。因此 lspBaselineSchemaVersion 保持
// "runtime.analytics.lsp.v1" 不动——只有删改既有字段或改变既有数字口径
// 才构成破坏性变更、才需要升版。

// lspBaselineCacheTTL 是端点层缓存的存活时间，取 60s。
//
// 取舍：TTL 太短则命中率塌陷（面板一次会话往往连续多次 GET）；太长则基线报表
// 失去实时性。60s 让"一轮面板交互"基本全命中，同时把窗口内新落盘事件的可见
// 延迟压在 1 分钟内——对一份看分布与比率（覆盖率/追加比/回落比）的基线报表够用，
// 它本来就不是秒级告警源。缓存值会在响应里显式告知命中与年龄，不做静默复用。
const lspBaselineCacheTTL = 60 * time.Second

// lspBaselineCacheMaxEntries 限制缓存条目数。键是 (roots × window) 组合，
// 理论上可被任意 since/days 组合撑爆，因此按最近写入时间裁剪到固定上限；
// 内存边界优先于极端命中率。
const lspBaselineCacheMaxEntries = 16

// lspBaselineRoots 是扫描根来源；生产固定为默认 chat-logs 根，测试可覆盖。
var lspBaselineRoots = func() []string {
	return []string{aiclipaths.DefaultChatLogsDir()}
}

// lspBaselineCacheInfo 是响应里的缓存可观测性字段。
//
// 语义（避免"缓存假装新鲜"）：
//   - Hit=false：本次请求**自己**发起了一次全量扫描，AgeSeconds 必为 0；
//   - Hit=true 且 AgeSeconds>0：数字来自 TTL 内的缓存条目，AgeSeconds 是该条目
//     的真实年龄（秒，向下取整）；
//   - Hit=true 且 AgeSeconds=0：数字来自**在途合并**（本请求复用了另一个并发
//     请求正在跑的扫描），它们确实是刚算出来的，所以年龄是 0 而不是 TTL。
type lspBaselineCacheInfo struct {
	Hit        bool `json:"hit"`
	AgeSeconds int  `json:"age_seconds"`
	TTLSeconds int  `json:"ttl_seconds"`
}

type lspBaselinePayload struct {
	SchemaVersion string `json:"schema_version"`
	GeneratedAt   string `json:"generated_at"`
	Window        string `json:"window"`
	// Source 声明本次读数的**事实源**。
	//
	// 为什么必须由后端声明：数据面读分析库、不扫日志，于是 scan.* 恒为 0。
	// 前端若只能看到 0，就无法区分"扫了但没数据"和"根本没扫"，只能渲染成
	// "未使用索引"之类的话——那是误导。让后端自己说，UI 才不用猜。
	Source string               `json:"source"`
	Scan   baseline.ScanStats   `json:"scan"`
	Stats  baseline.Stats       `json:"stats"`
	Rows   []baseline.Row       `json:"rows"`
	Cache  lspBaselineCacheInfo `json:"cache"`
}

// lspBaselineSourceAnalyticsDB 是数据面当前的事实源标识。
// "chat_logs" 保留给未来的回退路径：真回退了，UI 会自动改口，
// 而不需要前端跟着改。
const lspBaselineSourceAnalyticsDB = "analytics_db"

// lspBaselineCacheEntry 是一次扫描的结果 + 它的真实产出时刻。
// GeneratedAt 来自 stats 本身（Analyze 用注入时钟算出），因此它就是"这组数字
// 是什么时候算的"的权威来源，缓存不会另造一个时间戳。
type lspBaselineCacheEntry struct {
	stats  baseline.Stats
	window string
	// producedAt 是条目写入缓存的时刻，用于算 age 与裁剪。
	producedAt time.Time
}

// lspBaselineFlight 是一次在途扫描：让并发同键请求只触发一次 Analyze。
type lspBaselineFlight struct {
	done  chan struct{}
	entry lspBaselineCacheEntry
	err   error
}

// lspBaselineCache 是进程级缓存 + 在途合并表。
//
// 两张表共用一把锁：条目表负责 TTL 命中，在途表负责把并发的同键请求收敛成一次
// 扫描。临界区里只做 map 读写（不跑 Analyze），所以锁不会把秒级扫描串行化。
var lspBaselineCache = struct {
	mu      sync.Mutex
	entries map[string]lspBaselineCacheEntry
	flights map[string]*lspBaselineFlight
}{
	entries: map[string]lspBaselineCacheEntry{},
	flights: map[string]*lspBaselineFlight{},
}

// lspBaselineCacheKey 用「roots + 归一化窗口」而不是「解析后的 Since 绝对时刻」
// 做键。
//
// 为什么不用 opts.Since：?days=N 每次请求都会算出 now-N 的**新**绝对时刻，若拿它
// 做键，任何带 days 的请求都必然 miss，缓存形同虚设。window 字符串（"all" /
// "days=N" / "since=RFC3339"）才是调用方真正表达的意图。
//
// roots 曾经是键的一部分（那时事实源是 chat-logs 目录）。现在事实源是分析库，
// roots 不再影响任何数字，故用 dbPath 取代它 —— 键必须刻画**事实源身份**：同一个
// 库路径 + 同一个窗口 = 同一份数字；换了库就是另一份，绝不能互相顶。
// 只留 window 是不够的：进程级缓存会让两个不同库的结果串味。
func lspBaselineCacheKey(dbPath, window string) string {
	return strings.ToLower(strings.TrimSpace(dbPath)) + "\x01" + window
}

// loadLSPBaselineStats 返回（扫描结果, 缓存可观测性, error）。
//
// 三条路径，语义都在响应里如实区分：
//  1. TTL 命中：直接复用条目，不跑扫描，AgeSeconds = 条目真实年龄；
//  2. 在途合并：已有同键请求在扫描，等待它完成后共享同一份结果（一次扫描服务
//     多个并发 GET），AgeSeconds = 0——数字确实是刚算的；
//  3. miss：登记在途后自己跑 Analyze，完成写入缓存并唤醒所有等待者。
func loadLSPBaselineStats(store *usageanalytics.Store, opts baselinesql.Options, window string) (baseline.Stats, lspBaselineCacheInfo, error) {
	var dbPath string
	if store != nil {
		dbPath = store.Path()
	}
	key := lspBaselineCacheKey(dbPath, window)
	info := lspBaselineCacheInfo{TTLSeconds: int(lspBaselineCacheTTL / time.Second)}

	lspBaselineCache.mu.Lock()
	if entry, ok := lspBaselineCache.entries[key]; ok {
		age := time.Since(entry.producedAt)
		if age < lspBaselineCacheTTL {
			lspBaselineCache.mu.Unlock()
			info.Hit = true
			info.AgeSeconds = int(age / time.Second)
			return entry.stats, info, nil
		}
		delete(lspBaselineCache.entries, key)
	}
	if flight, ok := lspBaselineCache.flights[key]; ok {
		// 在途合并：等它算完（锁外等待，绝不持锁跑秒级扫描）。
		lspBaselineCache.mu.Unlock()
		<-flight.done
		if flight.err != nil {
			return baseline.Stats{}, info, flight.err
		}
		info.Hit = true
		return flight.entry.stats, info, nil
	}
	flight := &lspBaselineFlight{done: make(chan struct{})}
	lspBaselineCache.flights[key] = flight
	lspBaselineCache.mu.Unlock()

	stats, err := baselinesql.Analyze(store, opts)

	entry := lspBaselineCacheEntry{stats: stats, window: window, producedAt: time.Now()}
	lspBaselineCache.mu.Lock()
	flight.entry = entry
	flight.err = err
	if err == nil {
		lspBaselineCache.entries[key] = entry
		lspBaselineEvictLocked()
	}
	delete(lspBaselineCache.flights, key)
	// close 之前先写好 entry/err：等待方 <-done 后即可安全读取，无需再加锁。
	close(flight.done)
	lspBaselineCache.mu.Unlock()

	return stats, info, err
}

// lspBaselineEvictLocked 按 producedAt 从旧到新裁剪到上限；调用方必须已持锁。
func lspBaselineEvictLocked() {
	for len(lspBaselineCache.entries) > lspBaselineCacheMaxEntries {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range lspBaselineCache.entries {
			if oldestKey == "" || entry.producedAt.Before(oldest) {
				oldestKey, oldest = key, entry.producedAt
			}
		}
		delete(lspBaselineCache.entries, oldestKey)
	}
}

// GetAnalyticsLSPBaseline 返回舰队级 LSP 基线（§4.3 行 + 原始 Stats）。
func (h *Handler) GetAnalyticsLSPBaseline(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	opts, window, err := parseLSPBaselineOptions(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	// 事实源：分析库。原先这里回扫 chat-logs（本机实测 ~2.3k 文件 / 70 万行），
	// 而那份日志有保留策略、会被清理，所以以它为事实源意味着数字随轮转无声退化。
	// 口径没变——两条源共用 baseline.Aggregate，equivalence_test.go 钉住逐字段一致。
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	stats, cacheInfo, err := loadLSPBaselineStats(
		service.Store(), baselinesql.Options{Since: opts.Since}, window)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, lspBaselinePayload{
		SchemaVersion: lspBaselineSchemaVersion,
		GeneratedAt:   stats.GeneratedAt,
		Window:        window,
		Source:        lspBaselineSourceAnalyticsDB,
		Scan:          stats.Scan,
		Stats:         stats,
		Rows:          baseline.Rows(stats),
		Cache:         cacheInfo,
	})
}

// parseLSPBaselineOptions 解析时间窗（since 与 days 互斥；缺省 = 全窗口）。
func parseLSPBaselineOptions(r *http.Request) (baseline.Options, string, error) {
	var opts baseline.Options
	values := r.URL.Query()
	sinceRaw := strings.TrimSpace(values.Get("since"))
	daysRaw := strings.TrimSpace(values.Get("days"))
	if sinceRaw != "" && daysRaw != "" {
		return opts, "", fmt.Errorf("since and days are mutually exclusive")
	}
	if sinceRaw != "" {
		t, err := time.Parse(time.RFC3339, sinceRaw)
		if err != nil {
			return opts, "", fmt.Errorf("since must be RFC3339")
		}
		opts.Since = t
		return opts, "since=" + t.UTC().Format(time.RFC3339), nil
	}
	if daysRaw != "" {
		days, err := strconv.Atoi(daysRaw)
		if err != nil || days <= 0 || days > 3650 {
			return opts, "", fmt.Errorf("days must be a positive integer (<=3650)")
		}
		opts.Since = time.Now().AddDate(0, 0, -days)
		return opts, fmt.Sprintf("days=%d", days), nil
	}
	return opts, "all", nil
}
