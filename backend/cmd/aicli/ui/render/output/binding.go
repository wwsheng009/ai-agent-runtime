package output

import "context"

// ============================================================================
// SessionBindingRef（8.1）
// ============================================================================

// SessionBindingRef 是稳定 binding：SessionID + BindingGeneration + 绑定期间的
// 提交端口。binding 在两阶段切换时保持同一 generation；新旧 route 共用旧
// generation 直到 Commit（Phase 5 完整语义；Phase 0 固定 generation=0）。
type SessionBindingRef struct {
	SessionID         string
	BindingGeneration uint64
	Port              RenderSubmitPort
}

// Finality 是 authority 的最终结果。binding 上不保留 error interface。
type Finality struct {
	Class  DeliverabilityClass
	Reason string
}

// 在有效期内允许的客观结果；不许出现在 receipt 字段中。
type DeliverabilityClass string

const (
	DeliverableUnknownClass   DeliverabilityClass = "unknown"
	DeliverableZeroClass      DeliverabilityClass = "zero"
	DeliverablePartialClass   DeliverabilityClass = "partial"
	DeliverableFullClass      DeliverabilityClass = "full"
	DeliverableAbandonedClass DeliverabilityClass = "abandoned"
)

// LegacyTransactionAdapter（Phase 3 完整实现；Phase 0 定义接口与门禁）。
// active session 关闭后禁止向 stdout 补写；所有 legacy surface effect
// 必须通过 bound port 提交。
type LegacyTransactionAdapter struct {
	Port RenderSubmitPort
}

// LegacyTransaction 是 legacy surface 的一次 flush/handoff/immediate 效果；
// 映射到 TransactionLegacyFlush / TransactionLegacyImmediate。
type LegacyTransaction struct {
	Kind   TransactionKind
	Bytes  []byte
	Source string
	Cause  string
	Epoch  *uint64 // dynamic history epoch（仅 handoff）
}

// Submit 把 legacy transaction 提交到 bound port；返回 receipt。
func (a *LegacyTransactionAdapter) Submit(ctx context.Context, tx LegacyTransaction) OutputReceipt {
	if a == nil || a.Port == nil {
		return OutputReceipt{
			Admission: AdmissionReceipt{
				Decision:   AdmissionRejected,
				ErrorClass: DeliveryErrorInvalid,
				Message:    "legacy adapter has no bound port",
			},
		}
	}
	intent := RenderIntent{
		IntentID: randomID("lt"),
		Kind:     tx.Kind,
		Source:   "legacy_adapter",
		Cause:    tx.Cause,
		Bytes:    tx.Bytes,
	}
	if tx.Epoch != nil {
		ep := *tx.Epoch
		intent.HistoryEpoch = &ep
	}
	return a.Port.Submit(ctx, intent)
}

// ImmediateAdapter 是 allowlisted immediate effect adapter；只允许声明
// legacy_immediate/alternate_* 等低风险 effect（Phase 3+ 使用）。
type ImmediateAdapter struct {
	Port      RenderSubmitPort
	Allowlist map[TransactionKind]bool
}

// ImmediateEffect 是一次显式 immediate effect。
type ImmediateEffect struct {
	Kind  TransactionKind
	Bytes []byte
	Lease LeaseIdentity // alternate 相关；若无 lease 为空
}

// LeaseIdentity 是 alternate lease 的身份。
type LeaseIdentity struct {
	LeaseID   uint64
	SessionID string
}

// SubmitEffect 提交 immediate effect。
func (a *ImmediateAdapter) SubmitEffect(ctx context.Context, e ImmediateEffect) OutputReceipt {
	if a == nil || a.Port == nil {
		return OutputReceipt{
			Admission: AdmissionReceipt{
				Decision:   AdmissionRejected,
				ErrorClass: DeliveryErrorInvalid,
				Message:    "immediate adapter has no bound port",
			},
		}
	}
	if a.Allowlist != nil && !a.Allowlist[e.Kind] {
		return OutputReceipt{
			Admission: AdmissionReceipt{
				Decision:   AdmissionRejected,
				ErrorClass: DeliveryErrorInvalid,
				Message:    "kind not in immediate allowlist: " + string(e.Kind),
			},
		}
	}
	intent := RenderIntent{
		IntentID: randomID("ie"),
		Kind:     e.Kind,
		Source:   "immediate_adapter",
		Cause:    "explicit immediate effect",
		Bytes:    e.Bytes,
	}
	return a.Port.Submit(ctx, intent)
}
