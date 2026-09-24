package mesh

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 本文件是「跨进程扇入」的本节点广播端（architecture §6.1 第 3 层 /
// §6.3 帧格式 / §6.4 背压），对应实施方案 S7：
//
//	peer 节点 --SSE--> 本节点 Fanin --> 本进程的 Web 客户端（浏览器只连自己的进程）
//
// 三条硬约束：
//
//  1. **seq 同源**：帧的 seq 由节点 journal 的同一个计数器分配（§6.3），
//     因此 SSE 帧与 mesh/journal/<node_id>.ndjson 行可直接对齐。
//  2. **防环**：只并入「来源就是该 peer 自己」的帧，且 `mesh.peer.event` /
//     `mesh.lagged` 永不二次转发（§6.3 / §12.1）。
//  3. **不阻塞**：发布路径只做非阻塞入队，队列满即丢帧计数；任何 peer 故障
//     都不得阻塞本进程的事件循环（§6.4）。
//
// 扇入是「实时性的增强」，不是可见性边界：它挂了，peers 全量视图与
// aicli-mesh ls 仍能工作（MN1 / §4.7）。

// SSE 帧类型（architecture §5.5 / §6.3）。
const (
	// FramePeerJoined / FramePeerLeft / FramePeerUpdated 是节点生命周期帧。
	FramePeerJoined  = "mesh.peer.joined"
	FramePeerLeft    = "mesh.peer.left"
	FramePeerUpdated = "mesh.peer.updated"
	// FrameSessionChanged 是会话归属变化（冲突出现/消解）。
	FrameSessionChanged = "mesh.session.changed"
	// FrameCallInvoked / FrameCallCompleted 是本节点被调用 / 调用完成（S8）。
	FrameCallInvoked   = "mesh.call.invoked"
	FrameCallCompleted = "mesh.call.completed"
	// FramePeerEvent 是 peer 的转发事件（内层保留原始 turn.* / session.* 帧）。
	FramePeerEvent = "mesh.peer.event"
	// FrameLagged 是跳号提示：本连接的缓冲溢出，消费方应重新拉全量视图。
	FrameLagged = "mesh.lagged"
	// FrameReady 是流建立后的首帧（对齐 /web/api/events 的 connected 语义）。
	FrameReady = "mesh.ready"
)

// FrameSchemaVersion 是 mesh SSE 帧信封的版本。它与节点档案的 schema_version
// 各自独立：档案管磁盘结构，帧管流结构。
const FrameSchemaVersion = 1

// 扇入默认值（architecture §6.4）。全部可配，默认值面向「本机个位数节点」。
const (
	// DefaultFaninMaxClients 是单节点 SSE 客户端上限：超限拒绝新连接（429），
	// 不影响既有连接。
	DefaultFaninMaxClients = 32
	// DefaultFaninQueueCapacity 是单客户端缓冲帧数上限：超限丢帧并计数，
	// 由 FrameLagged 提示消费方做全量兜底。
	DefaultFaninQueueCapacity = 256
	// DefaultFaninPeerRatePerSec / DefaultFaninPeerBurst 是每 peer 令牌桶：
	// 20 帧/秒、突发 40，超限丢弃并计入 peers 视图的 dropped_events。
	DefaultFaninPeerRatePerSec = 20
	DefaultFaninPeerBurst      = 40
	// DefaultFaninMaxPeers 是单节点同时订阅的 peer 上限（§6.2：超过阈值时
	// 优先订阅同工作区 + 最近活跃的 N 个，其余靠轮询）。
	DefaultFaninMaxPeers = 16
)

// 扇入错误：都是「降级」而不是「崩溃」，调用方按 §4.7 静默处理。
var (
	// ErrFaninDisabled 表示本进程没有可用的网格根（不产帧、不可订阅）。
	ErrFaninDisabled = errors.New("mesh fan-in disabled")
	// ErrFaninClientLimit 表示单节点 SSE 客户端已达上限（HTTP 层映射 429）。
	ErrFaninClientLimit = errors.New("mesh fan-in client limit reached")
	// ErrFaninClosed 表示扇入已关闭（进程退出路径）。
	ErrFaninClosed = errors.New("mesh fan-in closed")
)

