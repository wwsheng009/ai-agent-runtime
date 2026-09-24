package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// GET /web/api/mesh/events —— 网格实时事件流（SSE，架构 §5.5 / §6.3 / §6.4）。
//
// 门禁：
//   - 首帧 mesh.ready（连接级，不占 seq，无 id 行）；
//   - `?since_seq=<n>` 跳过 seq<=n 的帧（对齐 /web/api/events 的续传语义）；
//   - `?peers=none` 只收本节点帧（节点间订阅用它避免 N² 转发），默认 auto 并入 peer 帧；
//   - 客户端上限 → 429；网格未启用 → 200 + available=false（绝不 5xx，MN1）。

// meshEventFrame 是测试侧解析出的 SSE 帧（§6.3 信封：外层信封 + data 段）。
type meshEventFrame struct {
	ID      string
	Event   string
	Payload map[string]any // 完整信封：schema_version / seq / ts / source_node_id / type / data
	Body    map[string]any // 信封的 data 段（类型相关载荷）
}

// openMeshEvents 连接事件流并返回帧通道；stop 关闭连接（t.Cleanup 兜底）。
func openMeshEvents(t *testing.T, baseURL, query string) (<-chan meshEventFrame, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+ChatWebAPIMeshEventsPath+query, nil)
	if err != nil {
		cancel()
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("连接事件流失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("事件流状态 = %d, want 200", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		resp.Body.Close()
		cancel()
		t.Fatalf("Content-Type = %q, want text/event-stream", contentType)
	}
	frames := make(chan meshEventFrame, 32)
	go func() {
		defer close(frames)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		var current meshEventFrame
		flush := func() {
			if current.Event == "" {
				return
			}
			select {
			case frames <- current:
			case <-ctx.Done():
			}
			current = meshEventFrame{}
		}
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				flush()
			case strings.HasPrefix(line, "id: "):
				current.ID = strings.TrimSpace(strings.TrimPrefix(line, "id: "))
			case strings.HasPrefix(line, "event: "):
				current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			case strings.HasPrefix(line, "data: "):
				raw := strings.TrimPrefix(line, "data: ")
				if err := json.Unmarshal([]byte(raw), &current.Payload); err == nil {
					if body, ok := current.Payload["data"].(map[string]any); ok {
						current.Body = body
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
	stop := func() {
		cancel()
		// 等待读取 goroutine 退出，避免测试结束时泄漏。
		for range frames {
		}
	}
	t.Cleanup(stop)
	return frames, stop
}

func nextMeshFrame(t *testing.T, frames <-chan meshEventFrame, timeout time.Duration) meshEventFrame {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if !ok {
			t.Fatal("事件流已关闭")
		}
		return frame
	case <-time.After(timeout):
		t.Fatal("等待 mesh 帧超时")
		return meshEventFrame{}
	}
}

func newMeshEventsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(HandleChatWebAPIMeshEvents))
	t.Cleanup(srv.Close)
	return srv
}

func meshFanin(t *testing.T, host *mesh.Host) *mesh.Fanin {
	t.Helper()
	fanin := host.Fanin()
	if fanin == nil || !fanin.Enabled() {
		t.Fatal("测试 host 的扇入必须可用（AICLI_MESH_DIR 指向临时目录）")
	}
	return fanin
}

func TestHandleChatWebAPIMeshEvents_ReadyFrame(t *testing.T) {
	host := meshTestHost(t)
	srv := newMeshEventsServer(t)
	frames, _ := openMeshEvents(t, srv.URL, "")

	ready := nextMeshFrame(t, frames, 3*time.Second)
	if ready.Event != mesh.FrameReady {
		t.Fatalf("首帧 = %q, want %q", ready.Event, mesh.FrameReady)
	}
	if nodeID, _ := ready.Body["node_id"].(string); nodeID != host.NodeID() {
		t.Fatalf("mesh.ready node_id = %q, want %q", nodeID, host.NodeID())
	}
	if schema, _ := ready.Body["frame_schema"].(float64); int(schema) != mesh.FrameSchemaVersion {
		t.Fatalf("mesh.ready frame_schema = %v, want %d", ready.Body["frame_schema"], mesh.FrameSchemaVersion)
	}
	// 连接级帧不占序号：不应出现 id 行。
	if ready.ID != "" {
		t.Fatalf("mesh.ready 不应带 id 行: %q", ready.ID)
	}
}

func TestHandleChatWebAPIMeshEvents_SinceSeqSkipsDelivered(t *testing.T) {
	host := meshTestHost(t)
	fanin := meshFanin(t, host)
	srv := newMeshEventsServer(t)

	first := fanin.PublishLocal(mesh.FramePeerUpdated, map[string]any{"index": 1})
	second := fanin.PublishLocal(mesh.FramePeerUpdated, map[string]any{"index": 2})
	if first == 0 || second != first+1 {
		t.Fatalf("seq 必须单调: first=%d second=%d", first, second)
	}

	frames, _ := openMeshEvents(t, srv.URL, fmt.Sprintf("?since_seq=%d", first))
	if ready := nextMeshFrame(t, frames, 3*time.Second); ready.Event != mesh.FrameReady {
		t.Fatalf("首帧 = %q, want %q", ready.Event, mesh.FrameReady)
	}
	// 续传游标（first）之前的帧不再重放：流上第一条业务帧必须是新发布的帧。
	third := fanin.PublishLocal(mesh.FrameSessionChanged, map[string]any{"state": "busy"})
	frame := nextMeshFrame(t, frames, 3*time.Second)
	if frame.Event != mesh.FrameSessionChanged {
		t.Fatalf("帧 = %+v, want mesh.session.changed（seq<=since_seq 必须被跳过）", frame)
	}
	if frame.ID != strconv.FormatUint(third, 10) {
		t.Fatalf("id = %q, want %d", frame.ID, third)
	}
	if seq, _ := frame.Payload["seq"].(float64); uint64(seq) != third {
		t.Fatalf("data.seq = %v, want %d", frame.Payload["seq"], third)
	}
	if source, _ := frame.Payload["source_node_id"].(string); source != host.NodeID() {
		t.Fatalf("source_node_id = %q, want %q", source, host.NodeID())
	}
}

