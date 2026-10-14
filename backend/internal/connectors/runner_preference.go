// runner_preference（D2，docs/bots/09 §2/§7.2 R3）的配置键与选举纯逻辑：
// `connectors.runner_preference` 是 config.yaml connectors 节的**同级全局键**
//（非 per-connector；per-connector `policy.runner_preference` 覆盖位在 §10.4
// 预留，本切片不实现）。值域：runtime-server（默认）| aicli | round-robin。
//
// 消费方是无绑定入站的 runner 选举（R3）：poller 在派发前用
// PickRunnerForUnbound 在候选执行体中选一个。当前派发通道是各宿主本地
// Backend（无跨节点派发），选举器在 P2 mesh turn 派发落地时被真正接线；
// 本文件先锁词表与排序语义，避免三宿主各自手写选举漂移。
package connectors

import "strings"

// Runner 选举偏好取值（connectors.runner_preference）。
const (
	// RunnerPreferenceRuntimeServer：默认。优先任一活着的 runtime-server 节点，
	// 没有则 aicli（与 D2 文案一致）。
	RunnerPreferenceRuntimeServer = "runtime-server"
	// RunnerPreferenceAICLI：优先 aicli 节点，没有则 runtime-server。
	RunnerPreferenceAICLI = "aicli"
	// RunnerPreferenceRoundRobin：在候选间轮转（按候选稳定排序后循环）。
	RunnerPreferenceRoundRobin = "round-robin"
)

// RunnerPreferenceDefault 是配置缺省值（未配置 = runtime-server 优先）。
const RunnerPreferenceDefault = RunnerPreferenceRuntimeServer

// NormalizeRunnerPreference 归一化配置值：大小写不敏感、未知/空白回落默认；
// 返回归一化后的规范词（恒非空）。
func NormalizeRunnerPreference(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RunnerPreferenceAICLI:
		return RunnerPreferenceAICLI
	case RunnerPreferenceRoundRobin:
		return RunnerPreferenceRoundRobin
	case RunnerPreferenceRuntimeServer, "":
		return RunnerPreferenceDefault
	default:
		// 未知值不报错（配置向后兼容：旧/手写配置不因拼错而炸启动），
		// 归一到默认并在调用方日志面提示。
		return RunnerPreferenceDefault
	}
}

// RunnerPreferenceStore 是宿主存储对全局 runner 选举偏好的**可选**能力接口
//（不进 Store 主接口，避免破坏既有实现；storefile/storejson 已实现）。
// restapi 的 /connectors/runner-preference 端点经它读写；宿主存储未实现时
// 端点返回 501（能力未装配，不伪装成功）。
type RunnerPreferenceStore interface {
	// RunnerPreference 读取全局偏好；恒返回规范词（未配置/未知值归一默认）。
	RunnerPreference() string
	// SetRunnerPreference 写入偏好；空串/默认值=清除（零值不落盘）。
	SetRunnerPreference(preference string) error
}

// runnerCandidate 是一次选举的候选执行体视图（P2 mesh peers 或本地宿主自身）。
type runnerCandidate struct {
	// HostKind 是执行体身份：RunnerPreferenceRuntimeServer / RunnerPreferenceAICLI
	// （gateway 是 poller 不是 runner，不作为候选）。
	HostKind string
	// NodeID 是 mesh 节点 id（P2 派发目标）；本地执行为空。
	NodeID string
	// Alive 报告节点当前是否在线（stale 快照不算）。
	Alive bool
}

// PickRunnerForUnbound 按偏好从候选中选出无绑定入站的执行体（R3，D2）：
//   - runtime-server 偏好：第一个活着的 runtime-server，否则第一个活着 aicli；
//   - aicli 偏好：对称（aicli 优先，回落 runtime-server）；
//   - round-robin：把活候选按（HostKind, NodeID）稳定排序后循环取下一个
//     （轮转指针由调用方持有——本函数是无状态纯函数，next 索引入参）；
//   - 无活候选返回 false（调用方按"无可用执行体"处理，不猜）。
//
// 候选顺序不做隐式重排：函数内先做稳定分区（偏好组在前），同组保持入参顺序。
func PickRunnerForUnbound(preference string, candidates []runnerCandidate, rrCursor int) (runnerCandidate, int, bool) {
	pref := NormalizeRunnerPreference(preference)
	alive := make([]runnerCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Alive && (c.HostKind == RunnerPreferenceRuntimeServer || c.HostKind == RunnerPreferenceAICLI) {
			alive = append(alive, c)
		}
	}
	if len(alive) == 0 {
		return runnerCandidate{}, rrCursor, false
	}
	if pref == RunnerPreferenceRoundRobin {
		// 稳定排序保证同候选集下轮转顺序确定（NodeID 唯一，作次级键）。
		sortRunnerCandidates(alive)
		idx := ((rrCursor % len(alive)) + len(alive)) % len(alive)
		return alive[idx], rrCursor + 1, true
	}
	primary, fallback := RunnerPreferenceRuntimeServer, RunnerPreferenceAICLI
	if pref == RunnerPreferenceAICLI {
		primary, fallback = RunnerPreferenceAICLI, RunnerPreferenceRuntimeServer
	}
	for _, c := range alive {
		if c.HostKind == primary {
			return c, rrCursor, true
		}
	}
	for _, c := range alive {
		if c.HostKind == fallback {
			return c, rrCursor, true
		}
	}
	// 候选里有非 primary/fallback 的 HostKind（防御：调用方过滤已收紧）——
	// 不选，保持无猜测语义。
	return runnerCandidate{}, rrCursor, false
}

// sortRunnerCandidates 按（HostKind, NodeID）稳定排序（round-robin 的确定性
// 依赖此序：同一候选集任何调用顺序产生同一轮转环）。
func sortRunnerCandidates(candidates []runnerCandidate) {
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && runnerCandidateLess(candidates[j], candidates[j-1]); j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
}

func runnerCandidateLess(a, b runnerCandidate) bool {
	if a.HostKind != b.HostKind {
		return a.HostKind < b.HostKind
	}
	return a.NodeID < b.NodeID
}
