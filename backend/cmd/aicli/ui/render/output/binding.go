package output

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ============================================================================
// SessionBinding（8.1/8.2）
// ============================================================================

// SessionBindingRef 是稳定 binding：SessionID + BindingGeneration + 绑定期间
// 的提交端口。binding 在两阶段切换时保持同一 generation；新旧 route 共用旧
// generation 直到 Commit（Phase 5 完整语义）。
//
// Port 是 generation-fenced facade，不是裸 gateway：unbind/close 会 fence 所有
// 旧 facade；stale generation 的提交被 admission fence 拒绝，绝不动态解析
// "当前 session"。fencedPort 在 Submit 前检查 fenced 标志——这是唯一的
// generation fence 机制（gateway 不参与 generation 校验）。
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

// SessionBindingRegistry 管理 {SessionID, BindingGeneration} 的绑定生命周期：
// bind 创建只暴露 RenderSubmitPort 的不可变 facade；每次 bind/unbind 递增
// generation；unbind/close fence 所有旧 facade。gateway 本身不校验
// generation——fence 全部由 fencedPort 承担（8.2 接入顺序 1）。
type SessionBindingRegistry struct {
	mu       sync.Mutex
	nextGen  uint64
	bindings map[string]*fencedPort // key = sessionID
}

// NewSessionBindingRegistry 创建 registry。
func NewSessionBindingRegistry() *SessionBindingRegistry {
	return &SessionBindingRegistry{bindings: map[string]*fencedPort{}}
}

// Bind 绑定 session 到 port；返回 generation-fenced facade。同 session
// 重复 bind 递增 generation 并使旧 facade 失效。
func (r *SessionBindingRegistry) Bind(sessionID string, port RenderSubmitPort) SessionBindingRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Rebinding the same session supersedes the previous facade.  Fence it
	// before replacing the registry entry so callers retaining the old
	// SessionBindingRef fail closed instead of dynamically writing through the
	// new session binding.
	if old, ok := r.bindings[sessionID]; ok {
		old.fence()
	}
	r.nextGen++
	ref := SessionBindingRef{
		SessionID:         sessionID,
		BindingGeneration: r.nextGen,
		Port:              newFencedPort(r, sessionID, r.nextGen, port),
	}
	r.bindings[sessionID] = ref.Port.(*fencedPort)
	return ref
}

// Unbind 使指定 session 的全部旧 facade 失效（fence）。幂等。
func (r *SessionBindingRegistry) Unbind(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fp, ok := r.bindings[sessionID]; ok {
		fp.fence()
		delete(r.bindings, sessionID)
	}
}

// UnbindFenceAll 使所有旧 facade 失效（shutdown）。
func (r *SessionBindingRegistry) UnbindFenceAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, fp := range r.bindings {
		fp.fence()
	}
	r.bindings = map[string]*fencedPort{}
}

// fencedPort 是 generation-fenced RenderSubmitPort facade。Submit 前校验
// generation；fence 后（或 generation 不匹配）返回 pre-admission rejected。
type fencedPort struct {
	reg        *SessionBindingRegistry
	sessionID  string
	generation uint64
	port       RenderSubmitPort
	mu         sync.RWMutex
	fenced     bool
}

func newFencedPort(reg *SessionBindingRegistry, sessionID string, gen uint64, port RenderSubmitPort) *fencedPort {
	return &fencedPort{reg: reg, sessionID: sessionID, generation: gen, port: port}
}

func (p *fencedPort) fence() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fenced = true
}

func (p *fencedPort) Submit(ctx context.Context, intent RenderIntent) OutputReceipt {
	p.mu.RLock()
	fenced := p.fenced
	port := p.port
	p.mu.RUnlock()
	if fenced || port == nil {
		return OutputReceipt{
			Admission: AdmissionReceipt{
				Decision:   AdmissionRejected,
				ErrorClass: DeliveryErrorClosed,
				Message:    "session binding is fenced (unbound or superseded)",
			},
		}
	}
	return port.Submit(ctx, intent)
}

// ============================================================================
// LegacyTransactionAdapter（8.2）
// ============================================================================

