package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// 本文件是网格实时事件流端点（SSE，架构 §5.5 / §6），对应实施方案 S7。
//
// 契约（§6.3 / §6.4 / §6.5）：
//
//   - 浏览器只连自己的进程：peer 事件由本节点扇入后并入这条流，浏览器不需要
//     也不应该直连其它进程（单连接、单令牌、单信任边界）；
//   - 帧 = `id: <seq>` + `event: <type>` + `data: {schema_version, seq, ts,
//     source_node_id, type, data}`；`mesh.peer.event` 的内层保留 peer 原始帧；
//   - seq 与 mesh journal 同源同计数器（由 internal/mesh.Fanin 从
//     Journal.NextSeq 取号），因此流上的序号可与 journal 行直接对齐；
//   - `?since_seq=<n>` 跳过 seq<=n 的帧。语义与既有 /web/api/events 一致：流
//     本身不重放历史，续传靠消费方按 §6.4 重新拉一次全量视图兜底；
//   - `?peers=none` 只收本节点自产帧（节点间订阅固定用该模式，避免 N² 转发）；
//     默认 `auto` = 本节点帧 + 已并入的 peer 帧；
//   - 客户端上限 32（可配）：超限 429，不影响既有连接；
//   - 单连接缓冲溢出 → `mesh.lagged` 帧（附 skipped 计数）。
//
// 降级（§4.7）：网格未启用时返回 200 + available=false，绝不 5xx；peer 订阅
// 失败只影响实时性，视图与 call 目标解析照常（§6.2）。

const (
	// chatWebMeshEventsKeepaliveInterval 是无事件时发送 `: keepalive` 注释的周期，
	// 与既有 /web/api/events 的 15s 保持一致（§5.9 超时表）。
	chatWebMeshEventsKeepaliveInterval = 15 * time.Second
	// chatWebMeshEventsLaggedInterval 是 lagged 提示的检查周期：即使没有新帧，
	// 也要让消费方知道「刚才有帧被丢了」。
	chatWebMeshEventsLaggedInterval = 1 * time.Second
)

