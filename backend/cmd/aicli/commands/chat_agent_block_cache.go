package commands

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// chatAgentBlockCacheTTL 是 agents 区块快照的保鲜期。
//
// 该区块的三项数据都要读会话库（连接池恒为单连接，且与后台 reconciler 争用）：
// registry 行 + 一致性审计、agent graph、mailbox。实测争用时单次采集阻塞数秒
// （见 docs/e2e/debug-guide-evidence.md §4），所以轮询型 HTTP 快照一律走「缓存 + 后台
// 刷新」：读取方永远立即拿到上一份样本及其年龄，冷启动显式呈现 collecting，
// 而不是把诊断变成一次同步排队。交互面板（/debug display，零值选项）保留同步
// 直读——人工排查要的正是当场那一份。
const chatAgentBlockCacheTTL = 5 * time.Second

// chatAgentBlockSample 是一次采集的原始结果。年龄在读取时计算：把它冻结进
// 缓存会让「stale」永远显示成采集那一刻的值。
type chatAgentBlockSample struct {
	RegistryLine string
	Audit        chatAgentRegistryAudit
	AuditTail    []string // issue 行 + 对账/回收摘要（不含 age）
	GraphItems   []chatDebugDisplayAgentInfo
	GraphLines   []string
	MailboxLines []string
	CollectedAt  time.Time
}

// chatAgentBlockSnapshot 是一次缓存读取的结果。Age < 0 表示尚无样本。
type chatAgentBlockSnapshot struct {
	Sample chatAgentBlockSample
	Age    time.Duration
	Stale  bool
}

// Collecting 报告区块是否还没有任何样本（首次采集在途）。
func (s chatAgentBlockSnapshot) Collecting() bool { return s.Age < 0 }

// AgeLabel 渲染样本年龄；过期样本额外标注 refreshing，采集未完成时标注
// collecting。消费者据此区分「这份是新的」「这份是旧的」「这份还没有」。
func (s chatAgentBlockSnapshot) AgeLabel() string {
	if s.Collecting() {
		return "collecting"
	}
	label := chatAgentBlockAgeLabel(s.Age)
	if s.Stale {
		label += " refreshing"
	}
	return label
}

// ConsistencyLines 按「样本 + 年龄」渲染一致性行；与同步路径
// （chatAgentControlConsistencyLines）逐字对齐，只多一个 age= 标注。
func (s chatAgentBlockSnapshot) ConsistencyLines() []string {
	if s.Collecting() {
		return []string{"  consistency=collecting (first sample in flight)"}
	}
	sample := s.Sample
	if !sample.Audit.Attempted {
		return []string{"  consistency=<unavailable>"}
	}
	if sample.Audit.Err != nil {
		return []string{"  consistency=<error: " + sample.Audit.Err.Error() + "> (age=" + chatAgentBlockAgeLabel(s.Age) + ")"}
	}
	report := sample.Audit.Report
	lines := []string{fmt.Sprintf("  consistency records=%d active=%d issues=%d age=%s",
		report.RecordsChecked, report.ActiveChecked, report.IssueCount, chatAgentBlockAgeLabel(s.Age))}
	return append(lines, sample.AuditTail...)
}

// DocumentLines 渲染文本面板/文本快照用的四段行；采集未完成时显式标注，避免
// 读者把「还没采集」当成「没有 agent」。
func (s chatAgentBlockSnapshot) DocumentLines() (registry string, consistency, graph, mailbox []string) {
	if s.Collecting() {
		return "  <collecting>",
			[]string{"  consistency=collecting (first sample in flight)"},
			[]string{"  <collecting>"},
			[]string{"  <collecting>"}
	}
	return s.Sample.RegistryLine, s.ConsistencyLines(), s.Sample.GraphLines, s.Sample.MailboxLines
}

func chatAgentBlockAgeLabel(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	return fmt.Sprintf("%.1fs", age.Seconds())
}

type chatAgentBlockCacheEntry struct {
	session  *ChatSession
	sample   chatAgentBlockSample
	at       time.Time
	inflight bool
}

var (
	chatAgentBlockCacheMu sync.Mutex
	// chatAgentBlockCached 是当前会话的样本槽（同一时刻只有一条会话）。
	chatAgentBlockCached *chatAgentBlockCacheEntry

	// chatAgentBlockCollectOverride 仅供测试注入采集行为（nil = 真实采集）。
	chatAgentBlockCollectOverride func(*ChatSession) chatAgentBlockSample
)

