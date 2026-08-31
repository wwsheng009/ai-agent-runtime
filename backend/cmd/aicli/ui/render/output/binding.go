package output

import (
	"context"
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
	old := r.bindings[sessionID]
	r.nextGen++
	ref := SessionBindingRef{
		SessionID:         sessionID,
		BindingGeneration: r.nextGen,
		Port:              newFencedPort(r, sessionID, r.nextGen, port),
	}
	r.bindings[sessionID] = ref.Port.(*fencedPort)
	r.mu.Unlock()

	// Never wait for an old facade while holding the registry-wide lock.  An
	// already admitted submission may call unrelated registry control code;
	// keeping r.mu here would turn that callback into a lock-order cycle.
	// The replacement is already linearized above, while fence() still makes
	// Bind's return the old generation's completion fence.
	if old != nil {
		old.fence()
	}
	return ref
}

// Unbind 使指定 session 的全部旧 facade 失效（fence）。幂等。
func (r *SessionBindingRegistry) Unbind(sessionID string) {
	r.mu.Lock()
	fp := r.bindings[sessionID]
	if fp != nil {
		delete(r.bindings, sessionID)
	}
	r.mu.Unlock()
	if fp != nil {
		fp.fence()
	}
}

// UnbindFenceAll 使所有旧 facade 失效（shutdown）。
func (r *SessionBindingRegistry) UnbindFenceAll() {
	r.mu.Lock()
	old := make([]*fencedPort, 0, len(r.bindings))
	for _, fp := range r.bindings {
		old = append(old, fp)
	}
	r.bindings = map[string]*fencedPort{}
	r.mu.Unlock()
	for _, fp := range old {
		fp.fence()
	}
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
	defer p.mu.RUnlock()
	if p.fenced || p.port == nil {
		return OutputReceipt{
			Admission: AdmissionReceipt{
				Decision:   AdmissionRejected,
				ErrorClass: DeliveryErrorClosed,
				Message:    "session binding is fenced (unbound or superseded)",
			},
		}
	}
	// Keep the read-side lease until the underlying submission returns.
	// Bind/Unbind take the write lock in fence(), so once either lifecycle
	// operation returns no previously retained facade can still enter (or be
	// executing in) its old port.
	// Carry the registry-owned generation through an unexported intent field.
	// This preserves the facade fence while preventing a producer from
	// forging BindingGeneration in a public RenderIntent literal.
	intent.bindingGeneration = p.generation
	return p.port.Submit(ctx, intent)
}
