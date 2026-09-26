package runtimeapi

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// SetSubagentBatchStore installs the host-owned durable batch control plane.
//
// 缺口背景（2026-09-16）：API 宿主此前没有共享的 batch store，每个
// newAPIAgent* 创建的 agent 都会在首次后台 batch 时惰性创建一份自己的
// 一次性内存 store。后果是 P0-B 进度投影在 API 侧永远读不到数据：digest 的
// Progress 通道没有可读的控制面，父会话在 runtime-server 上只能看到终态
// lifecycle 行（那是由 projector 单独落库的），看不到"batch 3/5 完成、谁还在跑"。
//
// runtime-server 可以在配置了数据目录时注入 file-backed store（跨重启可见）；
// 未注入时宿主回落到进程内默认 store，行为仍严格优于"每个 agent 一份"。
func (h *Handler) SetSubagentBatchStore(store subagentbatch.BatchStore) {
	if h == nil {
		return
	}
	h.subagentBatchMu.Lock()
	h.subagentBatchStore = store
	h.subagentBatchTried = true
	h.subagentBatchMu.Unlock()
	// G2：控制面一旦可见就把它接成 wake 的账本/进度投影（resume 上下文的数据源）。
	// 锁已释放后再接线，避免与 wireSupervisionSources 里的另一把读锁互相阻塞。
	h.wireSupervisionSources()
}

// getSubagentBatchStore 返回宿主级 batch store，第一次调用时创建进程内默认值。
// 创建失败会缓存"已尝试"标记并返回 nil：unwired 宿主保持改动前行为（没有
// progress 通道，digest 与旧输出逐字节一致），而不是每次 preflight 重试建库。
func (h *Handler) getSubagentBatchStore() subagentbatch.BatchStore {
	if h == nil {
		return nil
	}
	h.subagentBatchMu.Lock()
	defer h.subagentBatchMu.Unlock()
	if h.subagentBatchStore != nil || h.subagentBatchTried {
		return h.subagentBatchStore
	}
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{})
	if err != nil {
		h.subagentBatchTried = true
		return nil
	}
	h.subagentBatchStore = store
	h.subagentBatchTried = true
	return h.subagentBatchStore
}

// peekSubagentBatchStore 只读探测已建好的 batch store，**不触发懒加载**：账本判读
// （wait_agent 的 obligation 视图）是纯读路径，没有 store 就等价于"没有挂起记录"，
// 不该为一次等待在宿主上凭空建库（与 peekSessionHub / peekDurableSessionRuntimeStore
// 同形）。挂起记录一旦写入就必然已经建过 store，因此探测为 nil 时返回空账本是安全的。
func (h *Handler) peekSubagentBatchStore() subagentbatch.BatchStore {
	if h == nil {
		return nil
	}
	h.subagentBatchMu.Lock()
	defer h.subagentBatchMu.Unlock()
	return h.subagentBatchStore
}

// subagentBatchCoordinator 在共享 store 上为单个 agent 组装 coordinator。
//
// 与 CLI 宿主（chat_actor_host.go 的 NewSubagentBatchCoordinator）同形：store 共享、
// scheduler 属于当前 agent、emitter 由 SetSubagentBatchCoordinator 回填 agent 自身的
// runtime-event emitter。projector 也由该方法从 agent 上取，所以调用顺序是
// SetBatchLifecycleProjector → SetSubagentBatchCoordinator。
func (h *Handler) subagentBatchCoordinator(scheduler *agent.SubagentScheduler) *agent.SubagentBatchCoordinator {
	store := h.getSubagentBatchStore()
	if store == nil {
		return nil
	}
	return agent.NewSubagentBatchCoordinator(agent.SubagentBatchCoordinatorConfig{
		Store:     store,
		Scheduler: scheduler,
		// P0-1a/M1: task-level progress write-back is an explicit opt-in
		// (supervision.task_progress_interval, default 0 = no writes), wired
		// from the same host tuning the CLI reads so the two hosts cannot drift.
		TaskProgressInterval: h.supervisionTuning().TaskProgressInterval,
	})
}

// supervisionProgressSource 是 P0-B 的只读投影入口：nil 表示本宿主没有可读的
// batch 控制面，BuildDigest 因而跳过 progress 区块（与改动前一致）。
func (h *Handler) supervisionProgressSource() supervision.ProgressSource {
	if h == nil {
		return nil
	}
	source := supervision.NewBatchProgressSource(h.getSubagentBatchStore())
	batchSource, ok := source.(*supervision.BatchProgressSource)
	if !ok || batchSource == nil {
		return source
	}
	// P0-1c/M7: enrich running-task rows with the host's live-only progress
	// mirror so supervision_descendants / digest last_message matches the CLI
	// host. The mirror is shared with the per-child event subscription; a host
	// that never observed child progress simply reports no message.
	if mirror := h.subagentProgressMirror(); mirror != nil {
		batchSource.Messages = supervision.MirrorProgressMessages{Mirror: mirror}
	}
	return source
}