func TestHandleChatWebAPIMeshEvents_PeersNoneDropsPeerFrames(t *testing.T) {
	host := meshTestHost(t)
	fanin := meshFanin(t, host)
	srv := newMeshEventsServer(t)

	peerFrame := func(eventType string, seq uint64) mesh.Frame {
		return mesh.Frame{Type: eventType, SourceNodeID: "node-peer", Seq: seq, Data: map[string]any{"turn_id": "turn-1"}}
	}
	if !fanin.PublishPeerFrame("node-peer", peerFrame("turn_start", 5)) {
		t.Fatal("peer 白名单事件必须并入扇入")
	}

	// peers=none：节点间订阅模式，只收本节点自产帧（避免 N² 转发）。
	frames, stop := openMeshEvents(t, srv.URL, "?peers=none")
	ready := nextMeshFrame(t, frames, 3*time.Second)
	if ready.Event != mesh.FrameReady {
		t.Fatalf("首帧 = %q, want %q", ready.Event, mesh.FrameReady)
	}
	if peers, _ := ready.Body["peers"].(string); peers != "none" {
		t.Fatalf("mesh.ready peers = %q, want none", peers)
	}
	localSeq := fanin.PublishLocal(mesh.FramePeerUpdated, map[string]any{"state": "busy"})
	if !fanin.PublishPeerFrame("node-peer", peerFrame("turn_end", 6)) {
		t.Fatal("peer 帧必须并入扇入（auto 订阅者可见）")
	}
	frame := nextMeshFrame(t, frames, 3*time.Second)
	if frame.Event != mesh.FramePeerUpdated || frame.ID != strconv.FormatUint(localSeq, 10) {
		t.Fatalf("peers=none 只应收本节点帧, got %+v", frame)
	}
	select {
	case extra := <-frames:
		if extra.Event == mesh.FramePeerEvent {
			t.Fatalf("peers=none 不得出现 peer 帧: %+v", extra)
		}
	case <-time.After(300 * time.Millisecond):
	}
	stop()

	// 默认 auto：同一 peer 帧以 mesh.peer.event 并入本节点流。
	frames, _ = openMeshEvents(t, srv.URL, "")
	nextMeshFrame(t, frames, 3*time.Second) // mesh.ready
	// 流不重放历史（对齐 /web/api/events 的续传语义）：连接后再发一帧 peer 事件。
	if !fanin.PublishPeerFrame("node-peer", peerFrame("turn_end", 7)) {
		t.Fatal("peer 帧必须并入扇入")
	}
	seen := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for !seen[mesh.FramePeerEvent] {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("事件流已关闭")
			}
			seen[frame.Event] = true
		case <-deadline:
			t.Fatalf("auto 模式未收到 peer 帧: %v", seen)
		}
	}
}

func TestHandleChatWebAPIMeshEvents_DisabledDegrades(t *testing.T) {
	prev := mesh.Current()
	mesh.SetCurrent(nil)
	t.Cleanup(func() { mesh.SetCurrent(prev) })

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshEvents(rec, httptest.NewRequest(http.MethodGet, ChatWebAPIMeshEventsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("网格关闭时状态 = %d, want 200（绝不 5xx）", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if available, _ := body["available"].(bool); available {
		t.Fatalf("网格关闭时 available 必须为 false: %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIMeshEvents_MethodNotAllowed(t *testing.T) {
	host := meshTestHost(t)
	_ = host
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshEvents(rec, httptest.NewRequest(http.MethodPost, ChatWebAPIMeshEventsPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态 = %d, want 405", rec.Code)
	}
}

func TestChatWebMeshSubscribeStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{mesh.ErrFaninClientLimit, http.StatusTooManyRequests},
		{mesh.ErrFaninDisabled, http.StatusServiceUnavailable},
		{mesh.ErrFaninClosed, http.StatusServiceUnavailable},
		{errors.New("boom"), http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		if got := chatWebMeshSubscribeStatus(tc.err); got != tc.want {
			t.Fatalf("chatWebMeshSubscribeStatus(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// 客户端上限：既有连接不受影响，第 N+1 个连接被拒绝（§6.4）。
func TestHandleChatWebAPIMeshEvents_ClientLimitRejectsNewConnections(t *testing.T) {
	host := meshTestHost(t)
	fanin := meshFanin(t, host)
	// 直接占满扇入名额（默认 32 太重，这里按上限逐条订阅后释放）。
	const fillers = mesh.DefaultFaninMaxClients
	clients := make([]*mesh.FaninClient, 0, fillers)
	for i := 0; i < fillers; i++ {
		client, err := fanin.Subscribe()
		if err != nil {
			t.Fatalf("第 %d 个订阅失败: %v", i+1, err)
		}
		clients = append(clients, client)
	}
	defer func() {
		for _, client := range clients {
			client.Close()
		}
	}()

	srv := newMeshEventsServer(t)
	resp, err := http.Get(srv.URL + ChatWebAPIMeshEventsPath)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("超限状态 = %d, want 429", resp.StatusCode)
	}
	// 既有连接不受影响：扇入仍可发布并送达。
	seq := fanin.PublishLocal(mesh.FramePeerUpdated, map[string]any{"state": "busy"})
	if seq == 0 {
		t.Fatal("既有连接期间扇入必须继续工作")
	}
}
