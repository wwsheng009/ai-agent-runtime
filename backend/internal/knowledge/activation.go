package knowledge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ActivationOptions 控制 Activate 的副作用。
type ActivationOptions struct {
	// SkipInitialIndex 为 true 时不启动后台首次全量索引。
	// 只读探测、短任务与单测用它避免无谓 IO；TUI / runtime-server 不应设置。
	SkipInitialIndex bool
	// OnIndexDone 在后台首次索引结束（成功或失败）后回调，用于日志与状态面。
	// 回调在后台 goroutine 中执行；为 nil 时静默——错误仍保留在 LastIndex 中，
	// 因此静默不等于丢失。
	OnIndexDone func(IndexResult, error)
}

// Activation 是知识层的一次"接入"：句柄 + 后台索引的生命周期。
//
// 这是 Phase 1 交付 6（接入/激活）的共用原语：`cmd/runtime-server`、`cmd/aicli`
// （cmd/tui）与 ACP 三个入口都用它，避免各自复制"开层—判角色—建索引"的顺序。
//
// 不变量：
//   - `mode=off`（默认）时 Activate 返回 `(nil, nil)`；Activation 的所有方法对
//     nil 安全，接入方**不需要任何分支**，`mode=off` 行为与无知识层逐字节一致。
//   - reader 角色不跑索引（Layer.Index 本身是 no-op），也不写任何东西。
//   - Close 幂等、nil-safe；它会取消并**等待**后台索引退出，避免 store 在索引
//     写事务中途被关闭。
type Activation struct {
	layer *Layer

	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	result IndexResult
	err    error
	ran    bool

	onDone func(IndexResult, error)

	// Phase 2 W2：探索记忆采集器（惰性创建、随 Activation 关闭）。
	recorderMu sync.Mutex
	recorder   *ExplorationRecorder
}

// Activate 打开 workspace 上的知识层，并在本进程是 owner 时后台启动首次全量索引。
//
// workspace 为空且 mode != off 时返回配置错误（与 Config.Validate 一致，不静默降级）；
// 索引失败**不**让 Activate 失败——索引是后台任务，错误经 OnIndexDone / LastIndex 暴露。
func Activate(ctx context.Context, cfg Config, workspace string, opts ActivationOptions) (*Activation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = cfg.WithWorkspace(workspace).Normalize()
	if cfg.Mode == ModeOff {
		// 不建库、不建锁文件、不启动 goroutine：off 路径零副作用。
		return nil, nil
	}
	if strings.TrimSpace(cfg.Workspace) == "" {
		return nil, errors.New("knowledge: workspace must be set when mode is shadow or on")
	}

	layer, err := Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if layer == nil { // 防御：Open 的未来实现若对 off 返回 nil 而非 (nil, nil)
		return nil, nil
	}

	act := &Activation{layer: layer, done: make(chan struct{}), onDone: opts.OnIndexDone}
	if opts.SkipInitialIndex || layer.Role() != RoleOwner {
		close(act.done)
		return act, nil
	}

	indexCtx, cancel := context.WithCancel(ctx)
	act.cancel = cancel
	go func() {
		defer close(act.done)
		result, indexErr := layer.Index(indexCtx)
		act.mu.Lock()
		act.result, act.err, act.ran = result, indexErr, true
		act.mu.Unlock()
		if act.onDone != nil {
			act.onDone(result, indexErr)
		}
	}()
	return act, nil
}

// Layer 返回底层句柄（off 时为 nil）。
func (a *Activation) Layer() *Layer {
	if a == nil {
		return nil
	}
	return a.layer
}

// Config 返回本次接入的生效配置；nil/off 时返回零值 Config。
func (a *Activation) Config() Config {
	if a == nil || a.layer == nil {
		return Config{}
	}
	return a.layer.cfg
}

// Mode 返回生效模式（off 时为 ModeOff）。
func (a *Activation) Mode() Mode {
	if a == nil {
		return ModeOff
	}
	return a.layer.Mode()
}

// Role 返回本进程相对 store 的角色（off 时为 RoleNone）。
func (a *Activation) Role() Role {
	if a == nil {
		return RoleNone
	}
	return a.layer.Role()
}

// Enabled 报告知识层是否参与执行路径。
func (a *Activation) Enabled() bool { return a.Mode() != ModeOff }