// LegacyTransactionAdapter 用于 legacy flush/handoff/prompt repaint：
// 先完整编码，再一次 Submit。encode 只写 session-local bounded buffer，
// 不触达 terminal；一次 flush/handoff 只调用一次 primary。
//
// encode/local-limit 或结构上 nil/unusable binding 在 gateway 调用前失败时，
// Submit 返回 zero OutputReceipt 加 non-nil error；一旦调用 gateway，则返回
// 其完整 receipt，adapter-level error 为 nil，target/admission 失败只从
// receipt 读取。
type LegacyTransactionAdapter struct {
	Binding SessionBindingRef
	// LocalLimit 是 encode 缓冲区的硬上限（fail closed）。
	LocalLimit int
}

// ErrLegacyEncodeFailed / ErrLegacyBufferLimit 是在 gateway 调用前失败的
// 稳定 adapter error（不产生 gateway receipt）。
var (
	ErrLegacyEncodeFailed = errors.New("legacy transaction encode failed")
	ErrLegacyBufferLimit  = errors.New("legacy transaction buffer limit exceeded")
	ErrLegacyNoBinding    = errors.New("legacy transaction has no bound session")
)

// Submit 先 encode 到 bounded buffer，再构造一笔 intent 提交 primary。
// terminal/historyEpoch 由调用点显式提供；sequence/route/target 由 gateway
// 盖章。返回完整 OutputReceipt；adapter-level error 仅在 gateway 未调用时
// 非 nil。
func (a *LegacyTransactionAdapter) Submit(
	ctx context.Context,
	kind TransactionKind,
	source string,
	terminal RenderTerminalContext,
	historyEpoch *uint64,
	encode func(io.Writer) error,
) (OutputReceipt, error) {
	if a == nil || a.Binding.Port == nil {
		return OutputReceipt{}, ErrLegacyNoBinding
	}
	// encode 只写 session-local bounded buffer。
	limit := a.LocalLimit
	if limit <= 0 {
		limit = 64 << 10
	}
	limited := &limitedBuffer{max: limit}
	if err := encode(limited); err != nil {
		return OutputReceipt{}, errors.Join(ErrLegacyEncodeFailed, err)
	}
	if limited.truncated {
		return OutputReceipt{}, ErrLegacyBufferLimit
	}
	if len(limited.buf) == 0 {
		// 空 transaction：无 bytes，但仍推进 context barrier。
		if kind != TransactionContextBarrier {
			return OutputReceipt{}, ErrLegacyEncodeFailed
		}
	}
	intent := RenderIntent{
		IntentID:     randomID("lt"),
		Kind:         kind,
		Source:       source,
		Cause:        "legacy_transaction",
		Bytes:        limited.buf,
		Terminal:     terminal,
		HistoryEpoch: historyEpoch,
	}
	return a.Binding.Port.Submit(ctx, intent), nil
}

