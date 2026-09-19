package commands

import (
	"encoding/json"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// ============================================================================
// 渲染/显示状态 HTTP 快照（/debug/chat/status）
//
// 模式：与 chatDebugPprofProvider 一致，commands 包通过注册函数暴露
// 当前活动会话 provider，main 包在启动 HTTP 服务器时注册端点，端点
// 每次请求时回调此 provider 获取最新会话快照。
// ============================================================================

// chatDebugDisplaySessionProvider 返回当前活动 ChatSession；nil 表示无会话。
// 在会话创建后（bootstrapChatSession）注册，会话结束时（runExitCleanup）注销。
var chatDebugDisplaySessionProvider = func() *ChatSession { return nil }

// RegisterChatDebugDisplayProvider 注册当前活动会话的取值函数。
// provider 返回当前 ChatSession（nil 表示无会话）；传入 nil 时静默忽略。
func RegisterChatDebugDisplayProvider(provider func() *ChatSession) {
	if provider != nil {
		chatDebugDisplaySessionProvider = provider
	}
}

// chatDebugDisplaySession 返回当前活动会话；无会话时返回 nil。
func chatDebugDisplaySession() *ChatSession {
	if chatDebugDisplaySessionProvider == nil {
		return nil
	}
	return chatDebugDisplaySessionProvider()
}

// ============================================================================
// JSON 快照结构
// ============================================================================

// chatDebugDisplaySnapshot 是 /debug/chat/status 的 JSON 响应体。
type chatDebugDisplaySnapshot struct {
	Available    bool                            `json:"available"`
	Reason       string                          `json:"reason,omitempty"`
	CapturedAt   time.Time                       `json:"captured_at"`
	Session      *chatDebugDisplaySessionInfo    `json:"session,omitempty"`
	Files        *chatDebugDisplayFilesInfo      `json:"files,omitempty"`
	Runtime      *chatDebugDisplayRuntimeInfo    `json:"runtime,omitempty"`
	Routing      *chatDebugDisplayRoutingInfo    `json:"routing,omitempty"`
	Components   *chatDebugDisplayComponentsInfo `json:"components,omitempty"`
	Agents       *chatDebugDisplayAgentsInfo     `json:"agents,omitempty"`
	Encoder      *chatDebugDisplayEncoderInfo    `json:"render_encoder,omitempty"`
	Scene        *chatDebugDisplaySceneInfo      `json:"scene,omitempty"`
	RenderOutput *chatDebugDisplayOutputInfo     `json:"render_output,omitempty"`
	AppState     *chatDebugDisplayAppStateInfo   `json:"app_state,omitempty"`
	Executor     *chatDebugDisplayExecutorInfo   `json:"executor,omitempty"`
	Projection   *chatDebugDisplayProjectionInfo `json:"projection,omitempty"`
	PaintTrace   string                          `json:"paint_trace,omitempty"`
	PprofURL     string                          `json:"pprof_url,omitempty"`
	// Storage 是存储与持久化统计（P1.5/P1.6/P1.7 观测接线）：
	// 与 /debug display 的"存储与持久化:"区块同源，字段契约沿用
	// internal/chat 的 JSON tag（runtime-server health 的 persist/store_pools 同构）。
	Storage *chatDebugStorageInfo `json:"storage,omitempty"`
}

type chatDebugDisplaySessionInfo struct {
	SessionID   string `json:"session_id,omitempty"`
	DebugMode   bool   `json:"debug_mode"`
	Transport   string `json:"transport,omitempty"`
	CoreName    string `json:"runtime_core,omitempty"`
	Surface     bool   `json:"surface_enabled"`
	Interaction string `json:"interaction,omitempty"`
	// 顶部 Session Info 区块（与 /debug display 面板第一屏一致）。
	Provider         string `json:"provider,omitempty"`
	Protocol         string `json:"protocol,omitempty"`
	Model            string `json:"model,omitempty"`
	EndpointURL      string `json:"endpoint_url,omitempty"`
	Host             string `json:"host,omitempty"`
	KeyCount         int    `json:"key_count,omitempty"`
	Timeout          string `json:"timeout,omitempty"`
	IsStream         bool   `json:"stream,omitempty"`
	SupportsFast     bool   `json:"supports_fast,omitempty"`
	IsFast           bool   `json:"fast_mode,omitempty"`
	ReasoningEnabled bool   `json:"reasoning_enabled,omitempty"`
	Profile          string `json:"profile,omitempty"`
	AgentSource      string `json:"agent_source,omitempty"`
	ReasoningEffort  string `json:"reasoning_effort,omitempty"`
}

type chatDebugDisplayEncoderInfo struct {
	EncodeCount       uint64                          `json:"encode_count"`
	AppendCount       uint64                          `json:"append_count"`
	UpsertCount       uint64                          `json:"upsert_count"`
	RemoveCount       uint64                          `json:"remove_count"`
	OutOfOrderCount   uint64                          `json:"out_of_order_count"`
	DuplicateCount    uint64                          `json:"duplicate_count"`
	UnknownCount      uint64                          `json:"unknown_count"`
	Tail              *chatDebugDisplayTailInfo       `json:"tail,omitempty"`
	InteractionAnchor *chatDebugDisplayAnchorInfo     `json:"interaction_anchor,omitempty"`
	EventLog          *chatDebugDisplayEventLogInfo   `json:"event_log,omitempty"`
	ModelItems        int                             `json:"model_items_count"`
	ModelItemsTail    []chatDebugDisplayModelItemInfo `json:"model_items_tail,omitempty"`
}

type chatDebugDisplayTailInfo struct {
	ItemID string `json:"item_id"`
	Seq    uint64 `json:"seq"`
}

type chatDebugDisplayAnchorInfo struct {
	Tail   *chatDebugDisplayTailInfo `json:"tail,omitempty"`
	At     string                    `json:"at,omitempty"`
	Source string                    `json:"source,omitempty"`
	Count  uint64                    `json:"count"`
}

type chatDebugDisplayEventLogInfo struct {
	Path     string `json:"path,omitempty"`
	Recorded uint64 `json:"recorded"`
	Replayed uint64 `json:"replayed"`
	Failures uint64 `json:"failures"`
}

type chatDebugDisplayModelItemInfo struct {
	Seq     uint64 `json:"seq"`
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Head    string `json:"head,omitempty"`
	CauseID string `json:"cause_id,omitempty"`
}

type chatDebugDisplaySceneInfo struct {
	Cells         uint64                          `json:"cells"`
	Revision      uint64                          `json:"revision"`
	ApplyFailures uint64                          `json:"apply_failures"`
	LastError     string                          `json:"last_error,omitempty"`
	LayoutRows    int                             `json:"layout_rows,omitempty"`
	LayoutGaps    int                             `json:"layout_gaps,omitempty"`
	TextRows      int                             `json:"text_rows,omitempty"`
	TextParity    *chatDebugDisplayTextParityInfo `json:"text_parity,omitempty"`
	DeferredQueue *chatDebugDisplayDeferredInfo   `json:"deferred_queue,omitempty"`
	StreamQueue   *chatDebugDisplayStreamInfo     `json:"stream_queue,omitempty"`
	CellsTail     []chatDebugDisplayCellInfo      `json:"cells_tail,omitempty"`
}

type chatDebugDisplayTextParityInfo struct {
	Blocks  uint64 `json:"blocks"`
	Matched uint64 `json:"matched"`
	Missed  uint64 `json:"missed"`
	Resyncs uint64 `json:"resyncs,omitempty"`
	Skips   uint64 `json:"skips,omitempty"`
	LastErr string `json:"last_error,omitempty"`
}

// chatDebugDisplayDeferredInfo mirrors the non-streaming event overflow queue
// (Fix 2): pending/bytes describe the current backlog, dropped counts events
// discarded because a stalled consumer exceeded the backlog caps.
type chatDebugDisplayDeferredInfo struct {
	Pending int    `json:"pending"`
	Bytes   int64  `json:"bytes,omitempty"`
	Dropped uint64 `json:"dropped,omitempty"`
	// 事件分级（docs/plan/ui-event-bridge-drop-hardening.md §6.1.6）：新增字段
	// 只增不改，旧客户端不受影响。
	Merged              uint64            `json:"merged,omitempty"`
	Evicted             uint64            `json:"evicted,omitempty"`
	DroppedByClass      map[string]uint64 `json:"dropped_by_class,omitempty"`
	DroppedByType       map[string]uint64 `json:"dropped_by_type,omitempty"`
	EvictedByType       map[string]uint64 `json:"evicted_by_type,omitempty"`
	PeakPending         int               `json:"peak_pending,omitempty"`
	PeakBytes           int64             `json:"peak_bytes,omitempty"`
	CriticalPending     uint64            `json:"critical_pending,omitempty"`
	CriticalPeakPending uint64            `json:"critical_peak_pending,omitempty"`
	CriticalAtShutdown  uint64            `json:"critical_at_shutdown,omitempty"`
	Degraded            bool              `json:"degraded,omitempty"`
	Mode                string            `json:"mode,omitempty"`
}

type chatDebugDisplayStreamInfo struct {
	Pending                int               `json:"pending"`
	Bytes                  int64             `json:"bytes,omitempty"`
	OldestPendingAgeMS     int64             `json:"oldest_pending_age_ms,omitempty"`
	RetainedEvents         uint64            `json:"retained_events,omitempty"`
	RetainedBytes          uint64            `json:"retained_bytes,omitempty"`
	DroppedEvents          uint64            `json:"dropped_events,omitempty"`
	DroppedBytes           uint64            `json:"dropped_bytes,omitempty"`
	RetainedByType         map[string]uint64 `json:"retained_by_type,omitempty"`
	DroppedByType          map[string]uint64 `json:"dropped_by_type,omitempty"`
	OverflowLogsSuppressed uint64            `json:"overflow_logs_suppressed,omitempty"`
}

type chatDebugDisplayCellInfo struct {
	ID       uint64 `json:"id"`
	Kind     string `json:"kind"`
	Source   string `json:"source,omitempty"`
	ChainKey string `json:"chain_key,omitempty"`
}

type chatDebugDisplayOutputInfo struct {
	State               string `json:"state"`
	PrimaryCommitted    uint64 `json:"primary_committed"`
	PrimaryDeferred     uint64 `json:"primary_deferred"`
	PrimaryRejected     uint64 `json:"primary_rejected"`
	AdmissionAccepted   uint64 `json:"admission_accepted"`
	AdmissionDeferred   uint64 `json:"admission_deferred"`
	AdmissionRejected   uint64 `json:"admission_rejected"`
	MirrorsApplied      uint64 `json:"mirrors_applied"`
	MirrorsFailed       uint64 `json:"mirrors_failed"`
	MirrorsSkipped      uint64 `json:"mirrors_skipped"`
	MirrorsTimedOut     uint64 `json:"mirrors_timed_out"`
	MirrorsLate         uint64 `json:"mirrors_late"`
	MirrorScheduleDrops uint64 `json:"mirror_schedule_drops"`
	ObserverDrops       uint64 `json:"observer_drops"`
	EventJournalDrops   uint64 `json:"event_journal_drops"`
	// Explicit names distinguish observability-history eviction from primary
	// delivery loss; legacy fields above remain for old clients.
	ObserverSubscriberDrops  uint64 `json:"observer_subscriber_drops"`
	EventJournalEvictions    uint64 `json:"event_journal_evictions"`
	DeliveryJournalEvictions uint64 `json:"delivery_journal_evictions"`
	DeliverySealed           uint64 `json:"delivery_records_sealed"`
	LastSequence             uint64 `json:"last_sequence"`
	// 高价值 in-flight / abandoned / unsealed 诊断字段。
	PrimaryInFlight         int           `json:"primary_in_flight,omitempty"`
	MirrorPending           int           `json:"mirror_pending,omitempty"`
	MirrorInFlight          int           `json:"mirror_in_flight,omitempty"`
	DeliveryRecordsUnsealed int           `json:"delivery_records_unsealed,omitempty"`
	EntrySealCount          uint64        `json:"entry_seal_count,omitempty"`
	Abandoned               uint64        `json:"abandoned,omitempty"`
	AbandonedReason         string        `json:"abandoned_reason,omitempty"`
	LastPrimaryDuration     time.Duration `json:"last_primary_duration_ns,omitempty"`
}

type chatDebugDisplayAppStateInfo struct {
	Revision         uint64 `json:"revision"`
	LayoutGeneration uint64 `json:"layout_generation"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	PrimaryLease     string `json:"primary_lease"`
	HistoryEffects   string `json:"history_effects"`
	// HistoryGates exposes the structured gates that decide whether the
	// executor may commit a pending HistoryCommit (or must run recovery).
	// They mirror the live predicate used by the presenter scheduler, so a
	// poller can tell "producer never finalized the active band" apart from
	// "executor is stuck (frozen / projection unknown / generation mismatch)".
	HistoryGates *chatDebugDisplayHistoryGateInfo `json:"history_gates,omitempty"`
	// ActiveCell is the semantic in-progress cell. A phase that stays
	// "mutable" or "finalizing" while the poller keeps seeing a growing band
	// is the signature of an active band that never commits.
	ActiveCell *chatDebugDisplayActiveCellInfo `json:"active_cell,omitempty"`
	// UIActor 是消费端成本指标（docs/plan/ui-event-bridge-drop-hardening.md
	// §6.2 第 2 条）。它把"事件桥丢事件"与"消费端是否跟得上"区分开：
	// FlushCount/Processed 的比值反映批处理收益，PostWait* 反映生产者被
	// mailbox 背压的真实时长。采样只读取一次 actor 快照，不参与调度。
	UIActor *chatDebugDisplayUIActorInfo `json:"ui_actor,omitempty"`
}

type chatDebugDisplayUIActorInfo struct {
	Processed        uint64 `json:"processed"`
	FlushCount       uint64 `json:"flush_count"`
	Batches          uint64 `json:"batches"`
	BatchSizeP95     int    `json:"batch_size_p95,omitempty"`
	BatchSizeMax     int    `json:"batch_size_max,omitempty"`
	ReducerNanos     uint64 `json:"reducer_nanos,omitempty"`
	PostWaitNanos    uint64 `json:"post_wait_nanos,omitempty"`
	PostWaitCount    uint64 `json:"post_wait_count,omitempty"`
	PostWaitMaxNanos uint64 `json:"post_wait_max_nanos,omitempty"`
	PostWaitP95Nanos uint64 `json:"post_wait_p95_nanos,omitempty"`
	PeakPending      int    `json:"peak_pending,omitempty"`
	Pending          int    `json:"pending"`
	Dropped          uint64 `json:"dropped,omitempty"`
	DeferredMerged   uint64 `json:"deferred_merged,omitempty"`
}

type chatDebugDisplayHistoryGateInfo struct {
	Frozen                 bool `json:"frozen"`
	ProjectionUnknown      bool `json:"projection_unknown"`
	ReconciliationRequired bool `json:"reconciliation_required"`
	// ScrollbackReplayArmed is the reducer-installed one-shot authorization for
	// the session-load replay, and the only state that lets the executor replace
	// native scrollback. Armed=true while RecoveryActionable=false means the
	// authorized replay has not been composed yet; armed=false with an
	// outstanding obligation means the recovery will settle in place instead.
	ScrollbackReplayArmed   bool   `json:"scrollback_replay_armed"`
	RecoveryActionable      bool   `json:"recovery_actionable"`
	PendingCount            int    `json:"pending_count"`
	OldestPendingToken      uint64 `json:"oldest_pending_token,omitempty"`
	OldestPendingGeneration uint64 `json:"oldest_pending_generation,omitempty"`
}

type chatDebugDisplayActiveCellInfo struct {
	ID            uint64 `json:"id,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Phase         string `json:"phase,omitempty"`
	Revision      uint64 `json:"revision,omitempty"`
	Source        string `json:"source,omitempty"`
	CommitBlocked bool   `json:"commit_blocked"`
	// Stable/Enqueued/Acked are the semantic source boundaries of the mutable
	// cell. A poller can watch a growing Enqueued while Acked stays fixed:
	// that is the signature of an active band whose stream updates render but
	// whose acknowledged prefix never advances toward a commit.
	StableStart int `json:"stable_start,omitempty"`
	StableEnd   int `json:"stable_end,omitempty"`
	EnqueuedEnd int `json:"enqueued_end,omitempty"`
	AckedEnd    int `json:"acked_end,omitempty"`
}

// chatDebugDisplayExecutorInfo mirrors the executor recovery-loop diagnostics
// (see ui.ExecutorDiagSnapshot). It is the execution side of the commit gates:
// a window_diagnosis of "dead_guard" or "backoff_engaged" plus rising
// recoveries/frame-errors under an unchanged generation is direct evidence
// that the executor is spinning on recovery instead of committing.
type chatDebugDisplayExecutorInfo struct {
	// Diagnosis is the since-start verdict (lifetime counters); a past storm
	// keeps it non-healthy forever. DiagnosisScope names that explicitly so a
	// reader cannot mistake it for the current state.
	Diagnosis      string `json:"diagnosis"`
	DiagnosisScope string `json:"diagnosis_scope"`
	// WindowDiagnosis is the current-state verdict over the retained window
	// (most recent 64 executor iterations, none older than 60s): the field to
	// poll when asking "is the executor healthy right now?". The shape fields
	// say what the verdict was computed over.
	WindowDiagnosis          string                             `json:"window_diagnosis"`
	WindowEntries            int                                `json:"window_entries"`
	WindowSpanMs             int64                              `json:"window_span_ms"`
	WindowAgeMs              int64                              `json:"window_age_ms"`
	TotalRecoveries          uint64                             `json:"total_recoveries"`
	BackoffEngaged           uint64                             `json:"backoff_engaged"`
	ArmedBackoff             uint64                             `json:"armed_backoff"`
	FlushesWhileBackoff      uint64                             `json:"flushes_while_backoff"`
	HandoffsWhileBackoff     uint64                             `json:"handoffs_while_backoff"`
	GeneratedAtUnixMs        int64                              `json:"generated_at_unix_ms"`
	WindowRecoveriesPerSec   float64                            `json:"window_recoveries_per_sec"`
	GenerationAdvancesWindow int                                `json:"generation_advances_in_window"`
	FrameErrorsWindow        int                                `json:"frame_errors_in_window"`
	ScrollbackResetsWindow   int                                `json:"scrollback_resets_in_window"`
	LastGeneration           uint64                             `json:"last_generation"`
	LastEntry                *chatDebugDisplayExecutorEntryInfo `json:"last_entry,omitempty"`
}

// chatDebugDisplayExecutorEntryInfo is the most recent recovery-loop ring entry.
type chatDebugDisplayExecutorEntryInfo struct {
	Seq                 uint64 `json:"seq"`
	Branch              string `json:"branch"`
	Generation          uint64 `json:"generation"`
	Revision            uint64 `json:"revision"`
	RevisionAfter       uint64 `json:"revision_after"`
	TerminalEpoch       uint64 `json:"terminal_epoch"`
	ProjectionUnknown   bool   `json:"projection_unknown"`
	ReconciliationReq   bool   `json:"reconciliation_required"`
	BackoffEngaged      bool   `json:"backoff_engaged"`
	ArmedBackoff        bool   `json:"armed_backoff"`
	FullRepaint         bool   `json:"full_repaint"`
	ScrollbackReset     bool   `json:"scrollback_reset"`
	FrameErr            string `json:"frame_error,omitempty"`
	FlushedWhileBackoff bool   `json:"flushed_while_backoff"`
	HandoffWhileBackoff bool   `json:"handoff_while_backoff"`
	Continued           bool   `json:"continued"`
}

// chatDebugDisplayProjectionInfo mirrors TerminalSession.ProjectionState(): the
// physical terminal cache summary. HistoryKnown=false or Validity!=projection_known
// is direct evidence that the terminal believes it cannot trust the scrollback,
// which is exactly the condition that makes the executor refuse to commit.
type chatDebugDisplayProjectionInfo struct {
	HistoryRows               int    `json:"history_rows"`
	HistoryKnown              bool   `json:"history_known"`
	LayoutGeneration          uint64 `json:"layout_generation"`
	TerminalEpoch             uint64 `json:"terminal_epoch"`
	Frame                     uint64 `json:"frame"`
	ScrollbackResetCount      uint64 `json:"scrollback_reset_count"`
	LastScrollbackResetReason string `json:"last_scrollback_reset_reason,omitempty"`
	Validity                  string `json:"validity"`
	OutputBottomRow           int    `json:"output_bottom_row"`
}

// ============================================================================
// 快照构建
// ============================================================================

// BuildChatDebugDisplaySnapshot 返回当前会话的渲染/显示状态 JSON 快照。
// 无会话时返回 available=false 的轻量响应。
func BuildChatDebugDisplaySnapshot() *chatDebugDisplaySnapshot {
	snap := &chatDebugDisplaySnapshot{
		CapturedAt: time.Now(),
	}
	session := chatDebugDisplaySession()
	if session == nil {
		snap.Available = false
		snap.Reason = "no active chat session"
		return snap
	}
	snap.Available = true

	// ====== 会话基础信息（顶部 Session Info 区块，与 /debug display 面板一致） ======
	snap.Session = buildChatDebugDisplaySessionInfo(session)
	snap.PprofURL = chatDebugPprofEndpointURL()

	// ====== 面板信息区块的 HTTP 结构化投影 ======
	// 每个区块对应 /debug display 面板上的一个信息区块；只读快照，不做变更。
	snap.Files = buildChatDebugDisplayFilesInfo(session)
	snap.Runtime = buildChatDebugDisplayRuntimeInfo(session)
	snap.Routing = buildChatDebugDisplayRoutingInfo(session)
	snap.Components = buildChatDebugDisplayComponentsInfo(session)
	snap.Agents = buildChatDebugDisplayAgentsInfo(session)

	// ====== Unified Render Encoder ======
	if bridge := session.RuntimeEventBridge; bridge != nil {
		stats := bridge.renderEncoderStats()
		enc := &chatDebugDisplayEncoderInfo{
			EncodeCount:     stats.EncodeCount,
			AppendCount:     stats.AppendCount,
			UpsertCount:     stats.UpsertCount,
			RemoveCount:     stats.RemoveCount,
			OutOfOrderCount: stats.OutOfOrderCount,
			DuplicateCount:  stats.DuplicateCount,
			UnknownCount:    stats.UnknownCount,
		}
		if tail := bridge.renderModelTail(); tail != nil {
			enc.Tail = &chatDebugDisplayTailInfo{ItemID: tail.ItemID, Seq: tail.Seq}
		}
		if tail, at, source, count := bridge.lastInteractionAnchor(); tail != nil {
			enc.InteractionAnchor = &chatDebugDisplayAnchorInfo{
				Tail:   &chatDebugDisplayTailInfo{ItemID: tail.ItemID, Seq: tail.Seq},
				At:     at.Format("15:04:05"),
				Source: source,
				Count:  count,
			}
		}
		if path, count, replayed, failures := bridge.eventLogStats(); path != "" {
			enc.EventLog = &chatDebugDisplayEventLogInfo{
				Path:     path,
				Recorded: count,
				Replayed: replayed,
				Failures: failures,
			}
		}
		model := bridge.renderModelSnapshot()
		if model != nil {
			enc.ModelItems = len(model.Items)
			startItem := 0
			if len(model.Items) > chatDebugRenderEncoderItemCap {
				startItem = len(model.Items) - chatDebugRenderEncoderItemCap
			}
			for _, it := range model.Items[startItem:] {
				if it == nil {
					continue
				}
				enc.ModelItemsTail = append(enc.ModelItemsTail, chatDebugDisplayModelItemInfo{
					Seq:     it.Seq,
					ID:      it.ID,
					Kind:    string(it.Kind),
					Head:    chatDebugTruncate(it.Head, 48),
					CauseID: it.CauseID,
				})
			}
		}
		snap.Encoder = enc

		// ====== Unified Render Scene ======
		cells, revision, failures, lastErr := bridge.sceneStats()
		sc := &chatDebugDisplaySceneInfo{
			Cells:         cells,
			Revision:      revision,
			ApplyFailures: failures,
			LastError:     lastErr,
		}
		pending, queuedBytes, dropped := bridge.deferredQueueStats()
		classStats := bridge.deferredQueueClassStats()
		if pending > 0 || dropped > 0 || classStats.Merged > 0 || classStats.Evicted > 0 ||
			classStats.CriticalPending > 0 || classStats.CriticalPeakPending > 0 ||
			classStats.CriticalAtShutdown > 0 || classStats.Degraded {
			sc.DeferredQueue = &chatDebugDisplayDeferredInfo{
				Pending:             pending,
				Bytes:               queuedBytes,
				Dropped:             dropped,
				Merged:              classStats.Merged,
				Evicted:             classStats.Evicted,
				DroppedByClass:      classStats.DroppedByClass,
				DroppedByType:       classStats.DroppedByType,
				EvictedByType:       classStats.EvictedByType,
				PeakPending:         classStats.PeakPending,
				PeakBytes:           classStats.PeakBytes,
				CriticalPending:     classStats.CriticalPending,
				CriticalPeakPending: classStats.CriticalPeakPending,
				CriticalAtShutdown:  classStats.CriticalAtShutdown,
				Degraded:            classStats.Degraded,
				Mode:                classStats.Mode,
			}
		}
		streamStats := bridge.streamQueueStats()
		if streamStats.Pending > 0 || streamStats.RetainedEvents > 0 || streamStats.DroppedEvents > 0 ||
			streamStats.OverflowLogsSuppressed > 0 {
			sc.StreamQueue = &chatDebugDisplayStreamInfo{
				Pending:                streamStats.Pending,
				Bytes:                  streamStats.Bytes,
				OldestPendingAgeMS:     streamStats.OldestPendingAge.Milliseconds(),
				RetainedEvents:         streamStats.RetainedEvents,
				RetainedBytes:          streamStats.RetainedBytes,
				DroppedEvents:          streamStats.DroppedEvents,
				DroppedBytes:           streamStats.DroppedBytes,
				RetainedByType:         streamStats.RetainedByType,
				DroppedByType:          streamStats.DroppedByType,
				OverflowLogsSuppressed: streamStats.OverflowLogsSuppressed,
			}
		}
		if scn := bridge.sceneSnapshot(); scn != nil {
			if len(scn.Cells) > 0 {
				rows := scene.LayoutTranscript(scn.Cells, scn.Revision)
				gaps := 0
				for _, r := range rows {
					if r.Gap > 0 {
						gaps++
					}
				}
				sc.LayoutRows = len(rows)
				sc.LayoutGaps = gaps
				sc.TextRows = len(scene.RenderText(scn.Cells, scn.Revision))
			}
			if blocks, matched, missed, lastErr := bridge.textParityStats(); blocks > 0 || matched > 0 || missed > 0 {
				resyncs, skips := bridge.textParityAlignmentStats()
				sc.TextParity = &chatDebugDisplayTextParityInfo{
					Blocks:  blocks,
					Matched: matched,
					Missed:  missed,
					Resyncs: resyncs,
					Skips:   skips,
					LastErr: lastErr,
				}
			}
			startCell := 0
			if len(scn.Cells) > chatDebugRenderEncoderItemCap {
				startCell = len(scn.Cells) - chatDebugRenderEncoderItemCap
			}
			for _, c := range scn.Cells[startCell:] {
				if c == nil {
					continue
				}
				sc.CellsTail = append(sc.CellsTail, chatDebugDisplayCellInfo{
					ID:       uint64(c.ID),
					Kind:     c.Kind.String(),
					Source:   chatDebugTruncate(c.Source, 48),
					ChainKey: c.ChainKey,
				})
			}
		}
		snap.Scene = sc
	}

	// ====== Render Output (Gateway) ======
	if session.TerminalSession != nil {
		if out := session.TerminalSession.RenderOutputSnapshot(); out != nil {
			snap.RenderOutput = &chatDebugDisplayOutputInfo{
				State:                    string(out.State),
				PrimaryCommitted:         out.PrimaryCommitted,
				PrimaryDeferred:          out.PrimaryDeferred,
				PrimaryRejected:          out.PrimaryRejected,
				AdmissionAccepted:        out.AdmissionAccepted,
				AdmissionDeferred:        out.AdmissionDeferred,
				AdmissionRejected:        out.AdmissionRejected,
				MirrorsApplied:           out.MirrorsApplied,
				MirrorsFailed:            out.MirrorsFailed,
				MirrorsSkipped:           out.MirrorsSkipped,
				MirrorsTimedOut:          out.MirrorsTimedOut,
				MirrorsLate:              out.MirrorsLate,
				MirrorScheduleDrops:      out.MirrorScheduleDrops,
				ObserverDrops:            out.ObserverDrops,
				EventJournalDrops:        out.EventJournalDrops,
				ObserverSubscriberDrops:  out.ObserverSubscriberDrops,
				EventJournalEvictions:    out.EventJournalEvictions,
				DeliveryJournalEvictions: out.DeliveryJournalEvictions,
				DeliverySealed:           out.DeliveryRecordsSealed,
				LastSequence:             out.LastSequence,
				// 补充高价值诊断字段：in-flight、abandoned、unsealed
				PrimaryInFlight:         out.PrimaryInFlight,
				MirrorPending:           out.MirrorPending,
				MirrorInFlight:          out.MirrorInFlight,
				DeliveryRecordsUnsealed: out.DeliveryRecordsUnsealed,
				EntrySealCount:          out.EntrySealCount,
				Abandoned:               out.Abandoned,
				AbandonedReason:         out.AbandonedReason,
				LastPrimaryDuration:     out.LastPrimaryDuration,
			}
		}
	}

	// ====== AppState (Presenter Migration) ======
	if session.Interaction != nil && session.Interaction.uiActor != nil {
		state := session.Interaction.uiActor.State()
		lease := "inactive"
		if state.Lease.Active {
			lease = "active"
		}
		app := &chatDebugDisplayAppStateInfo{
			Revision:         state.Revision,
			LayoutGeneration: state.LayoutGeneration,
			Width:            state.Geometry.Width,
			Height:           state.Geometry.Height,
			PrimaryLease:     lease,
			HistoryEffects:   chatDebugHistoryEffectSummary(state.HistoryEffects),
		}
		// Structured commit gates: the same predicate the presenter scheduler
		// uses (terminalHistoryRecoveryActionable) plus the oldest pending
		// token identity so a poller can correlate a stuck pending commit with
		// the current layout generation.
		effects := state.HistoryEffects
		gates := &chatDebugDisplayHistoryGateInfo{
			Frozen:                 effects.Frozen,
			ProjectionUnknown:      effects.ProjectionUnknown,
			ReconciliationRequired: effects.ReconciliationRequired,
			ScrollbackReplayArmed:  effects.ScrollbackReplayArmed,
			RecoveryActionable: !state.Lease.Active && !effects.Frozen &&
				(effects.ProjectionUnknown || effects.ReconciliationRequired),
		}
		for _, entry := range effects.Entries() {
			if entry.State != ui.HistoryCommitPending {
				continue
			}
			gates.PendingCount++
			if gates.OldestPendingToken == 0 {
				gates.OldestPendingToken = entry.Commit.Token
				gates.OldestPendingGeneration = entry.Commit.LayoutGeneration
			}
		}
		app.HistoryGates = gates
		// 消费端成本快照（§6.2 第 2 条）：只在有活动时输出，避免空会话的
		// 噪声字段。Stats() 是 actor 互斥量下的定长快照，不阻塞 Run 循环。
		if actorStats := session.Interaction.uiActor.Stats(); actorStats.Processed > 0 ||
			actorStats.PostWaitCount > 0 || actorStats.Pending > 0 {
			app.UIActor = &chatDebugDisplayUIActorInfo{
				Processed:        actorStats.Processed,
				FlushCount:       actorStats.FlushCount,
				Batches:          actorStats.Batches,
				BatchSizeP95:     actorStats.BatchSizeP95,
				BatchSizeMax:     actorStats.BatchSizeMax,
				ReducerNanos:     actorStats.ReducerNanos,
				PostWaitNanos:    actorStats.PostWaitNanos,
				PostWaitCount:    actorStats.PostWaitCount,
				PostWaitMaxNanos: actorStats.PostWaitMaxNanos,
				PostWaitP95Nanos: actorStats.PostWaitP95Nanos,
				PeakPending:      actorStats.PeakPending,
				Pending:          actorStats.Pending,
				Dropped:          actorStats.Dropped,
				DeferredMerged:   actorStats.DeferredMerged,
			}
		}
		if state.Active.Phase != ui.ActiveCellInactive {
			app.ActiveCell = &chatDebugDisplayActiveCellInfo{
				ID:            uint64(state.Active.CellID),
				Kind:          state.Active.Kind.String(),
				Phase:         chatDebugActiveCellPhase(state.Active.Phase),
				Revision:      state.Active.Revision,
				Source:        chatDebugTruncate(state.Active.Source, 48),
				CommitBlocked: state.Active.HistoryCommitBlocked,
				StableStart:   state.Active.Stable.Start,
				StableEnd:     state.Active.Stable.End,
				EnqueuedEnd:   state.Active.Enqueued.End,
				AckedEnd:      state.Active.Acked.End,
			}
		}
		snap.AppState = app
	}

	// ====== Executor Recovery Diagnostics ======
	if diag := ui.ExecutorDiagSnapshot(); diag.Diagnosis != "" || diag.WindowDiagnosis != "" || diag.TotalRecoveries > 0 {
		exec := &chatDebugDisplayExecutorInfo{
			Diagnosis:                diag.Diagnosis,
			DiagnosisScope:           "since_start",
			WindowDiagnosis:          diag.WindowDiagnosis,
			WindowEntries:            diag.WindowEntries,
			WindowSpanMs:             diag.WindowSpanMs,
			WindowAgeMs:              diag.WindowAgeMs,
			TotalRecoveries:          diag.TotalRecoveries,
			BackoffEngaged:           diag.BackoffEngaged,
			ArmedBackoff:             diag.ArmedBackoff,
			FlushesWhileBackoff:      diag.FlushesWhileBackoff,
			HandoffsWhileBackoff:     diag.HandoffsWhileBackoff,
			GeneratedAtUnixMs:        diag.GeneratedAtUnixMs,
			WindowRecoveriesPerSec:   diag.WindowRecoveriesPerSec,
			GenerationAdvancesWindow: diag.GenerationAdvancesInWindow,
			FrameErrorsWindow:        diag.FrameErrorsInWindow,
			ScrollbackResetsWindow:   diag.ScrollbackResetsInWindow,
			LastGeneration:           diag.LastGeneration,
		}
		if len(diag.Entries) > 0 {
			last := diag.Entries[len(diag.Entries)-1]
			exec.LastEntry = &chatDebugDisplayExecutorEntryInfo{
				Seq:                 last.Seq,
				Branch:              last.Branch,
				Generation:          last.Generation,
				Revision:            last.Revision,
				RevisionAfter:       last.RevisionAfter,
				TerminalEpoch:       last.TerminalEpoch,
				ProjectionUnknown:   last.ProjectionUnknown,
				ReconciliationReq:   last.ReconciliationReq,
				BackoffEngaged:      last.BackoffEngaged,
				ArmedBackoff:        last.ArmedBackoff,
				FullRepaint:         last.FullRepaint,
				ScrollbackReset:     last.ScrollbackReset,
				FrameErr:            last.FrameErr,
				FlushedWhileBackoff: last.FlushedWhileBackoff,
				HandoffWhileBackoff: last.HandoffWhileBackoff,
				Continued:           last.Continued,
			}
		}
		snap.Executor = exec
	}

	// ====== Terminal Projection (Physical Cache) ======
	if session.TerminalSession != nil {
		proj := session.TerminalSession.ProjectionState()
		snap.Projection = &chatDebugDisplayProjectionInfo{
			HistoryRows:               proj.HistoryRows,
			HistoryKnown:              proj.HistoryKnown,
			LayoutGeneration:          proj.LayoutGeneration,
			TerminalEpoch:             proj.TerminalEpoch,
			Frame:                     proj.Frame,
			ScrollbackResetCount:      proj.ScrollbackResetCount,
			LastScrollbackResetReason: proj.LastScrollbackResetReason,
			Validity:                  proj.Validity.String(),
			OutputBottomRow:           proj.OutputBottomRow,
		}
	}

	// ====== Paint Trace ======
	if session.Surface != nil {
		snap.PaintTrace = session.Surface.PaintTraceDebugString()
	}

	// ====== Storage / Persistence (P1.5/P1.6/P1.7) ======
	snap.Storage = chatDebugStorageSnapshot(session)

	return snap
}

// BuildChatDebugDisplayText 返回 /debug display 的纯文本摘要（用于 ?format=text）。
func BuildChatDebugDisplayText() string {
	session := chatDebugDisplaySession()
	if session == nil {
		return "Debug Display: no active chat session\n"
	}
	return buildChatDebugDisplayDocument(session).PlainText()
}

// MarshalChatDebugDisplayJSON 返回缩进 JSON 字节，供 HTTP 端点直接写入。
func MarshalChatDebugDisplayJSON() ([]byte, error) {
	return json.MarshalIndent(BuildChatDebugDisplaySnapshot(), "", "  ")
}

// ============================================================================
// 辅助函数
// ============================================================================

// chatDebugSessionID 返回当前 runtime 会话 ID（无则空串）。
func chatDebugSessionID(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return ""
	}
	return session.RuntimeSession.ID
}

// chatDebugInteractionSummary 返回 coordination 摘要（与 /debug display 一致）。
func chatDebugInteractionSummary(session *ChatSession) string {
	if session == nil || session.Interaction == nil {
		return "<none>"
	}
	return session.Interaction.DebugSummary()
}

// chatDebugTruncate 截断字符串到指定宽度（按 rune，避免切断 UTF-8）。
func chatDebugTruncate(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width]) + "…"
}

// chatDebugActiveCellPhase renders the semantic active-cell lifecycle phase as
// a stable token for pollers. A band that stays in "mutable" or "finalizing"
// while growing is the signature of an active band that never commits.
func chatDebugActiveCellPhase(phase ui.ActiveCellPhase) string {
	switch phase {
	case ui.ActiveCellMutable:
		return "mutable"
	case ui.ActiveCellFinalizing:
		return "finalizing"
	default:
		return "inactive"
	}
}
