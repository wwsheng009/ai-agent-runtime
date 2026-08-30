package ui

import (
	"context"
	"io"
	"sync"

	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
)

// ============================================================================
// Phase 3：legacy surface → gateway binding（8.2）
// ============================================================================

// LegacySurfaceBinding 把 legacy surface 的 flush/handoff/prompt 输出绑定到
// 一个 generation-fenced gateway port。绑定存在时，一次 flush 提交一笔
// primary transaction；未绑定时回落 process TerminalOutput()（启动前
// process-compat），active session 期间由调用方负责保持绑定非空。
type LegacySurfaceBinding struct {
	Registry  *outputpkg.SessionBindingRegistry
	SessionID string
	ref       outputpkg.SessionBindingRef
	refMu     sync.RWMutex
	bound     bool
}

// NewLegacySurfaceBinding 创建绑定（初始未绑定）。
func NewLegacySurfaceBinding(registry *outputpkg.SessionBindingRegistry, sessionID string) *LegacySurfaceBinding {
	return &LegacySurfaceBinding{Registry: registry, SessionID: sessionID}
}

// Bind 绑定到 port（registry 内部递增 generation、fence 旧 facade）。
func (b *LegacySurfaceBinding) Bind(port outputpkg.RenderSubmitPort) {
	if b == nil || b.Registry == nil {
		return
	}
	ref := b.Registry.Bind(b.SessionID, port)
	b.refMu.Lock()
	b.ref = ref
	b.bound = true
	b.refMu.Unlock()
}

// Unbind 解绑并 fence 旧 facade。
func (b *LegacySurfaceBinding) Unbind() {
	if b == nil || b.Registry == nil {
		return
	}
	b.Registry.Unbind(b.SessionID)
	b.refMu.Lock()
	b.bound = false
	b.refMu.Unlock()
}

// boundRef 返回当前 binding ref（无锁拷贝）。
func (b *LegacySurfaceBinding) boundRef() (outputpkg.SessionBindingRef, bool) {
	if b == nil {
		return outputpkg.SessionBindingRef{}, false
	}
	b.refMu.RLock()
	defer b.refMu.RUnlock()
	return b.ref, b.bound
}

// LegacySurfaceWriter 是 legacy surface 用的 io.Writer：一次 Write 包装成
// 一笔 immediate transaction（白名单 kind 由调用方在构造时固定）。它不触达
// os.Stdout；binding 失效时返回稳定错误。
//
// 当前生产调用方经 FlushLegacySurfaceBytes/LegacyFlushRunner 走 flush 路径；
// 本 writer 保留作为 Phase 4 的 immediate writer 接入点（title/bell/pager
// 单次写），避免即时删除后 Phase 4 再重建。
type LegacySurfaceWriter struct {
	Binding  *LegacySurfaceBinding
	Kind     outputpkg.TransactionKind
	Source   string
	geometry GeometryState
}

// NewLegacySurfaceWriter 创建 surface 专用 immediate writer。
func NewLegacySurfaceWriter(binding *LegacySurfaceBinding, kind outputpkg.TransactionKind, source string) *LegacySurfaceWriter {
	return &LegacySurfaceWriter{Binding: binding, Kind: kind, Source: source}
}

// SetGeometry 更新 geometry（提交时带 terminal context）。
func (w *LegacySurfaceWriter) SetGeometry(g GeometryState) {
	if w == nil {
		return
	}
	w.geometry = g
}

// Write 提交一笔 legacy_immediate transaction。
func (w *LegacySurfaceWriter) Write(p []byte) (int, error) {
	if w == nil || w.Binding == nil {
		return 0, outputpkg.ErrLegacyNoBinding
	}
	ref, ok := w.Binding.boundRef()
	if !ok {
		return 0, outputpkg.ErrLegacyNoBinding
	}
	adapter := &outputpkg.LegacyImmediateAdapter{
		Binding: ref,
		Kind:    w.Kind,
		Source:  w.Source,
		Terminal: outputpkg.RenderTerminalContext{
			Geometry: outputpkg.TerminalGeometry{Width: w.geometry.Width, Height: w.geometry.Height},
			Profile:  outputpkg.TerminalProfileRef{ID: "ansi", Version: 1},
		},
	}
	return adapter.Write(p)
}

// LegacyFlushRunner 把一次 legacy flush（多处 Write 合并）转成一笔 primary
// transaction（8.2：一个 callback buffer 对应一个 primary batch）。
type LegacyFlushRunner struct {
	Binding  *LegacySurfaceBinding
	Kind     outputpkg.TransactionKind
	Source   string
	geometry GeometryState
}

// NewLegacyFlushRunner 创建 flush runner。
func NewLegacyFlushRunner(binding *LegacySurfaceBinding, kind outputpkg.TransactionKind, source string) *LegacyFlushRunner {
	return &LegacyFlushRunner{Binding: binding, Kind: kind, Source: source}
}

// SetGeometry 更新 geometry。
func (r *LegacyFlushRunner) SetGeometry(g GeometryState) {
	if r == nil {
		return
	}
	r.geometry = g
}

// Run 执行 render 回调（写 session-local buffer），再一次性提交 primary。
// 返回 receipt；gateway 调用前失败返回错误。
func (r *LegacyFlushRunner) Run(ctx context.Context, render func(io.Writer) error) (outputpkg.OutputReceipt, error) {
	if r == nil || r.Binding == nil {
		return outputpkg.OutputReceipt{}, outputpkg.ErrLegacyNoBinding
	}
	ref, ok := r.Binding.boundRef()
	if !ok {
		return outputpkg.OutputReceipt{}, outputpkg.ErrLegacyNoBinding
	}
	adapter := &outputpkg.LegacyTransactionAdapter{
		Binding:    ref,
		LocalLimit: 1 << 20,
	}
	return adapter.Submit(ctx, r.Kind, r.Source,
		outputpkg.RenderTerminalContext{
			Geometry: outputpkg.TerminalGeometry{Width: r.geometry.Width, Height: r.geometry.Height},
			Profile:  outputpkg.TerminalProfileRef{ID: "ansi", Version: 1},
		}, nil, render)
}

// IsUncertainWriteError 复用 output 包的 helper，供 ui 层识别 uncertain
// 写入（fence projection 而非 retry）。
func IsUncertainWriteError(err error) outputpkg.UncertainWriteError {
	return outputpkg.EnsureUncertainWriteError(err)
}

// FlushLegacySurfaceBytes 提交一笔包含已编码 bytes 的 legacy flush
// （内部把 bytes 经 adapter 提交，一次 primary）。
func FlushLegacySurfaceBytes(ctx context.Context, binding *LegacySurfaceBinding,
	kind outputpkg.TransactionKind, source string, geometry GeometryState, bytes string,
) (outputpkg.OutputReceipt, error) {
	if binding == nil {
		return outputpkg.OutputReceipt{}, outputpkg.ErrLegacyNoBinding
	}
	ref, ok := binding.boundRef()
	if !ok {
		return outputpkg.OutputReceipt{}, outputpkg.ErrLegacyNoBinding
	}
	adapter := &outputpkg.LegacyTransactionAdapter{Binding: ref, LocalLimit: 1 << 20}
	return adapter.Submit(ctx, kind, source,
		outputpkg.RenderTerminalContext{
			Geometry: outputpkg.TerminalGeometry{Width: geometry.Width, Height: geometry.Height},
			Profile:  outputpkg.TerminalProfileRef{ID: "ansi", Version: 1},
		}, nil, func(w io.Writer) error {
			_, werr := io.WriteString(w, bytes)
			return werr
		})
}
