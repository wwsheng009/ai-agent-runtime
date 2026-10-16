package commands

import (
	"sync"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// TestChatRuntimeContextMap_ConcurrentReadersAndWriters 是 2026-10 线上
// `fatal error: concurrent map read and map write` 的回归护栏：
//
//	sessionmeta.Value -> runtime/maps.fatal
//	runtimeSessionContextString <- chatSessionRoutingOverrideRaw
//	<- chatRuntimeEventBridge.run（bridge worker 重绘状态栏）
//
// 现场是 bridge worker 读 session.RuntimeSession.Metadata.Context，同时另一
// goroutine（属主命令路径 / actor prepare-run / plan mode）写同一张 map。fatal
// 不可 recover，所以这里用多读多写并发长时间跑：
//   - 加了 -race 时，任何漏锁的 map 访问都会被 race detector 直接指出；
//   - 未加 -race 时，漏锁写的回归也会以进程级 fatal 终止本测试。
//
// 读侧只允许快照/加锁辅助函数，写侧只允许 withRuntimeContext* /
// withChatRuntimeSession*（含指针替换）。本测试故意不触碰裸 map。
func TestChatRuntimeContextMap_ConcurrentReadersAndWriters(t *testing.T) {
	session := &ChatSession{RuntimeSession: runtimechat.NewSession("tester")}

	const (
		readerCount = 4
		writerCount = 3
		iterations  = 400
	)

	done := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < readerCount; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				// 状态栏/路由投影的读路径：快照、单键读、session 快照、ID。
				_ = chatRuntimeContextMapSnapshot(session)
				_ = chatRuntimeContextString(session, sessionmeta.WorkspacePath)
				_, _ = chatRuntimeContextInt(session, chatRuntimeContextContextTokenCount)
				_, _ = chatRuntimeContextRawValue(session, chatRuntimeContextTokenCount)
				_ = chatRuntimeSessionID(session)
				_ = chatRuntimeSessionSnapshot(session)
			}
		}()
	}

	var writers sync.WaitGroup
	for i := 0; i < writerCount; i++ {
		writers.Add(1)
		go func(seed int) {
			defer writers.Done()
			for j := 0; j < iterations; j++ {
				withRuntimeContextWrite(session, func(ctx map[string]interface{}) {
					ctx[sessionmeta.WorkspacePath] = "E:/projects/ws"
					ctx[chatRuntimeContextContextTokenCount] = j
				})
				withRuntimeContextEdit(session, func(ctx map[string]interface{}) {
					delete(ctx, "transient")
				})
				// 外部包自带写逻辑的路径（compact lineage 会写 5 个 context 键）。
				withChatRuntimeSessionWrite(session, func(runtimeSession *runtimechat.Session) {
					runtimeSession.ApplyCompactTitleLineage("parent-1", "root")
				})
				if seed == 0 && j%16 == 0 {
					// 回合中的指针替换（syncRuntimeSessionFromChatMode 的 Clone 路径）。
					setChatRuntimeSessionPointer(session, cloneChatRuntimeSessionWithoutHistory(session))
				}
			}
		}(i)
	}

	writers.Wait()
	close(done)
	readers.Wait()

	if got := chatRuntimeContextString(session, sessionmeta.WorkspacePath); got != "E:/projects/ws" {
		t.Fatalf("unexpected workspace context after concurrent access: %q", got)
	}
}

// TestChatRuntimeContextMapSnapshot_IsDetached 固化辅助函数的语义：快照与共享
// map 完全脱钩，改快照不影响线上对象（读侧无需在锁内做字符串处理）。
func TestChatRuntimeContextMapSnapshot_IsDetached(t *testing.T) {
	session := &ChatSession{RuntimeSession: runtimechat.NewSession("tester")}
	withRuntimeContextWrite(session, func(ctx map[string]interface{}) {
		ctx[sessionmeta.WorkspacePath] = "E:/ws/one"
	})

	ctxSnapshot := chatRuntimeContextMapSnapshot(session)
	ctxSnapshot[sessionmeta.WorkspacePath] = "E:/ws/two"
	ctxSnapshot["extra"] = true
	if got := chatRuntimeContextString(session, sessionmeta.WorkspacePath); got != "E:/ws/one" {
		t.Fatalf("context snapshot leaked into live map: %q", got)
	}
	if _, ok := chatRuntimeContextRawValue(session, "extra"); ok {
		t.Fatalf("context snapshot write leaked into live map")
	}

	sessionSnapshot := chatRuntimeSessionSnapshot(session)
	if sessionSnapshot == nil {
		t.Fatalf("expected runtime session snapshot")
	}
	sessionSnapshot.Metadata.Context[sessionmeta.WorkspacePath] = "E:/ws/three"
	if got := chatRuntimeContextString(session, sessionmeta.WorkspacePath); got != "E:/ws/one" {
		t.Fatalf("session snapshot leaked into live map: %q", got)
	}
}