// chatAgentBlockSnapshotFor 读取 agents 区块快照，永不阻塞：命中即返回，过期
// 样本照常返回（由 AgeLabel 标注）并把刷新放到后台，冷启动只启动采集。
func chatAgentBlockSnapshotFor(session *ChatSession) chatAgentBlockSnapshot {
	if session == nil {
		return chatAgentBlockSnapshot{Age: -1}
	}
	now := time.Now()
	chatAgentBlockCacheMu.Lock()
	entry := chatAgentBlockCached
	if entry != nil && entry.session != session {
		// 会话已切换：旧样本对新会话没有证据力。
		entry = nil
	}
	if entry == nil {
		entry = &chatAgentBlockCacheEntry{session: session}
		chatAgentBlockCached = entry
	}
	if entry.at.IsZero() {
		chatAgentBlockStartLocked(entry)
		chatAgentBlockCacheMu.Unlock()
		return chatAgentBlockSnapshot{Age: -1}
	}
	snap := chatAgentBlockSnapshot{Sample: entry.sample, Age: now.Sub(entry.at)}
	snap.Stale = snap.Age >= chatAgentBlockCacheTTL
	if snap.Stale {
		chatAgentBlockStartLocked(entry)
	}
	chatAgentBlockCacheMu.Unlock()
	return snap
}

// chatAgentBlockStartLocked 启动一次后台采集（单飞：同一时刻最多一个在途，
// 采集卡住也不会堆积成风暴）。调用方必须持有 chatAgentBlockCacheMu。
func chatAgentBlockStartLocked(entry *chatAgentBlockCacheEntry) {
	if entry.inflight {
		return
	}
	entry.inflight = true
	session := entry.session
	collect := chatAgentBlockCollectOverride
	go func() {
		sample := collectChatAgentBlock(session, collect)
		if sample.CollectedAt.IsZero() {
			// 时间戳是缓存新鲜度的唯一依据：零值会让样本永远显得「冷」。
			sample.CollectedAt = time.Now()
		}
		chatAgentBlockCacheMu.Lock()
		entry.sample = sample
		entry.at = sample.CollectedAt
		entry.inflight = false
		chatAgentBlockCacheMu.Unlock()
	}()
}

// setChatAgentBlockCollectOverride 注入采集行为（仅供测试；nil = 真实采集）。
// 走同一把锁，避免与后台采集 goroutine 的读取构成数据竞争。
func setChatAgentBlockCollectOverride(collect func(*ChatSession) chatAgentBlockSample) {
	chatAgentBlockCacheMu.Lock()
	chatAgentBlockCollectOverride = collect
	chatAgentBlockCacheMu.Unlock()
}

// collectChatAgentBlock 采集整个 agents 区块。这里的读仍可能阻塞（会话库单
// 连接），但阻塞的是后台刷新而不是请求。collect 为 nil 时走真实采集。
func collectChatAgentBlock(session *ChatSession, collect func(*ChatSession) chatAgentBlockSample) chatAgentBlockSample {
	if collect != nil {
		return collect(session)
	}
	sample := chatAgentBlockSample{CollectedAt: time.Now()}
	if session == nil {
		return sample
	}
	// 审计只做一次，registry 行与一致性行共用（单次采集只读一次会话库）。
	audit := chatAgentRegistryAuditFor(session)
	sample.Audit = audit
	sample.RegistryLine = strings.TrimSpace(chatAgentPanelRegistryLineWithAudit(session, audit))
	sample.MailboxLines = chatDebugMailboxLines(session)
	sample.GraphLines = chatAgentGraphLines(session)
	if agents, err := chatAgentGraphItems(session); err == nil {
		sample.GraphItems = make([]chatDebugDisplayAgentInfo, 0, len(agents))
		for _, agent := range agents {
			sample.GraphItems = append(sample.GraphItems, chatDebugDisplayAgentInfo{
				Path:            firstNonEmptyChatValue(agent.Path, agent.SessionID, agent.ID),
				Status:          firstNonEmptyChatValue(agent.Status, "unknown"),
				SessionID:       firstNonEmptyChatValue(agent.SessionID, agent.ID),
				SessionState:    agent.SessionState,
				Parent:          agent.ParentSessionID,
				Depth:           agent.Depth,
				AgentType:       agent.AgentType,
				TeamID:          agent.TeamID,
				PendingApproval: agent.PendingApproval,
				PendingQuestion: agent.PendingQuestion,
				PendingTool:     agent.PendingToolName,
			})
		}
	}
	if audit.Attempted {
		sample.AuditTail = chatAgentConsistencyTailLines(session)
	}
	return sample
}

// resetChatAgentBlockCache 清空缓存（测试用，避免用例之间互相污染）。
func resetChatAgentBlockCache() {
	chatAgentBlockCacheMu.Lock()
	chatAgentBlockCached = nil
	chatAgentBlockCacheMu.Unlock()
}