// Recorder 返回探索记忆采集器（06 §4 Phase 2 W2）。
//
// 门控与 ShadowObserverFor 同口径：仅 mode=shadow|on 且本进程是 owner 时返回
// 非 nil；off / reader / 未打开 store 时返回 nil（调用方无需分支，nil 上调用
// Record/Flush/Close 都是 no-op，保证零写入）。采集器惰性创建并随本 Activation
// 关闭（Close 会等待其队列排空/取消），同一 Activation 多次调用返回同一实例。
func (a *Activation) Recorder() *ExplorationRecorder {
	if a == nil || a.layer == nil || a.layer.store == nil {
		return nil
	}
	mode := a.layer.Mode()
	if mode != ModeShadow && mode != ModeOn {
		return nil
	}
	if a.layer.Role() != RoleOwner {
		return nil
	}
	a.recorderMu.Lock()
	defer a.recorderMu.Unlock()
	if a.recorder == nil {
		layer := a.layer
		a.recorder = NewExplorationRecorder(ExplorationRecorderConfig{
			Store:          layer.store,
			Workspace:      layer.Workspace(),
			Mode:           mode,
			workspaceIDFor: layer.ensureWorkspace,
		})
	}
	return a.recorder
}

// Workspace 返回被索引的工作区根目录。
func (a *Activation) Workspace() string {
	if a == nil {
		return ""
	}
	return a.layer.Workspace()
}

// DBPath 返回 store 路径；off 时为空。状态面（CLI 状态栏 / HTTP status）用它展示落点。
func (a *Activation) DBPath() string {
	if a == nil {
		return ""
	}
	return a.layer.DBPath()
}

// Stats 返回索引行数汇总；off 或不可用时返回零值。
func (a *Activation) Stats(ctx context.Context) (Stats, error) {
	if a == nil {
		return Stats{}, nil
	}
	return a.layer.Stats(ctx)
}

// IndexRunning 报告后台首次索引是否仍在跑；off / reader / SkipInitialIndex 时为 false。
func (a *Activation) IndexRunning() bool {
	if a == nil {
		return false
	}
	select {
	case <-a.done:
		return false
	default:
		return true
	}
}

// Status 返回知识层状态快照（06 §4 Phase 1 交付 5 的统一载荷）。
//
// 它合并两类事实：Layer 的 store/行数/锁等待（落盘真相）与 Activation 的
// 后台索引生命周期（本进程真相）。off（nil Activation）返回 mode=off 的最小
// 载荷而不是错误——"off"是合法状态，状态面必须能如实回答它。
func (a *Activation) Status(ctx context.Context) (StatusReport, error) {
	if a == nil {
		return StatusReport{Mode: ModeOff, Role: RoleNone, GeneratedAt: time.Now().UnixMilli()}, nil
	}
	report, err := a.layer.Status(ctx)
	if err != nil {
		return report, err
	}
	report.IndexRunning = a.IndexRunning()
	a.mu.Lock()
	ran, indexErr := a.ran, a.err
	a.mu.Unlock()
	if ran && indexErr != nil && report.DegradedReason == "" {
		// 本进程刚跑完的索引失败优先展示：它比落库的 job 行更贴近"现在"。
		report.DegradedReason = "last index failed: " + indexErr.Error()
	}
	return report, nil
}

// WaitIndex 等待后台首次索引结束并返回其结果。
//
// 返回 `(zero, nil)` 表示"没有索引可等"：off、reader 或 SkipInitialIndex。
// ctx 取消时返回 ctx.Err()。
func (a *Activation) WaitIndex(ctx context.Context) (IndexResult, error) {
	if a == nil {
		return IndexResult{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-a.done:
	case <-ctx.Done():
		return IndexResult{}, ctx.Err()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result, a.err
}

// LastIndex 返回后台索引的最近状态：ok=false 表示尚未结束（仍在跑或无索引任务）。
func (a *Activation) LastIndex() (IndexResult, error, bool) {
	if a == nil {
		return IndexResult{}, nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result, a.err, a.ran
}

// Close 取消后台索引、等待其退出，然后释放所有权与 store。幂等且 nil-safe。
func (a *Activation) Close() error {
	if a == nil {
		return nil
	}
	// 先停采集器：它的 worker 可能在写 store，必须在关库之前退出。
	a.recorderMu.Lock()
	recorder := a.recorder
	a.recorder = nil
	a.recorderMu.Unlock()
	if recorder != nil {
		recorder.Close()
	}
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	// 等待后台 goroutine 退出：Index 持写事务，先关 store 会造成锁竞争。
	<-a.done
	return a.layer.Close()
}
