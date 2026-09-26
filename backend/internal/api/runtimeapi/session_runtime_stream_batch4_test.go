package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// Batch 4 · 体验收口的回归护栏（方案 §4 Batch 4 表）：
//  1. 回放帧标 replay:true（初始 dump 与断点补齐），实时帧标 live:true —— 两者互斥，
//     客户端据此区分「历史补齐」与「刚刚发生」，不必再猜 isResponding；
//  2. 断点续传起始帧 `: resumed from=N to=M`：只在带游标且确实补到新行时下发，
//     from 取游标下一行、to 取补齐后的最高 seq（含跨页跨度）。

const batch4SessionID = "session-runtime-stream-batch4"

// batch4SSEFrame 是断言用的最小 SSE 帧（注释帧只有 comment，业务帧带 event + data）。
type batch4SSEFrame struct {
	comment string
	event   string
	data    map[string]interface{}
}

func TestStreamSessionRuntimeEventsMarksReplayAndResumeSpan(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	for index := 1; index <= 5; index++ {
		_, err := runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
			Type:      chat.EventAssistantMessage,
			SessionID: batch4SessionID,
			TraceID:   "trace-batch4",
			Payload:   map[string]interface{}{"content": fmt.Sprintf("event-%d", index)},
		})
		require.NoErrorf(t, err, "造第 %d 条事件失败", index)
	}

	router := mux.NewRouter()
	router.HandleFunc("/api/runtime/sessions/{id}/runtime/stream", handler.StreamSessionRuntimeEvents).Methods(http.MethodGet)
	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamURL := server.URL + "/api/runtime/sessions/" + batch4SessionID + "/runtime/stream?after=2&live=1&poll_ms=5000"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// 第一段：after=2 的补齐（3 行）+ 续传边界帧。
	resumeBody := readBatch3StreamUntil(t, resp.Body, func(seen string) bool {
		return strings.Contains(seen, ": resumed from=3 to=5")
	})

	// 第二段：连上之后总线直投一条 live-only 进度帧，实时帧必须带 live:true。
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      "tool.progress",
		SessionID: batch4SessionID,
		Payload:   map[string]interface{}{"tool_call_id": "call-batch4", "percent": 42.0},
	})
	liveBody := readBatch3StreamUntil(t, resp.Body, func(seen string) bool {
		return strings.Contains(seen, `"live":true`)
	})

	frames := parseBatch4SSEFrames(resumeBody + liveBody)
	replayFrames := 0
	liveFrames := 0
	resumedSeen := false
	for _, frame := range frames {
		switch frame.comment {
		case "resumed from=3 to=5":
			resumedSeen = true
		}
		if frame.data == nil {
			continue
		}
		if frame.data["replay"] == true {
			replayFrames++
			assert.NotContains(t, frame.data, "live", "回放帧不得同时带 live:true")
			assert.Equal(t, chat.EventAssistantMessage, frame.data["type"])
		}
		if frame.data["live"] == true {
			liveFrames++
			assert.NotContains(t, frame.data, "replay", "实时帧不得带 replay:true")
			assert.Equal(t, "tool.progress", frame.data["type"])
		}
	}
	assert.True(t, resumedSeen, "带游标续传且补到新行时必须下发 `: resumed from=… to=…`：%s", resumeBody+liveBody)
	assert.Equal(t, 3, replayFrames, "after=2 只补齐第 3~5 条，且都必须标 replay:true")
	assert.Equal(t, 1, liveFrames, "总线直投的进度帧必须标 live:true")
}

// parseBatch4SSEFrames 把 SSE 文本切成帧（注释帧只有 comment，业务帧带 event/data）。
func parseBatch4SSEFrames(body string) []batch4SSEFrame {
	frames := make([]batch4SSEFrame, 0, 8)
	for _, block := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		frame := batch4SSEFrame{}
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimRight(line, "\r")
			switch {
			case strings.HasPrefix(line, ": "):
				frame.comment = strings.TrimSpace(strings.TrimPrefix(line, ": "))
			case strings.HasPrefix(line, "event: "):
				frame.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				var data map[string]interface{}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err == nil {
					frame.data = data
				}
			}
		}
		frames = append(frames, frame)
	}
	return frames
}