// Frame 是一帧 mesh SSE 事件（architecture §6.3 的信封）。
//
// 与 `id:` / `event:` / `data:` 三行的对应关系：
//
//	id:    <Seq>（Seq 为 0 时省略 id 行——连接级帧不占节点序号）
//	event: <Type>
//	data:  {"schema_version":1,"seq":N,"ts":"…","source_node_id":"…","type":"…","data":{…}}
type Frame struct {
	SchemaVersion int            `json:"schema_version"`
	Seq           uint64         `json:"seq"`
	TS            time.Time      `json:"ts"`
	SourceNodeID  string         `json:"source_node_id"`
	Type          string         `json:"type"`
	Data          map[string]any `json:"data,omitempty"`
}

// IsRelayWhitelistType 报告事件类型是否属于扇入白名单（§6.3）：只转发
// `turn.*` / `session.*` 的边界事件，不转发 token 级高频事件（逐字 delta
// 与状态栏刷新），否则 N 个 peer 会把单进程 SSE 淹没。
//
// 本仓库的运行时事件名是下划线风格（turn_start / session_end / assistant_delta），
// 因此白名单按前缀族匹配，并显式排除 delta 类。
func IsRelayWhitelistType(eventType string) bool {
	name := strings.ToLower(strings.TrimSpace(eventType))
	if name == "" || strings.Contains(name, "delta") {
		return false
	}
	return strings.HasPrefix(name, "turn") || strings.HasPrefix(name, "session")
}

// IsMeshFrameType 报告类型是否属于扇入自产的网格帧（`mesh.*`）。
func IsMeshFrameType(eventType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(eventType)), "mesh.")
}

// relayFrame 决定一帧「来自 peer」的事件是否并入本节点流（防环硬约束，§6.3）。
//
// 规则：
//  1. 来源必须就是该 peer 自己 —— 经 peer 转手的第三方事件一律丢弃，因此
//     A↔B 互订（乃至 A→B→C→A 环）不可能形成多跳回声；
//  2. `mesh.peer.event` 绝不二次转发；`mesh.lagged` 是 peer 自己连接的背压
//     标记，与本节点无关，同样丢弃；
//  3. 白名单内的 `turn.*` / `session.*` 包一层 `mesh.peer.event`（内层保留
//     原始帧供追溯）；
//  4. 其余 `mesh.*` 帧（joined/left/updated、session.changed、call.*）原样
//     并入，外层 seq 换成本节点计数器。
func relayFrame(peerNodeID string, frame Frame) (Frame, bool) {
	peerNodeID = strings.TrimSpace(peerNodeID)
	source := strings.TrimSpace(frame.SourceNodeID)
	if peerNodeID == "" || source == "" || source != peerNodeID {
		return Frame{}, false
	}
	eventType := strings.TrimSpace(frame.Type)
	switch eventType {
	case "", FramePeerEvent, FrameLagged:
		return Frame{}, false
	}
	switch {
	case IsRelayWhitelistType(eventType):
		return Frame{
			Type: FramePeerEvent,
			Data: map[string]any{
				"peer_node_id": source,
				"peer_seq":     frame.Seq,
				"peer_ts":      frame.TS,
				"frame":        frame,
			},
		}, true
	case IsMeshFrameType(eventType):
		return Frame{Type: eventType, Data: frame.Data}, true
	}
	return Frame{}, false
}

// tokenBucket 是每 peer 的令牌桶（§6.4 限流）。零值表示不限流。
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(rate float64, burst int, now time.Time) *tokenBucket {
	if burst <= 0 {
		burst = DefaultFaninPeerBurst
	}
	return &tokenBucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: now}
}