// HandleChatWebAPIMeshEvents 提供网格实时事件流（SSE，架构 §5.5）。
//
// 这是**独立端点**：节点间订阅固定用 `?peers=none`（避免 N² 转发，§5.5），
// 非浏览器调用方（脚本、E2E、远程节点）也用它。浏览器默认走
// `/web/api/events?mesh=1` 的合并流——同一条 SSE 承载主事件与网格帧，避免每个
// 页面占用两条 HTTP/1.1 连接（浏览器单 host 并发上限约 6 条，见 §5.6 单连接契约）。
// 两条路径共用 attachChatWebMeshStream，订阅语义逐字一致。
func HandleChatWebAPIMeshEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "error",
			"reason": "method not allowed: use GET " + ChatWebAPIMeshEventsPath,
		})
		return
	}
	hub := chatWebMeshFanin()
	if hub == nil || !hub.Enabled() {
		// --mesh=false 时路由通常不注册（§9.7）；真被调用到也只降级不报错。
		writeWebAPIJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    "mesh disabled",
		})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// 订阅必须先于任何字节写出：失败要回 JSON 状态码（§6.4 的 429/503 映射），
	// 而 SSE 响应头一旦写出就改不回来。attach 只把 mesh.ready 入队（不碰 socket），
	// 所以这里可以先建 stream、再订阅、最后才设头并启动 writer。
	stream := newChatWebSSEStream(w, flusher)
	sub, err := attachChatWebMeshStream(stream, chatWebMeshStreamOptions{
		SinceSeq:  chatWebMeshSinceSeq(r),
		PeersMode: chatWebMeshPeersMode(r),
		// 独立端点没有别的心跳来源，keepalive 由订阅自己发。
		Keepalive: true,
	})
	if err != nil {
		writeWebAPIJSON(w, chatWebMeshSubscribeStatus(err), map[string]any{
			"status":         "error",
			"code":           "mesh_stream_unavailable",
			"message":        err.Error(),
			"node_id":        hub.NodeID(),
			"schema_version": mesh.FrameSchemaVersion,
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// handler 返回前必须先停掉 writer goroutine（与 /web/api/events 同一约束：
	// ResponseWriter 在 handler 返回后由 net/http 收尾）。
	defer func() {
		stream.Close()
		<-stream.writerDone
	}()
	// mesh 流没有 EventBus 订阅，死流回调为空（订阅解绑由 run 的 defer 完成）。
	stream.start(nil)

	// 事件泵直接占用本 handler goroutine：帧只做非阻塞入队，退出路径为
	// ctx 取消（客户端断开）/ 流判死 / 订阅关闭（进程退出）。
	sub.run(ctx)
}

// ---------------------------------------------------------------------------
// 扇入订阅：独立端点与合并流（/web/api/events?mesh=1）共用实现
// ---------------------------------------------------------------------------

// chatWebMeshStreamOptions 是一条网格扇入订阅的过滤与承载参数。
type chatWebMeshStreamOptions struct {
	// SinceSeq 是续传游标：跳过 seq<=SinceSeq 的帧（0 = 不过滤，§6.4）。
	SinceSeq uint64
	// PeersMode 是订阅拓扑：auto（本节点 + 已并入的 peer 帧）| none（只收本节点）。
	PeersMode string
	// Merged 表示帧并入 /web/api/events（`?mesh=1`）。只影响 mesh.ready 的回显
	// 字段，供前端与诊断区分「合并流」与「独立端点」。
	Merged bool
	// Keepalive 表示由本订阅自己发 `: keepalive`（独立端点）。合并流由主循环
	// 统一发心跳，避免同一条连接上出现两份 keepalive。
	Keepalive bool
}

// peersOnly 报告是否只保留本节点自产帧。
func (o chatWebMeshStreamOptions) peersOnly() bool {
	return chatWebMeshPeersModeName(o.PeersMode) == "none"
}

// chatWebMeshStreamSubscription 是一条已建立的扇入订阅。
//
// 生命周期：attachChatWebMeshStream 订阅（失败返回错误且无副作用）→ run 泵帧
// → run 返回时解绑（client.Close）。帧只经 stream 的非阻塞入队，因此 run 既可
// 跑在独立 goroutine（合并流），也可直接占用 handler goroutine（独立端点）。
type chatWebMeshStreamSubscription struct {
	hub    *mesh.Fanin
	client *mesh.FaninClient
	opts   chatWebMeshStreamOptions
	stream *chatWebSSEStream
}

// chatWebMeshFanin 返回本进程的扇入枢纽（网格未启用时返回 nil，§4.7 降级）。
func chatWebMeshFanin() *mesh.Fanin {
	host := mesh.Current()
	if host == nil {
		return nil
	}
	return host.Fanin()
}

// chatWebMeshNodeID 返回本节点 ID（网格未启用时为空串，用于降级提示帧）。
func chatWebMeshNodeID() string {
	if hub := chatWebMeshFanin(); hub != nil {
		return hub.NodeID()
	}
	return ""
}

// chatWebMeshMergeRequested 解析 `?mesh=1|true|yes|on`：请求把网格帧并入
// /web/api/events 这条单连接（Web 子方案 §5.6）。缺省/其它值 = 不合并（历史行为）。
func chatWebMeshMergeRequested(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mesh"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// chatWebMeshPeersModeName 归一化订阅拓扑名（未知/空值按 auto，§5.5）。
func chatWebMeshPeersModeName(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "none") {
		return "none"
	}
	return "auto"
}

// attachChatWebMeshStream 建立一条扇入订阅并写好 mesh.ready 首帧（§6.3 / §6.4）。
//
// 返回错误时无任何副作用（未订阅、未入队）：调用方可以安全地改回 JSON 错误响应
// （独立端点）或补一条降级提示帧（合并流）。错误分类见 chatWebMeshSubscribeStatus。
func attachChatWebMeshStream(stream *chatWebSSEStream, opts chatWebMeshStreamOptions) (*chatWebMeshStreamSubscription, error) {
	hub := chatWebMeshFanin()
	if hub == nil || !hub.Enabled() {
		return nil, mesh.ErrFaninDisabled
	}
	client, err := hub.Subscribe()
	if err != nil {
		return nil, err
	}
	sub := &chatWebMeshStreamSubscription{hub: hub, client: client, opts: opts, stream: stream}
	// 首帧 mesh.ready：对齐既有流的 connected 语义，客户端据此确认已接入，并看到
	// 本次订阅生效的模式（since_seq / peers / 当前客户端数 / 是否合并流）。
	sub.writeConnectionFrame(mesh.FrameReady, map[string]any{
		"node_id":      hub.NodeID(),
		"clients":      hub.Clients(),
		"since_seq":    opts.SinceSeq,
		"peers":        chatWebMeshPeersModeName(opts.PeersMode),
		"merged":       opts.Merged,
		"frame_schema": mesh.FrameSchemaVersion,
		"resume_hint":  "since_seq 不重放历史：跳号时重新拉一次 /web/api/mesh/peers",
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
	return sub, nil
}

// writeConnectionFrame 入队一条连接级帧（mesh.ready / mesh.lagged：无 seq，
// 不广播、不带续传游标，§6.3）。
func (s *chatWebMeshStreamSubscription) writeConnectionFrame(frameType string, data map[string]any) {
	if s == nil || s.hub == nil || s.stream == nil {
		return
	}
	frame, ok := s.hub.ConnectionFrame(frameType, data)
	if !ok {
		return
	}
	payload, err := mesh.MarshalFrame(frame)
	if err != nil {
		return
	}
	id := ""
	if frame.Seq > 0 {
		id = strconv.FormatUint(frame.Seq, 10)
	}
	s.stream.writeRawEvent(frame.Type, id, payload)
}

// run 是订阅的事件泵，直到 ctx 取消 / 流判死 / 订阅关闭（进程退出）才返回。
//
// 帧只做非阻塞入队（writeRawEvent：队列满即丢帧并计数），既不阻塞发布者也不阻塞
// handler；返回前一定解绑订阅，避免扇入客户端泄漏到上限（§6.4，默认 32）。
func (s *chatWebMeshStreamSubscription) run(ctx context.Context) {
	if s == nil || s.client == nil || s.stream == nil {
		return
	}
	defer s.client.Close()

	// keepalive 只在独立端点启用；nil channel 的 select 分支永不就绪（合并流由
	// /web/api/events 的主循环统一发心跳）。
	var keepaliveC <-chan time.Time
	if s.opts.Keepalive {
		keepalive := time.NewTicker(chatWebMeshEventsKeepaliveInterval)
		defer keepalive.Stop()
		keepaliveC = keepalive.C
	}
	// lagged 提示即使没有新帧也要发：让消费方知道「刚才有帧被丢了」（§6.4）。
	lagged := time.NewTicker(chatWebMeshEventsLaggedInterval)
	defer lagged.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stream.Done():
			// writer 已因写入失败/超时判死：直接退出（defer 里解绑订阅）。
			return
		case <-s.client.Done():
			// 扇入关闭（进程退出路径）：退出，避免空转。
			return
		case message, ok := <-s.client.Payloads():
			if !ok {
				return
			}
			if s.opts.peersOnly() && !chatWebMeshFrameIsLocal(message.Payload, s.hub.NodeID()) {
				continue
			}
			if seq := chatWebMeshFrameSeq(message.Payload); seq > 0 && seq <= s.opts.SinceSeq {
				continue
			}
			s.stream.writeRawEvent(message.Type, message.ID, message.Payload)
		case <-lagged.C:
			if skipped := s.client.TakeLagged(); skipped > 0 {
				s.writeConnectionFrame(mesh.FrameLagged, map[string]any{
					"node_id": s.hub.NodeID(),
					"skipped": skipped,
					"hint":    "重新拉 /web/api/mesh/peers 做全量兜底",
				})
			}
		case <-keepaliveC:
			s.stream.keepalive()
		}
	}
}

// chatWebMeshSubscribeStatus 把扇入订阅错误映射为 HTTP 状态（§6.4）：
// 客户端上限 → 429（不影响既有连接），其余（禁用/已关闭）→ 503。
func chatWebMeshSubscribeStatus(err error) int {
	switch {
	case errors.Is(err, mesh.ErrFaninClientLimit):
		return http.StatusTooManyRequests
	default:
		return http.StatusServiceUnavailable
	}
}

// chatWebMeshSinceSeq reads `?since_seq=` (invalid values mean "from the start").
func chatWebMeshSinceSeq(r *http.Request) uint64 {
	if r == nil {
		return 0
	}
	raw := strings.TrimSpace(r.URL.Query().Get("since_seq"))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// chatWebMeshPeersMode reads `?peers=auto|none` (default auto, §5.5).
func chatWebMeshPeersMode(r *http.Request) string {
	if r == nil {
		return "auto"
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("peers"))) {
	case "none":
		return "none"
	case "auto", "":
		return "auto"
	default:
		return "auto"
	}
}

// chatWebMeshFrameSeq extracts the seq of a rendered frame (0 when absent).
// The frame is already valid JSON produced by internal/mesh.
func chatWebMeshFrameSeq(payload []byte) uint64 {
	var envelope struct {
		Seq uint64 `json:"seq"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return 0
	}
	return envelope.Seq
}

// chatWebMeshFrameIsLocal reports whether a frame was produced by this node
// (`?peers=none` drops everything a peer contributed).
func chatWebMeshFrameIsLocal(payload []byte, nodeID string) bool {
	var envelope struct {
		SourceNodeID string `json:"source_node_id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return false
	}
	return strings.TrimSpace(envelope.SourceNodeID) == strings.TrimSpace(nodeID)
}

// ---------------------------------------------------------------------------
// 本进程本地事件 → 网格扇入（S7，§6.3 扇入白名单）
// ---------------------------------------------------------------------------

// chatWebMeshRelayInterval 是重新解析本地 EventBus 的周期：会话切换/重建会换
// 总线实例，与 /web/api/events 的重新订阅循环（§8.5）同一节奏。
const chatWebMeshRelayInterval = 2 * time.Second

// StartChatWebMeshLocalRelay 把本进程的白名单运行时事件发布进网格扇入。
//
// 为什么需要它：peer 订阅到的是**本节点的本地事件**（§6.3 防环），再由 peer 包
// 一层 `mesh.peer.event` 并入 peer 自己的流。没有这条 relay，peer 只能看到档案类
// 帧（joined / updated / session.changed），看不到 turn 边界。
//
// 只转发白名单（`turn.*` / `session.*` 且非 delta，§6.3），事件名与浏览器看到的
// SSE 事件名一致（chatWebSSEEventName）。返回停止函数；mesh 不可用（--mesh=false
// 或扇入未启用）时返回 no-op，调用方无需分支（§4.7）。
func StartChatWebMeshLocalRelay(host *mesh.Host) func() {
	if host == nil {
		return func() {}
	}
	fanin := host.Fanin()
	if fanin == nil || !fanin.Enabled() {
		return func() {}
	}
	stop := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		var (
			curBus *runtimeevents.Bus
			unsub  runtimeevents.Unsubscribe = func() {}
		)
		defer func() { unsub() }()
		subscribe := func(bus *runtimeevents.Bus) {
			if bus == curBus {
				return
			}
			unsub()
			unsub = func() {}
			if bus != nil {
				unsub = bus.SubscribeCancelable("", func(ev runtimeevents.Event) {
					name, _ := chatWebSSEEventName(ev.Type)
					if !mesh.IsRelayWhitelistType(name) {
						return
					}
					// PublishLocal 非阻塞：扇入满/关闭时丢帧并计数，绝不阻塞
					// 事件发布者（agent / UI actor / 停滞看门狗）。
					fanin.PublishLocal(name, chatWebSSEDataForEvent(ev))
				})
			}
			curBus = bus
		}
		subscribe(chatWebMeshLocalBus())
		ticker := time.NewTicker(chatWebMeshRelayInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				subscribe(chatWebMeshLocalBus())
			}
		}
	}()
	return func() { stopOnce.Do(func() { close(stop) }) }
}

// chatWebMeshLocalBus 解析当前会话的进程级事件总线（无会话时为 nil）。
func chatWebMeshLocalBus() *runtimeevents.Bus {
	session := chatWebSession()
	if session == nil || session.LocalRuntimeHost == nil {
		return nil
	}
	return session.LocalRuntimeHost.EventBus
}