// limitedBuffer 是 bounded 编码缓冲；超限标 truncated（fail closed）。
// Write 不返回错误：encode 回调可能忽略错误继续写，limit 判定统一由
// Submit 在 encode 结束后基于 truncated 标志完成（区分 encode 失败与
// buffer 超限两种稳定错误）。
type limitedBuffer struct {
	buf       []byte
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := b.max - len(b.buf)
	if len(p) > room {
		if room > 0 {
			b.buf = append(b.buf, p[:room]...)
		}
		b.truncated = true
		return len(p), nil // 如实返回 len(p)（被记为入账但已标 truncated）
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// ============================================================================
// LegacyImmediateAdapter（8.2）
// ============================================================================

// UncertainWriteError 表示 target-level unknown 的 immediate write：调用点
// 必须用 errors.As 识别 batch/target 并 fence projection，禁止普通 writer
// retry loop。
type UncertainWriteError interface {
	ClassifiedDeliveryError
	BatchID() string
	ProjectionTargetID() string
	AcceptedBytes() int
}

type uncertainWriteError struct {
	class              DeliveryErrorClass
	batchID            string
	projectionTargetID string
	acceptedBytes      int
	msg                string
}

func (e *uncertainWriteError) Error() string                     { return e.msg }
func (e *uncertainWriteError) DeliveryClass() DeliveryErrorClass { return e.class }
func (e *uncertainWriteError) BatchID() string                   { return e.batchID }
func (e *uncertainWriteError) ProjectionTargetID() string        { return e.projectionTargetID }
func (e *uncertainWriteError) AcceptedBytes() int                { return e.acceptedBytes }

// LegacyImmediateAdapter 只用于白名单审计的、天然单次 immediate write 的
// 旧接口。operation kind、source 与 terminal context 在 adapter 创建时固定，
// 每次 Write 包装成一个 TransactionLegacyImmediate intent；不在 Write 中
// 直接调用 os.Stdout，也不在 binding 失效时降级到 process writer。
type LegacyImmediateAdapter struct {
	Binding      SessionBindingRef
	Kind         TransactionKind
	Source       string
	Terminal     RenderTerminalContext
	HistoryEpoch *uint64

	mu          sync.Mutex
	lastReceipt OutputReceipt // 线程安全保留仅供诊断，不作 authority
}

// Write 把 p 包装成一笔 immediate intent，按 8.2 映射表转换返回：
//   - AdmissionAccepted + primary Committed/Full → n=len(p), nil
//   - pre-admission deferred/rejected → n=0, 稳定错误
//   - target-level failed-zero/deferred/rejected → n=0, 稳定错误
//   - target-level unknown → clamped accepted count + UncertainWriteError
//   - binding 失效（fenced）→ n=0, DeliveryErrorClosed
func (a *LegacyImmediateAdapter) Write(p []byte) (int, error) {
	if a == nil || a.Binding.Port == nil {
		return 0, ErrLegacyNoBinding
	}
	// 空 Kind 兜底为 legacy_immediate（局部变量，避免写字段的并发 race；
	// 结构体字面量构造路径也覆盖）。
	kind := a.Kind
	if kind == "" {
		kind = TransactionLegacyImmediate
	}
	intent := RenderIntent{
		IntentID:     randomID("lim"),
		Kind:         kind,
		Source:       a.Source,
		Cause:        "legacy_immediate",
		Bytes:        append([]byte(nil), p...),
		Terminal:     a.Terminal,
		HistoryEpoch: a.HistoryEpoch,
	}
	receipt := a.Binding.Port.Submit(context.Background(), intent)
	a.mu.Lock()
	a.lastReceipt = receipt
	a.mu.Unlock()
	if receipt.Primary == nil {
		// pre-admission rejection/defer。
		class := receipt.Admission.ErrorClass
		if class == "" {
			class = DeliveryErrorInvalid
		}
		return 0, NewClassifiedError(class, "legacy immediate pre-admission rejected: "+receipt.Admission.Message)
	}
	prim := receipt.Primary
	switch prim.Status {
	case DeliveryCommitted:
		return len(p), nil
	case DeliveryUnknownPartial:
		accepted := prim.AcceptedBytes
		if accepted > len(p) {
			accepted = len(p)
		}
		return accepted, &uncertainWriteError{
			class:              prim.ErrorClass,
			batchID:            prim.BatchID,
			projectionTargetID: prim.ProjectionTargetID,
			acceptedBytes:      accepted,
			msg:                "legacy immediate write is uncertain (partial)",
		}
	default: // failed-zero/deferred/rejected
		class := prim.ErrorClass
		if class == "" {
			class = DeliveryErrorInvalid
		}
		return 0, NewClassifiedError(class, "legacy immediate write failed: "+string(prim.Status))
	}
}

// LastReceipt 返回最近一次提交的 receipt（诊断用，不作 authority）。
func (a *LegacyImmediateAdapter) LastReceipt() OutputReceipt {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastReceipt
}

// EnsureUncertainWriteError 供调用方把任意 error 转换为
// UncertainWriteError；非 uncertain 时返回 nil。
func EnsureUncertainWriteError(err error) UncertainWriteError {
	var ue UncertainWriteError
	if errors.As(err, &ue) {
		return ue
	}
	return nil
}