// allow 消耗一个令牌；返回 false 表示超限（调用方丢帧并计数）。
func (b *tokenBucket) allow(now time.Time) bool {
	if b == nil || b.rate <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last.IsZero() {
		b.last = now
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(b.burst, b.tokens+elapsed*b.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// FaninConfig configures one node's fan-in hub. The zero value is inert: without
// a Journal there is no sequence counter, hence no stream (fail-closed).
type FaninConfig struct {
	// NodeID is this node's id (used as the default frame source).
	NodeID string
	// Journal owns the node's seq counter: frames and journal lines share it.
	Journal *Journal
	// Now overrides the clock (tests).
	Now func() time.Time
	// MaxClients caps concurrent SSE clients (default DefaultFaninMaxClients).
	MaxClients int
	// QueueCapacity is the per-client frame buffer (default 256).
	QueueCapacity int
	// PeerRatePerSec / PeerBurst tune the per-peer token bucket.
	PeerRatePerSec float64
	PeerBurst      int
	// Relay overrides the relay decision (tests / future policy). nil uses
	// relayFrame.
	Relay func(peerNodeID string, frame Frame) (Frame, bool)
}

// Fanin is the local broadcast hub: it renders each frame once and fans it out
// to every connected client (browser, aicli-mesh watch, peer subscribers).
//
// All methods are nil-safe so a degraded process (no mesh root) can call them
// unconditionally.
type Fanin struct {
	mu     sync.Mutex
	nodeID string
	// journal is the seq source; nil disables the hub (fail-closed).
	journal       *Journal
	now           func() time.Time
	maxClients    int
	queueCapacity int
	rate          float64
	burst         int
	relay         func(string, Frame) (Frame, bool)

	clients      map[*FaninClient]struct{}
	buckets      map[string]*tokenBucket
	dropped      map[string]uint64
	closed       bool
	published    uint64
	droppedTotal uint64
}

// NewFanin builds the fan-in hub. It never fails: an unresolvable journal yields
// a disabled hub (ErrFaninDisabled on Subscribe, no frames published).
func NewFanin(cfg FaninConfig) *Fanin {
	if cfg.Now == nil {
		cfg.Now = NowUTC
	}
	if cfg.MaxClients <= 0 {
		cfg.MaxClients = DefaultFaninMaxClients
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = DefaultFaninQueueCapacity
	}
	if cfg.PeerRatePerSec <= 0 {
		cfg.PeerRatePerSec = DefaultFaninPeerRatePerSec
	}
	if cfg.PeerBurst <= 0 {
		cfg.PeerBurst = DefaultFaninPeerBurst
	}
	if cfg.Relay == nil {
		cfg.Relay = relayFrame
	}
	return &Fanin{
		nodeID:        strings.TrimSpace(cfg.NodeID),
		journal:       cfg.Journal,
		now:           cfg.Now,
		maxClients:    cfg.MaxClients,
		queueCapacity: cfg.QueueCapacity,
		rate:          cfg.PeerRatePerSec,
		burst:         cfg.PeerBurst,
		relay:         cfg.Relay,
		clients:       map[*FaninClient]struct{}{},
		buckets:       map[string]*tokenBucket{},
		dropped:       map[string]uint64{},
	}
}

// Enabled reports whether the hub can produce frames (a journal-backed node).
func (f *Fanin) Enabled() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.closed && f.journal != nil
}

// NodeID returns this node's id.
func (f *Fanin) NodeID() string {
	if f == nil {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodeID
}

// Clients returns the number of connected SSE clients.
func (f *Fanin) Clients() int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clients)
}

// Published returns how many frames were broadcast since start (diagnostics).
func (f *Fanin) Published() uint64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.published
}

// Dropped returns the frames dropped for one source node (rate limit or a full
// client queue). It feeds the `dropped_events` field of the peers view (§6.4).
func (f *Fanin) Dropped(nodeID string) uint64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropped[strings.TrimSpace(nodeID)]
}

// DroppedCounts returns a copy of the per-source drop counters.
func (f *Fanin) DroppedCounts() map[string]uint64 {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dropped) == 0 {
		return nil
	}
	out := make(map[string]uint64, len(f.dropped))
	for nodeID, count := range f.dropped {
		out[nodeID] = count
	}
	return out
}

// NextSeq allocates the next frame sequence from the shared journal counter.
func (f *Fanin) NextSeq() uint64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	journal := f.journal
	f.mu.Unlock()
	return journal.NextSeq()
}

// PublishLocal publishes a frame produced by this node itself (source = self).
func (f *Fanin) PublishLocal(frameType string, data map[string]any) uint64 {
	frame, ok := f.buildFrame(frameType, "", data, true)
	if !ok {
		return 0
	}
	f.broadcast(frame)
	return frame.Seq
}

// PublishPeer publishes a peer-sourced frame that did not come from the peer's
// stream verbatim (join/left lifecycle notices built by the subscriber).
func (f *Fanin) PublishPeer(peerNodeID, frameType string, data map[string]any) uint64 {
	frame, ok := f.buildFrame(frameType, peerNodeID, data, true)
	if !ok {
		return 0
	}
	f.broadcast(frame)
	return frame.Seq
}

