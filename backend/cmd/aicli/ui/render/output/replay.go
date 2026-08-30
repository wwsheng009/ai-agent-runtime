package output

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ============================================================================
// Phase 5：版本化 recorder 与 replay decoder（9.5 / 11.5 / 17.5）
// ============================================================================

// RecordedSchemaVersion 是 replay archive 的格式版本。未知 major version
// 拒绝；minor 只允许忽略明确标记为 optional 的字段。
const (
	ReplayArchiveSchemaMajor = 1
	ReplayArchiveSchemaMinor = 0
)

// ReplayMode 描述 replay 的应用模式。
type ReplayMode string

const (
	ReplayCommittedWire   ReplayMode = "committed_wire"
	ReplayAttemptedIntent ReplayMode = "attempted_intent"
)

// ReplayArchiveEntry 是 decoder 的不可信输入模型；PayloadSource 是导出时
// 选择的单个 capture/recorder delivery，不是 payload handle。
type ReplayArchiveEntry struct {
	SchemaMajor     uint32
	SchemaMinor     uint32
	Record          DeliveryRecord
	PayloadSource   CapturedDelivery
	Payload         []byte
	PayloadChecksum string // 覆盖 canonical descriptor + payload 的 archive integrity checksum
}

// ReplayProvenance 是 envelope 的来源事实；必须与封存 record 的 primary
// identity/status 一致，且不带 error、payload handle 或可变 sink 指针。
type ReplayProvenance struct {
	SourceSessionID          string
	SourceBatchID            string
	SourceSequence           uint64
	SourceRouteEpoch         uint64
	SourceTargetClass        TargetClass
	SourceProjectionTargetID string
	SourceStatus             DeliveryStatus
	SourceCertainty          WriteCertainty
	SourceKind               TransactionKind
	SourceTerminal           RenderTerminalContext
	SourceHistory            *HistoryDeliveryDomain
}

// ReplayEnvelope 是 replay runner 的 detached 输入；Payload 是 bounded copy，
// 不可引用 payload store/handle。
type ReplayEnvelope struct {
	Record           DeliveryRecord // detached；只作 provenance/审计输入
	PayloadSource    CapturedDelivery
	Payload          []byte // detached bounded copy
	Mode             ReplayMode
	Provenance       ReplayProvenance
	NonAuthoritative bool
}

// ReplayValidationError 是 decoder fail-closed 的稳定错误（不可信输入）。
type ReplayValidationError struct {
	Reason string
}

func (e *ReplayValidationError) Error() string { return "replay validation failed: " + e.Reason }

func replayErr(format string, args ...any) error {
	return &ReplayValidationError{Reason: fmt.Sprintf(format, args...)}
}

