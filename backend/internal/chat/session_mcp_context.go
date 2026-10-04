package chat

// 会话级 MCP 覆盖键（runtime-server 管理接口写入的会话元数据）。
//
// 「本会话停用 / 本会话临时启用」由 runtime-server 的 MCP 管理 API 直接写入
// 会话元数据；chat actor 在自己的内存快照上做回合末整行持久化，快照里没有这些
// 键。因此 persistSession 必须像 broker 句柄别名、plan-mode 状态那样，在整行
// 写入前按 store 最新副本合并这些键，否则运行期写入的覆盖会被旧快照抹掉
// （2026-10-04 实测：enable/disable 后名单在下一回合保存时消失）。
const (
	// SessionMCPDisabledContextKey 是「本会话停用」名单的会话元数据键。
	SessionMCPDisabledContextKey = "runtime_mcp_session_disabled"
	// SessionMCPEnabledContextKey 是「本会话临时启用」名单的会话元数据键。
	SessionMCPEnabledContextKey = "runtime_mcp_session_enabled"
)