// PublishPeerFrame merges one frame read from a peer's stream (S7 relay path).
// It applies the anti-loop relay rules and the per-peer token bucket; false
// means the frame was filtered or rate-limited (and counted).
func (f *Fanin) PublishPeerFrame(peerNodeID string, frame Frame) bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	relay := f.relay
	closed := f.closed
	f.mu.Unlock()
	if closed || relay == nil {
		return false
	}
	out, ok := relay(peerNodeID, frame)
	if !ok {
		return false
	}
	if !f.allowPeer(peerNodeID) {
		f.countDropped(peerNodeID)
		return false
	}
	built, ok := f.buildFrame(out.Type, peerNodeID, out.Data, true)
	if !ok {
		return false
	}
	f.broadcast(built)
	return true
}

// ConnectionFrame builds a frame that belongs to one connection only
// (mesh.ready / mesh.lagged). It carries no seq and is never broadcast.
func (f *Fanin) ConnectionFrame(frameType string, data map[string]any) (Frame, bool) {
	return f.buildFrame(frameType, "", data, false)
}

// Subscribe registers one SSE client. It returns ErrFaninDisabled when there is
// no mesh root and ErrFaninClientLimit at the client cap (HTTP 429).
func (f *Fanin) Subscribe() (*FaninClient, error) {
	if f == nil {
		return nil, ErrFaninDisabled
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, ErrFaninClosed
	}
	if f.journal == nil {
		return nil, ErrFaninDisabled
	}
	if len(f.clients) >= f.maxClients {
		return nil, ErrFaninClientLimit
	}
	client := &FaninClient{
		hub:  f,
		ch:   make(chan FaninFramePayload, f.queueCapacity),
		done: make(chan struct{}),
	}
	f.clients[client] = struct{}{}
	return client, nil
}

// Close stops the hub and detaches every client.
func (f *Fanin) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	clients := make([]*FaninClient, 0, len(f.clients))
	for client := range f.clients {
		clients = append(clients, client)
	}
	f.clients = map[*FaninClient]struct{}{}
	f.mu.Unlock()
	for _, client := range clients {
		client.Close()
	}
}

// buildFrame assembles a frame and allocates its sequence. broadcast=false is
// used for connection-level frames (no seq, no fan-out).
func (f *Fanin) buildFrame(frameType, sourceNodeID string, data map[string]any, withSeq bool) (Frame, bool) {
	if f == nil {
		return Frame{}, false
	}
	frameType = strings.TrimSpace(frameType)
	if frameType == "" {
		return Frame{}, false
	}
	f.mu.Lock()
	if f.closed || f.journal == nil {
		f.mu.Unlock()
		return Frame{}, false
	}
	journal := f.journal
	now := f.now
	nodeID := f.nodeID
	f.mu.Unlock()

	frame := Frame{
		SchemaVersion: FrameSchemaVersion,
		TS:            now(),
		SourceNodeID:  strings.TrimSpace(sourceNodeID),
		Type:          frameType,
		Data:          data,
	}
	if frame.SourceNodeID == "" {
		frame.SourceNodeID = nodeID
	}
	if withSeq {
		frame.Seq = journal.NextSeq()
	}
	return frame, true
}

// broadcast renders the frame once and fans it out to every client. Rendering
// outside the lock keeps the publish path O(clients) with no allocation per
// client (payload is shared, read-only).
func (f *Fanin) broadcast(frame Frame) {
	payload, err := MarshalFrame(frame)
	if err != nil {
		return
	}
	message := FaninFramePayload{Type: frame.Type, Payload: payload}
	if frame.Seq > 0 {
		message.ID = strconv.FormatUint(frame.Seq, 10)
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	clients := make([]*FaninClient, 0, len(f.clients))
	for client := range f.clients {
		clients = append(clients, client)
	}
	f.published++
	f.mu.Unlock()
	for _, client := range clients {
		client.push(message)
	}
}

// allowPeer consumes one token from the peer's bucket (rate limit §6.4).
func (f *Fanin) allowPeer(peerNodeID string) bool {
	peerNodeID = strings.TrimSpace(peerNodeID)
	if peerNodeID == "" {
		return true
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return false
	}
	bucket := f.buckets[peerNodeID]
	if bucket == nil {
		bucket = newTokenBucket(f.rate, f.burst, f.now())
		f.buckets[peerNodeID] = bucket
	}
	now := f.now()
	f.mu.Unlock()
	return bucket.allow(now)
}

// countDropped records a dropped frame against its source node.
func (f *Fanin) countDropped(sourceNodeID string) {
	if f == nil {
		return
	}
	sourceNodeID = strings.TrimSpace(sourceNodeID)
	if sourceNodeID == "" {
		sourceNodeID = "unknown"
	}
	f.mu.Lock()
	f.dropped[sourceNodeID]++
	f.droppedTotal++
	f.mu.Unlock()
}

// DroppedTotal returns the total number of dropped frames (all sources).
func (f *Fanin) DroppedTotal() uint64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.droppedTotal
}