// ReplayArchiveChecksum 计算 canonical descriptor + payload 的 archive
// integrity checksum（与 session-keyed ContentHash 分离；不是敏感内容 hash）。
func ReplayArchiveChecksum(entry ReplayArchiveEntry) string {
	h := sha256.New()
	fmt.Fprintf(h, "schema=%d.%d;", entry.SchemaMajor, entry.SchemaMinor)
	fmt.Fprintf(h, "record=%s;seq=%d;", entry.Record.Output.BatchID, entry.Record.Output.Sequence)
	fmt.Fprintf(h, "batch=%s;", entry.Record.Batch.BatchID)
	fmt.Fprintf(h, "payload=%d;", len(entry.Payload))
	h.Write(entry.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

// ValidateReplayChecksum 校验 entry 自带的 checksum 与重算值一致。
func ValidateReplayChecksum(entry ReplayArchiveEntry) bool {
	return entry.PayloadChecksum != "" && entry.PayloadChecksum == ReplayArchiveChecksum(entry)
}

// ReplayEnvelopeFromArchive 把不可信 ReplayArchiveEntry 转成 detached
// ReplayEnvelope。校验顺序（9.5）：
//  1. archive schema major 必须已知（minor 只允许 optional 忽略）；
//  2. record 必须已封存（SealedAt 非零、Output.Primary 存在）；
//  3. payload source 存在、payload bound、checksum 匹配；
//  4. provenance 派生自 record 并按值校验 identity/status/kind/terminal/history。
//
// 任何失败返回 ReplayValidationError，不产生 envelope。
func ReplayEnvelopeFromArchive(entry ReplayArchiveEntry, mode ReplayMode) (*ReplayEnvelope, error) {
	// 1. schema。
	if entry.SchemaMajor != ReplayArchiveSchemaMajor {
		return nil, replayErr("unknown archive major %d", entry.SchemaMajor)
	}
	if entry.SchemaMinor > ReplayArchiveSchemaMinor {
		return nil, replayErr("unsupported archive minor %d", entry.SchemaMinor)
	}
	// 2. record 已封存。
	rec := entry.Record
	if rec.SealedAt.IsZero() {
		return nil, replayErr("record not sealed")
	}
	if rec.Output.Primary == nil {
		return nil, replayErr("record has no primary receipt")
	}
	// 3. payload source 与 checksum。
	if entry.PayloadSource.CaptureEntryID == "" {
		return nil, replayErr("payload source missing")
	}
	if len(entry.Payload) == 0 {
		return nil, replayErr("payload empty")
	}
	if !ValidateReplayChecksum(entry) {
		return nil, replayErr("archive checksum mismatch")
	}
	// 4. provenance 与 record 按值一致。
	provenance := ReplayProvenance{
		SourceSessionID:          rec.Batch.SessionID,
		SourceBatchID:            rec.Output.BatchID,
		SourceSequence:           rec.Output.Sequence,
		SourceRouteEpoch:         rec.Output.RouteEpoch,
		SourceTargetClass:        rec.Output.ProjectionTargetClass,
		SourceProjectionTargetID: rec.Output.ProjectionTargetID,
		SourceStatus:             rec.Output.Primary.Status,
		SourceCertainty:          rec.Output.Primary.Certainty,
		SourceKind:               rec.Batch.Kind,
		SourceTerminal:           rec.Batch.Terminal,
		SourceHistory:            rec.Batch.History,
	}
	nonAuth := false
	if mode == ReplayAttemptedIntent {
		// attempted-intent 例外：允许重放 non-committed primary，但结果
		// 必须标 non-authoritative。zero-proof 的 bytes 从未写入，无
		// intent 可重放——拒绝。
		if !provableAttempted(provenance) {
			return nil, replayErr("attempted replay requires provable intent, got %s", provenance.SourceStatus)
		}
		nonAuth = true
	} else if provenance.SourceStatus == DeliveryUnknownPartial {
		// 9.5：UnknownPartial 默认阻断连续 replay（committed_wire 模式）。
		return nil, replayErr("UnknownPartial primary blocks committed replay")
	}
	// committed_wire 只允许 committed/full primary。
	if mode == ReplayCommittedWire && !(provenance.SourceStatus == DeliveryCommitted &&
		provenance.SourceCertainty == WriteCertaintyFull) {
		return nil, replayErr("committed_wire requires committed/full primary, got %s/%s",
			provenance.SourceStatus, provenance.SourceCertainty)
	}
	return &ReplayEnvelope{
		Record:           rec,
		PayloadSource:    entry.PayloadSource,
		Payload:          append([]byte(nil), entry.Payload...),
		Mode:             mode,
		Provenance:       provenance,
		NonAuthoritative: nonAuth,
	}, nil
}

// provableAttempted 判断 attempted-intent 是否可证明（status 非 committed
// 但已记录 intent；rejected 除外——被拒的 intent 无 bytes 可重放）。
func provableAttempted(p ReplayProvenance) bool {
	switch p.SourceStatus {
	case DeliveryUnknownPartial, DeliveryDeferred:
		return true
	case DeliveryFailedZeroBytes:
		// zero-proof 的 bytes 从未写入；attempted 重放无意义。
		return false
	default:
		return false
	}
}

// ReplayRecordID 是每次 replay 生成的稳定新 identity（9.5：replay 不复用
// source identity；source BatchID → replay ParentBatchID）。
func (e *ReplayEnvelope) ReplayRecordID() string {
	// 调用方需用 gateway 新建的 session/route/sequence/batch；此处只暴露
	// source identity 供审计，运行期 identity 由 gateway 盖章。
	return "replay:" + e.Record.Output.BatchID
}

// SourceIdentity 返回 source 的完整 identity 链（审计/去重用）。
type SourceIdentity struct {
	SessionID        string
	BatchID          string
	Sequence         uint64
	RouteEpoch       uint64
	TargetID         string
	Kind             TransactionKind
	NonAuthoritative bool
}

// Identity 返回 envelope 的 source identity（重放审计用）。
func (e *ReplayEnvelope) Identity() SourceIdentity {
	return SourceIdentity{
		SessionID:        e.Provenance.SourceSessionID,
		BatchID:          e.Provenance.SourceBatchID,
		Sequence:         e.Provenance.SourceSequence,
		RouteEpoch:       e.Provenance.SourceRouteEpoch,
		TargetID:         e.Provenance.SourceProjectionTargetID,
		Kind:             e.Provenance.SourceKind,
		NonAuthoritative: e.NonAuthoritative,
	}
}

// ReplayBatchFromEnvelope 构造 replay 用的 RenderIntent（feed 到
// virtual/capture replay route；不触达 console）。
func (e *ReplayEnvelope) ReplayBatchFromEnvelope(intentID string) RenderIntent {
	intent := RenderIntent{
		IntentID:     intentID,
		Kind:         e.Provenance.SourceKind,
		Source:       "replay",
		Cause:        "replay:" + e.Provenance.SourceBatchID,
		Bytes:        append([]byte(nil), e.Payload...),
		Terminal:     e.Provenance.SourceTerminal,
		HistoryEpoch: e.Provenance.SourceHistoryEpoch(),
	}
	return intent
}

// SourceHistoryEpoch 返回 provenance 的 history epoch（若存在）。
func (p ReplayProvenance) SourceHistoryEpoch() *uint64 {
	if p.SourceHistory == nil {
		return nil
	}
	ep := p.SourceHistory.HistoryEpoch
	return &ep
}

// ValidateProvenanceMatchesRecord 校验 provenance 与 record 按值一致
// （decoder 使用；外部构造的 provenance 不能绕过）。
func ValidateProvenanceMatchesRecord(p ReplayProvenance, rec DeliveryRecord) error {
	if p.SourceBatchID != rec.Output.BatchID {
		return replayErr("provenance batch %q != record %q", p.SourceBatchID, rec.Output.BatchID)
	}
	if p.SourceSequence != rec.Output.Sequence {
		return replayErr("provenance sequence %d != record %d", p.SourceSequence, rec.Output.Sequence)
	}
	if p.SourceRouteEpoch != rec.Output.RouteEpoch {
		return replayErr("provenance route epoch %d != record %d", p.SourceRouteEpoch, rec.Output.RouteEpoch)
	}
	if p.SourceProjectionTargetID != rec.Output.ProjectionTargetID {
		return replayErr("provenance target %q != record %q",
			p.SourceProjectionTargetID, rec.Output.ProjectionTargetID)
	}
	if p.SourceKind != rec.Batch.Kind {
		return replayErr("provenance kind %s != record %s", p.SourceKind, rec.Batch.Kind)
	}
	if !strings.EqualFold(p.SourceTerminal.Profile.ID, rec.Batch.Terminal.Profile.ID) ||
		p.SourceTerminal.Geometry != rec.Batch.Terminal.Geometry {
		return replayErr("provenance terminal context mismatch")
	}
	// history equality（nil vs nil 相等；否则按 epoch 比较）。
	ph, rh := p.SourceHistory, rec.Batch.History
	if (ph == nil) != (rh == nil) {
		return replayErr("provenance history presence mismatch")
	}
	if ph != nil && ph.HistoryEpoch != rh.HistoryEpoch {
		return replayErr("provenance history epoch %d != record %d", ph.HistoryEpoch, rh.HistoryEpoch)
	}
	return nil
}
