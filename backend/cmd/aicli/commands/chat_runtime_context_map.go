package commands

import (
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// ChatSession.RuntimeSession.Metadata.Context（下称「运行时 context map」）是一个
// 普通 map[string]interface{}，却被三族 goroutine 共享：
//
//   - 会话属主 goroutine：命令分发、状态栏刷新、prompt 组合与持久化；
//   - chatRuntimeEventBridge worker goroutine：状态栏重绘 + 长回合 token 锚点回写；
//   - actor/executor goroutine：冻结 system prompt 锚点、plan mode 状态。
//
// Go 的 map 不是并发安全的：同一 map 上任意一处未同步的读与写在同进程内并发发生，
// 都会以 `fatal error: concurrent map read and map write` 直接终止整个 CLI（现场栈：
// sessionmeta.Value -> runtime/maps.fatal，长回合流式输出中 bridge worker 读 token
// 锚点/路由覆盖，同时属主或 actor 线程在写同一 map）。该 fatal 不可 recover，只能靠
// 纪律消除。
//
// 纪律：本包内对运行时 context map 的所有访问都必须走本文件的辅助函数。
//   - 读：chatRuntimeContextMapSnapshot / chatRuntimeContext{String,Int,Bool} /
//     withChatRuntimeSessionRead（外部包自带读逻辑时，如 planmode.Load）；
//   - 写：withRuntimeContextWrite（按需建 map）/ withRuntimeContextEdit（不建 map）/
//     withChatRuntimeSessionWrite（外部包自带写逻辑时，如 planmode.Save）。
//
// 锁是叶子锁：闭包内不得再调用本文件任何加锁辅助函数（sync.RWMutex 不可重入），
// 也不得调用可能回到 ChatSession 锁路径的函数。
//
// 另外：RuntimeSession 指针本身也会被属主 goroutine 在回合中替换
// （syncRuntimeSessionFromChatMode / ensureChatPlanModeRuntimeSession）。指针读写因此
// 同样收口到 chatRuntimeSessionPointer / setChatRuntimeSessionPointer。锁挂在
// ChatSession 上而不是 runtime session 上，所以指针替换（含 Clone 替换）后保护关系
// 依然成立：新旧 map 的访问共用同一把锁。

// chatRuntimeContextLockedMap 返回当前运行时 context map，调用方必须已持锁
// （读或写）。
func chatRuntimeContextLockedMap(session *ChatSession) map[string]interface{} {
	if session == nil || session.RuntimeSession == nil {
		return nil
	}
	return session.RuntimeSession.Metadata.Context
}

// chatRuntimeSessionPointer 在读锁下返回当前 runtime session 指针。
func chatRuntimeSessionPointer(session *ChatSession) *runtimechat.Session {
	if session == nil {
		return nil
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	return session.RuntimeSession
}

// setChatRuntimeSessionPointer 在写锁下替换 runtime session 指针（回合中的
// Clone 替换、恢复、ephemeral plan session 创建都走这里）。
func setChatRuntimeSessionPointer(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil {
		return
	}
	session.runtimeContextMapMu.Lock()
	defer session.runtimeContextMapMu.Unlock()
	session.RuntimeSession = runtimeSession
}

// cloneChatRuntimeSessionWithoutHistory 在读锁下克隆 runtime session。Clone 会复制
// Metadata.Context（整张 map 的读），必须与写方互斥，否则就是并发 map 读+写。
func cloneChatRuntimeSessionWithoutHistory(session *ChatSession) *runtimechat.Session {
	if session == nil {
		return nil
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	if session.RuntimeSession == nil {
		return nil
	}
	return session.RuntimeSession.CloneWithoutHistory()
}

// chatRuntimeSessionID 在读锁下取 runtime session ID（bridge 路由身份等）。
func chatRuntimeSessionID(session *ChatSession) string {
	runtimeSession := chatRuntimeSessionPointer(session)
	if runtimeSession == nil {
		return ""
	}
	return runtimeSession.ID
}

// chatRuntimeContextMapSnapshot 在读锁下返回 context map 的浅拷贝，供需要在锁外做
// 多键读取的调用方使用（避免长时间持锁）。值仍是原引用，与改造前一致。
func chatRuntimeContextMapSnapshot(session *ChatSession) map[string]interface{} {
	if session == nil {
		return nil
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	live := chatRuntimeContextLockedMap(session)
	if live == nil {
		return nil
	}
	snapshot := make(map[string]interface{}, len(live))
	for key, value := range live {
		snapshot[key] = value
	}
	return snapshot
}

// chatRuntimeContextValue 在读锁下按 sessionmeta 候选键语义取单键值。
func chatRuntimeContextValue(session *ChatSession, key string) (interface{}, bool) {
	if session == nil {
		return nil, false
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	return sessionmeta.Value(chatRuntimeContextLockedMap(session), key)
}

// chatRuntimeContextString 在读锁下按 sessionmeta 语义取字符串值。
func chatRuntimeContextString(session *ChatSession, key string) string {
	if session == nil {
		return ""
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	live := chatRuntimeContextLockedMap(session)
	if live == nil {
		return ""
	}
	return sessionmeta.String(live, key)
}

// chatRuntimeContextInt 在读锁下按 sessionmeta 语义取 int 值。
func chatRuntimeContextInt(session *ChatSession, key string) (int, bool) {
	if session == nil {
		return 0, false
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	live := chatRuntimeContextLockedMap(session)
	if live == nil {
		return 0, false
	}
	return sessionmeta.Int(live, key)
}

// chatRuntimeContextBool 在读锁下按 sessionmeta 语义取 bool 值。
func chatRuntimeContextBool(session *ChatSession, key string) (bool, bool) {
	if session == nil {
		return false, false
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	live := chatRuntimeContextLockedMap(session)
	if live == nil {
		return false, false
	}
	return sessionmeta.Bool(live, key)
}

// chatRuntimeContextRawValue 在读锁下逐字取单键值（不做候选键解析），语义与
// runtimeSessionContextRawInt 一致：锚点原值读取必须绕开 canonical 优先顺序。
func chatRuntimeContextRawValue(session *ChatSession, key string) (interface{}, bool) {
	if session == nil {
		return nil, false
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	live := chatRuntimeContextLockedMap(session)
	if live == nil {
		return nil, false
	}
	value, ok := live[key]
	if !ok || value == nil {
		return nil, false
	}
	return value, true
}

// chatRuntimeContextRawInt 在读锁下逐字读取单个 context 键（不做别名解析），见
// runtimeSessionContextRawInt 的注释说明。
func chatRuntimeContextRawInt(session *ChatSession, key string) (int, bool) {
	value, ok := chatRuntimeContextRawValue(session, key)
	if !ok {
		return 0, false
	}
	return sessionmeta.Int(map[string]interface{}{key: value}, key)
}

// chatRuntimeContextHasKey 在读锁下判断键是否存在（结构化锚点探测）。
func chatRuntimeContextHasKey(session *ChatSession, key string) bool {
	_, ok := chatRuntimeContextRawValue(session, key)
	return ok
}

// ensureChatRuntimeContextMap 在写锁下补齐缺失的 context map；runtime session
// 不存在时是 no-op。
func ensureChatRuntimeContextMap(session *ChatSession) {
	if session == nil {
		return
	}
	session.runtimeContextMapMu.Lock()
	defer session.runtimeContextMapMu.Unlock()
	if session.RuntimeSession == nil {
		return
	}
	if session.RuntimeSession.Metadata.Context == nil {
		session.RuntimeSession.Metadata.Context = make(map[string]interface{})
	}
}

// withRuntimeContextWrite 在写锁下改写 context map，缺失时先创建（写意图路径）。
func withRuntimeContextWrite(session *ChatSession, mutate func(ctx map[string]interface{})) {
	if session == nil || mutate == nil {
		return
	}
	session.runtimeContextMapMu.Lock()
	defer session.runtimeContextMapMu.Unlock()
	if session.RuntimeSession == nil {
		return
	}
	if session.RuntimeSession.Metadata.Context == nil {
		session.RuntimeSession.Metadata.Context = make(map[string]interface{})
	}
	mutate(session.RuntimeSession.Metadata.Context)
}

// withRuntimeContextEdit 在写锁下改写已存在的 context map，不创建（删除/覆写路径；
// 保持「无 map == 无键」的既有语义）。
func withRuntimeContextEdit(session *ChatSession, mutate func(ctx map[string]interface{})) {
	if session == nil || mutate == nil {
		return
	}
	session.runtimeContextMapMu.Lock()
	defer session.runtimeContextMapMu.Unlock()
	ctx := chatRuntimeContextLockedMap(session)
	if ctx == nil {
		return
	}
	mutate(ctx)
}

// withChatRuntimeSessionRead 在读锁下把 runtime session 交给外部包的读逻辑
// （如 planmode.Load 自行读同一张 map）。
func withChatRuntimeSessionRead(session *ChatSession, read func(runtimeSession *runtimechat.Session)) {
	if session == nil || read == nil {
		return
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	if session.RuntimeSession == nil {
		return
	}
	read(session.RuntimeSession)
}

// withChatRuntimeSessionWrite 在写锁下把 runtime session 交给外部包的写逻辑
// （如 planmode.Save 自行写同一张 map）。闭包不得再进入本文件的加锁辅助函数。
func withChatRuntimeSessionWrite(session *ChatSession, write func(runtimeSession *runtimechat.Session)) {
	if session == nil || write == nil {
		return
	}
	session.runtimeContextMapMu.Lock()
	defer session.runtimeContextMapMu.Unlock()
	if session.RuntimeSession == nil {
		return
	}
	write(session.RuntimeSession)
}

// trimChatRuntimeContextString 小工具：把 map 里的值按 sessionmeta 语义转字符串。
func trimChatRuntimeContextString(ctx map[string]interface{}, key string) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(sessionmeta.String(ctx, key))
}

// chatRuntimeSessionSnapshot 在读锁下返回一个与共享对象脱钩的 runtime session 快照
// （CloneWithoutHistory：不带历史，context map 为独立副本）。展示/投影类只读路径把
// 它交给既有的纯函数，从而不必在锁内做字符串拼装、也不触碰共享 map。
//
// 注意：快照只保证 map 访问安全；runtime session 的其它可变字段（State/UpdatedAt）
// 仍可能被其它 goroutine 改写，快照读到的值可能略旧——与改造前这些路径的读一致，
// 但不再有 fatal 风险。
func chatRuntimeSessionSnapshot(session *ChatSession) *runtimechat.Session {
	if session == nil {
		return nil
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	if session.RuntimeSession == nil {
		return nil
	}
	return session.RuntimeSession.CloneWithoutHistory()
}

// chatRuntimeSessionRead 在读锁下把 live runtime session 交给 read，并取回锁内算出的
// 结果；ok=false 表示当前没有 runtime session，由调用方决定退化语义。
//
// 与 chatRuntimeSessionSnapshot 的分工：快照是 CloneWithoutHistory，map 脱钩但**不含
// 历史**，只能喂「只读 context / Metadata 字段」的纯函数。凡是需要历史的读（轮数统计、
// 派生标题 BuildPreview、CompactRootTitleCandidate）必须走本函数——用快照会静默读到
// 空历史，表现为确认行变成 "0轮/N条消息" 或标题丢失。
//
// 本函数是 HEAD 里「直接读 session.RuntimeSession」那些路径在锁下的等价形态：read 只能
// 读，且不得再进入本文件任何加锁辅助函数（叶子锁不可重入）。
func chatRuntimeSessionRead[T any](session *ChatSession, read func(runtimeSession *runtimechat.Session) T) (T, bool) {
	var zero T
	if session == nil || read == nil {
		return zero, false
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	if session.RuntimeSession == nil {
		return zero, false
	}
	return read(session.RuntimeSession), true
}

// chatRuntimeContextAnchoredInt 在单次读锁内解析 token 锚点，语义与
// runtimeSessionAnchoredContextInt 完全一致（见 chat_token_usage.go 的说明）。
func chatRuntimeContextAnchoredInt(session *ChatSession) (int, string, bool) {
	if session == nil {
		return 0, "", false
	}
	session.runtimeContextMapMu.RLock()
	defer session.runtimeContextMapMu.RUnlock()
	return runtimeContextAnchoredInt(chatRuntimeContextLockedMap(session))
}
