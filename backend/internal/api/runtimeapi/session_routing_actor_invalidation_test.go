package runtimeapi

// A6（server 平面）回归：路由写入撞上在途 turn 时绝不打断——旧 actor 保留到本轮结束，
// 由命令入口边界兑现重建，写入自下一轮生效（响应如实带 warning）。
// 事故与归因链见 docs/analysis/a6-host-stop-attribution-and-inflight-refresh-20260927.md：
// 旧实现无条件 hub.StopContext → actor.cancelActive(actor_stop) 取消在途 run，
// 用户只看到一句 context canceled。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// sessionRoutingInFlightPatchBody 是一个合法的 session 层路由补丁（与 U-1 同形）。
const sessionRoutingInFlightPatchBody = `{"target_layer":"session","updated_by":"web","main_agent":{"enabled":true,"levels":["easy","normal","hard"],"profiles":{"hard":{"provider":"anthropic","model":"claude-opus-4","reasoning_effort":"high"}}}}`

func TestSessionRoutingPatchInFlightTurnIsNotInterrupted(t *testing.T) {
	ctx := context.Background()
	handler, runtime, _ := newProfileSwitchTestHandler(t)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	blocking := &blockingProfileSwitchProvider{
		testLLMProvider: testLLMProvider{name: "blocking-model", content: "ok"},
		started:         make(chan struct{}, 1),
		release:         make(chan struct{}),
	}
	require.NoError(t, runtime.RegisterProvider("blocking-model", blocking))

	session, err := handler.sessionManager.Create(ctx, "user-routing-inflight-write")
	require.NoError(t, err)
	session.SetContext(sessionmeta.ProviderName, "blocking-model")
	session.SetContext(sessionmeta.Model, "blocking-model")
	require.NoError(t, handler.sessionManager.Update(ctx, session))

	hub := handler.getSessionHub()
	actor, err := hub.GetOrCreate(session.ID)
	require.NoError(t, err)
	require.NoError(t, actor.SubmitPromptAsync(ctx, "hi", nil))
	select {
	case <-blocking.started:
	case <-time.After(10 * time.Second):
		t.Fatal("在途回合未起跑")
	}
	require.True(t, actor.RunInFlight(), "回合必须处于在途状态")

	rec := doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, sessionRoutingInFlightPatchBody, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload sessionRoutingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.True(t, payload.Updated)
	assert.True(t, payload.ActorInvalidated, "写入仍必须自下一轮生效")
	assert.Contains(t, strings.Join(payload.Warnings, "\n"), "在途 turn",
		"在途写入必须如实说明自下一轮入口重建后生效，否则是假开关")
	if _, ok := hub.Get(session.ID); !ok {
		t.Fatal("在途 actor 不得被驱逐（A6：绝不打断在途回合）")
	}
	require.True(t, handler.hasPendingActorRebuild(session.ID), "在途写入必须留下延迟重建标记")
	assert.Equal(t, sessionActorRebuildReasonRoutingWrite, handler.pendingActorRebuildReason(session.ID))

	close(blocking.release)
	deadline := time.Now().Add(10 * time.Second)
	for actor.RunInFlight() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.False(t, actor.RunInFlight(), "回合必须正常结束，不能被路由写入打断")

	// 命令入口边界兑现：actor 空闲后驱逐，使紧随其后的 GetOrCreate 按新路由重建。
	handler.reconcilePendingActorRebuild(session.ID)
	if _, ok := hub.Get(session.ID); ok {
		t.Fatal("边界兑现后旧 actor 必须被驱逐，使下一轮按新路由重建")
	}
	assert.False(t, handler.hasPendingActorRebuild(session.ID), "兑现后标记必须清除")
}

func TestSessionRoutingPatchEvictsIdleActor(t *testing.T) {
	ctx := context.Background()
	handler, _, _ := newProfileSwitchTestHandler(t)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	session, err := handler.sessionManager.Create(ctx, "user-routing-idle-write")
	require.NoError(t, err)
	require.NoError(t, handler.sessionManager.Update(ctx, session))

	hub := handler.getSessionHub()
	_, err = hub.GetOrCreate(session.ID)
	require.NoError(t, err)

	rec := doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, sessionRoutingInFlightPatchBody, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload sessionRoutingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.True(t, payload.ActorInvalidated)
	assert.NotContains(t, strings.Join(payload.Warnings, "\n"), "在途 turn",
		"空闲路径不得报告延迟生效")
	if _, ok := hub.Get(session.ID); ok {
		t.Fatal("空闲 actor 必须立即驱逐，使下一轮按新路由重建")
	}
	assert.False(t, handler.hasPendingActorRebuild(session.ID), "空闲路径不应留下延迟标记")
}
