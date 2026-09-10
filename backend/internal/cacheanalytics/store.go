package cacheanalytics

// RequestStore 终态记录持久化接口（方案 §375 Phase 3：session_runtime.sqlite
// 只读镜像表 cache_requests）。
//
// 依赖方向约束（types.go 包注释）：本包不 import cmd 层与 internal/api 层，
// 因此 sqlite 实现放在 internal/chat.SQLiteRuntimeStore 上，由挂载方
// （cmd/aicli/commands/chat_cache_local.go、internal/api/skills）以接口注入，
// 与 HistoryLookup 同模式。
//
// 语义约定：
//   - SaveRequest 幂等（按 llm_request_id upsert）：同一请求重复终态事件
//     （collector 幂等回放、进程重放）不产生重复行；
//   - 记录终态后不可变（message id 回填除外，见 types.go CacheRequestRecord），
//     回填发生在查询期（LiveSource + HistoryLookup），不落库——重启后回放的
//     记录由同一查询期路径再次回填，聚合不受影响；
//   - LoadSessionRequests 返回按 started_at 升序的该会话全部记录，供
//     Projector 幂等回放（projector.append 按 llm_request_id 去重）。
type RequestStore interface {
	// SaveRequest 幂等写入一条终态记录。实现应为 best-effort 镜像：
	// 写入失败不阻塞在线投影（调用方忽略错误）。
	SaveRequest(record CacheRequestRecord) error
	// LoadSessionRequests 加载指定会话的全部持久化记录（started_at 升序）。
	// 会话无记录时返回空切片而非错误。
	LoadSessionRequests(sessionID string) ([]CacheRequestRecord, error)
}