// FaninFramePayload is one broadcast frame as delivered to a client: the frame
// type, its SSE resume cursor and the pre-rendered `data:` JSON bytes. The
// payload slice is shared (read-only) by every client of the node.
type FaninFramePayload struct {
	Type    string
	ID      string
	Payload []byte
}

// RenderFrame renders one frame as SSE bytes (`id:` / `event:` / `data:`). The
// returned slice is read-only and shared by every client of the node.
func RenderFrame(frame Frame) ([]byte, error) {
	data, err := MarshalFrame(frame)
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	sb.Grow(len(data) + 48)
	if frame.Seq > 0 {
		sb.WriteString("id: ")
		sb.WriteString(strconv.FormatUint(frame.Seq, 10))
		sb.WriteString("\n")
	}
	sb.WriteString("event: ")
	sb.WriteString(frame.Type)
	sb.WriteString("\ndata: ")
	sb.Write(data)
	sb.WriteString("\n\n")
	return []byte(sb.String()), nil
}

// MarshalFrame renders the `data:` JSON of one frame.
func MarshalFrame(frame Frame) ([]byte, error) {
	return json.Marshal(frame)
}

// ---------------------------------------------------------------------------
// FaninClient：单条 SSE 连接
// ---------------------------------------------------------------------------

// FaninClient is one SSE consumer of the fan-in hub. Frames arrive as
// pre-rendered read-only SSE payloads; the client only owns a bounded queue, so
// a slow consumer can never block the publishers.
type FaninClient struct {
	hub  *Fanin
	ch   chan FaninFramePayload
	done chan struct{}
	once sync.Once
	// lagged counts frames dropped for this connection (queue full); the
	// handler turns it into a mesh.lagged frame so the consumer can re-pull the
	// full view (§6.4).
	lagged  atomic.Uint64
	dropped atomic.Uint64
}

// Payloads is the read side of the client queue: each value is one complete
// frame (type + resume cursor + pre-rendered data bytes). The channel is closed
// when the client is closed.
func (c *FaninClient) Payloads() <-chan FaninFramePayload {
	if c == nil {
		return nil
	}
	return c.ch
}

// Done is closed when the client is closed (hub shutdown or client Close).
func (c *FaninClient) Done() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.done
}

// TakeLagged returns the number of frames dropped since the last call and
// resets the counter (0 means "nothing to report").
func (c *FaninClient) TakeLagged() uint64 {
	if c == nil {
		return 0
	}
	return c.lagged.Swap(0)
}

// Lagged reports the cumulative number of dropped frames (diagnostics).
func (c *FaninClient) Lagged() uint64 {
	if c == nil {
		return 0
	}
	return c.lagged.Load()
}

// Dropped reports frames dropped because the connection was already closed.
func (c *FaninClient) Dropped() uint64 {
	if c == nil {
		return 0
	}
	return c.dropped.Load()
}

// Close detaches the client from the hub. Idempotent and safe to call from any
// goroutine.
func (c *FaninClient) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		if c.hub != nil {
			c.hub.mu.Lock()
			delete(c.hub.clients, c)
			c.hub.mu.Unlock()
		}
		close(c.done)
	})
}

// push enqueues one pre-rendered frame. Never blocks: a full queue drops the
// frame and counts it (the consumer learns via TakeLagged).
func (c *FaninClient) push(message FaninFramePayload) {
	if c == nil || len(message.Payload) == 0 {
		return
	}
	select {
	case <-c.done:
		c.dropped.Add(1)
		return
	default:
	}
	select {
	case c.ch <- message:
	default:
		c.lagged.Add(1)
	}
}
