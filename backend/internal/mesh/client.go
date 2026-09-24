package mesh

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件是「跨进程扇入」的订阅端（architecture §6.2 订阅拓扑 / §6.4 背压），
// 对应实施方案 S7：
//
//	本节点 --SSE--> peer 的 /web/api/mesh/events --并入--> 本节点 Fanin
//
// 行为契约：
//
//   - 每个 live 且带回环 endpoint 的 peer 一条订阅（默认全连，上限 16）；
//   - 断线指数退避重连 1s → 2s → 4s … 上限 30s，期间该 peer 在视图里降级为
//     「实时不可用」，但 peers 全量视图 / call 目标解析完全不受影响（§6.2）；
//   - 重连带 `?since_seq=<last>` 续传；连接级失败只记 warning，绝不影响本进程
//     的 chat（MN1 / §4.7）；
//   - 订阅固定请求 `?peers=none`：peer 只回它自己的本地事件，跨 peer 的转发
//     由每个节点各自完成（避免 N² 转发）。

// ChatWebMeshEventsPath is the mesh SSE endpoint of every node (§5.5). The
// commands package registers the same path; node-to-node subscriptions dial it.
const ChatWebMeshEventsPath = "/web/api/mesh/events"

// PeerTarget is one peer this node subscribes to.
type PeerTarget struct {
	NodeID        string
	BaseURL       string
	Token         string
	WorkspacePath string
	SessionID     string
	State         string
}

// PeerTargetOptions tunes peer discovery (§6.2).
type PeerTargetOptions struct {
	// SelfNodeID is this node (never subscribed to itself).
	SelfNodeID string
	// MaxPeers caps the subscription set; 0 means DefaultFaninMaxPeers.
	MaxPeers int
	// IncludeStale also subscribes to stale nodes (diagnostics / tests).
	IncludeStale bool
	// PreferWorkspace lists same-workspace peers first when over the cap (§6.2:
	// 超过阈值时优先订阅同工作区 + 最近活跃的 N 个，其余靠轮询).
	PreferWorkspace string
}

// PeerTargetsFromView derives the subscription set from the aggregated view.
//
// Only nodes that are live (unless IncludeStale), advertise a loopback endpoint
// and declare the `events` capability are subscribable. The write token is read
// from the in-process record — it is never marshalled (§9.1 / M7).
func PeerTargetsFromView(view MeshView, opts PeerTargetOptions) []PeerTarget {
	selfID := strings.TrimSpace(opts.SelfNodeID)
	maxPeers := opts.MaxPeers
	if maxPeers <= 0 {
		maxPeers = DefaultFaninMaxPeers
	}
	targets := make([]PeerTarget, 0, len(view.Nodes))
	for _, node := range view.Nodes {
		nodeID := strings.TrimSpace(node.NodeID)
		if nodeID == "" || nodeID == selfID {
			continue
		}
		if !opts.IncludeStale && node.State != NodeStateLive {
			continue
		}
		if node.Endpoint == nil || !node.Endpoint.Loopback {
			continue
		}
		baseURL := strings.TrimSpace(node.Endpoint.BaseURL)
		if baseURL == "" {
			continue
		}
		if !nodeAdvertisesEvents(node) {
			continue
		}
		target := PeerTarget{
			NodeID:  nodeID,
			BaseURL: baseURL,
			State:   string(node.State),
		}
		if node.Workspace != nil {
			target.WorkspacePath = strings.TrimSpace(node.Workspace.Path)
		}
		if node.Session != nil {
			target.SessionID = strings.TrimSpace(node.Session.ID)
		}
		if node.Record != nil && node.Record.Auth != nil {
			target.Token = strings.TrimSpace(node.Record.Auth.Token)
		}
		targets = append(targets, target)
	}
	// 同工作区优先、最近活跃优先：只影响「超上限时谁被订阅」，不影响可见性。
	sort.SliceStable(targets, func(i, j int) bool {
		if opts.PreferWorkspace != "" {
			left := targets[i].WorkspacePath == opts.PreferWorkspace
			right := targets[j].WorkspacePath == opts.PreferWorkspace
			if left != right {
				return left
			}
		}
		leftActive := heartbeatRank(view, targets[i].NodeID)
		rightActive := heartbeatRank(view, targets[j].NodeID)
		if leftActive != rightActive {
			return leftActive > rightActive
		}
		return targets[i].NodeID < targets[j].NodeID
	})
	if len(targets) > maxPeers {
		targets = targets[:maxPeers]
	}
	return targets
}

// nodeAdvertisesEvents reports whether the node can serve /web/api/mesh/events.
func nodeAdvertisesEvents(node NodeView) bool {
	if node.Record == nil {
		return false
	}
	for _, capability := range node.Record.Capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), "events") {
			return true
		}
	}
	return false
}

// heartbeatRank orders peers by recency (higher = more recent).
func heartbeatRank(view MeshView, nodeID string) int64 {
	for _, node := range view.Nodes {
		if strings.TrimSpace(node.NodeID) != nodeID || node.HeartbeatAt == nil {
			continue
		}
		return node.HeartbeatAt.Unix()
	}
	return 0
}

// SubscriberConfig configures the peer SSE subscriber.
type SubscriberConfig struct {
	// NodeID is this node's id (sent as X-AICLI-Mesh-Caller).
	NodeID string
	// Fanin receives every merged frame (nil disables merging).
	Fanin *Fanin
	// HTTPClient overrides the streaming client (tests). nil uses a client with
	// no overall timeout (SSE is long-lived) and a 10s header timeout.
	HTTPClient *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
	// BackoffMin / BackoffMax bound the reconnect backoff (1s / 30s defaults).
	BackoffMin time.Duration
	BackoffMax time.Duration
	// MaxPeers caps concurrent subscriptions (default DefaultFaninMaxPeers).
	MaxPeers int
	// Warn receives degraded-mode warnings; nil means "stay silent".
	Warn func(format string, args ...any)
}

// PeerSubscriptionState is the per-peer subscription snapshot (diagnostics).
type PeerSubscriptionState struct {
	NodeID    string `json:"node_id"`
	BaseURL   string `json:"base_url,omitempty"`
	Connected bool   `json:"connected"`
	LastSeq   uint64 `json:"last_seq"`
	Frames    uint64 `json:"frames"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error,omitempty"`
}

// Subscriber keeps one SSE subscription per live loopback peer and merges the
// frames into the local fan-in hub (§6.2 / §6.4). Every failure is local to the
// subscription: the chat, the view and the CLI are unaffected (MN1).
type Subscriber struct {
	mu     sync.Mutex
	cfg    SubscriberConfig
	subs   map[string]*peerSub
	closed bool
	wg     sync.WaitGroup
}

// NewSubscriber builds the subscriber (no connections yet: call Sync).
func NewSubscriber(cfg SubscriberConfig) *Subscriber {
	if cfg.Now == nil {
		cfg.Now = NowUTC
	}
	if cfg.BackoffMin <= 0 {
		cfg.BackoffMin = DefaultPeerBackoffMin
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = DefaultPeerBackoffMax
	}
	if cfg.BackoffMax < cfg.BackoffMin {
		cfg.BackoffMax = cfg.BackoffMin
	}
	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = DefaultFaninMaxPeers
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Transport: defaultPeerTransport()}
	}
	return &Subscriber{cfg: cfg, subs: map[string]*peerSub{}}
}

// Reconnect backoff bounds (§6.4).
const (
	DefaultPeerBackoffMin = 1 * time.Second
	DefaultPeerBackoffMax = 30 * time.Second
)

func defaultPeerTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.MaxIdleConnsPerHost = 4
	return transport
}

// Sync reconciles the subscription set with the discovered targets: new peers
// are dialed, vanished peers are dropped, existing ones keep their connection.
// It returns how many subscriptions were started.
func (s *Subscriber) Sync(targets []PeerTarget) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	seen := make(map[string]bool, len(targets))
	started := 0
	for _, target := range targets {
		nodeID := strings.TrimSpace(target.NodeID)
		baseURL := strings.TrimSpace(target.BaseURL)
		if nodeID == "" || baseURL == "" || nodeID == s.cfg.NodeID {
			continue
		}
		seen[nodeID] = true
		if existing := s.subs[nodeID]; existing != nil {
			existing.refresh(target)
			continue
		}
		if len(s.subs) >= s.cfg.MaxPeers {
			// Over the cap: skip silently — the node stays visible in the view
			// and callable, it just loses sub-second push (§6.2).
			continue
		}
		sub := newPeerSub(target)
		s.subs[nodeID] = sub
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.run(sub)
		}()
		started++
	}
	for nodeID, sub := range s.subs {
		if seen[nodeID] {
			continue
		}
		delete(s.subs, nodeID)
		sub.stopNow()
	}
	return started
}

// Stats returns a snapshot of every subscription (diagnostics / tests).
func (s *Subscriber) Stats() []PeerSubscriptionState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	subs := make([]*peerSub, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	states := make([]PeerSubscriptionState, 0, len(subs))
	for _, sub := range subs {
		states = append(states, sub.state())
	}
	sort.Slice(states, func(i, j int) bool { return states[i].NodeID < states[j].NodeID })
	return states
}

// Close stops every subscription and waits for the goroutines to exit.
func (s *Subscriber) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	subs := make([]*peerSub, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subs = map[string]*peerSub{}
	s.mu.Unlock()
	for _, sub := range subs {
		sub.stopNow()
	}
	s.wg.Wait()
}

// run is one peer's reconnect loop: stream until error, then back off.
func (s *Subscriber) run(sub *peerSub) {
	backoff := s.cfg.BackoffMin
	for {
		if sub.stopped() {
			return
		}
		established, err := s.stream(sub)
		if established {
			backoff = s.cfg.BackoffMin
		}
		if sub.stopped() {
			return
		}
		sub.noteError(err, s.cfg.Fanin)
		if err != nil {
			s.warn("mesh: peer %s stream lost: %v", sub.nodeID, err)
		}
		if !sleepOrStop(sub.stop, backoff) {
			return
		}
		backoff *= 2
		if backoff > s.cfg.BackoffMax {
			backoff = s.cfg.BackoffMax
		}
	}
}

// stream runs one SSE connection until it fails or the subscription stops.
// established reports whether the stream was up (so the caller resets backoff).
func (s *Subscriber) stream(sub *peerSub) (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-sub.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	url := strings.TrimRight(sub.baseURL(), "/") + ChatWebMeshEventsPath + "?peers=none"
	if seq := sub.seq(); seq > 0 {
		url += fmt.Sprintf("&since_seq=%d", seq)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("X-AICLI-Mesh-Caller", s.cfg.NodeID)
	if token := sub.token(); token != "" {
		req.Header.Set("X-AICLI-Token", token)
	}
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Drain a bounded amount so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("status %d", resp.StatusCode)
	}

	established := true
	sub.noteConnected(s.cfg.Fanin)
	reader := bufio.NewReaderSize(resp.Body, 32<<10)
	var (
		eventType string
		dataBuf   strings.Builder
	)
	dispatch := func() {
		frame, ok := decodePeerFrame(eventType, dataBuf.String(), sub.nodeID)
		eventType = ""
		dataBuf.Reset()
		if !ok {
			return
		}
		sub.noteFrame(frame.Seq)
		if s.cfg.Fanin != nil {
			s.cfg.Fanin.PublishPeerFrame(sub.nodeID, frame)
		}
	}
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				dispatch()
			case strings.HasPrefix(line, ":"):
				// SSE comment (keepalive): ignore.
			case strings.HasPrefix(line, "event:"):
				eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				if dataBuf.Len() > 0 {
					dataBuf.WriteString("\n")
				}
				dataBuf.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case strings.HasPrefix(line, "id:"):
				// id 与 data.seq 同源；seq 以 data 为准（§6.3）。
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return established, nil
			}
			return established, readErr
		}
	}
}

// decodePeerFrame parses one SSE data payload into a Frame.
func decodePeerFrame(eventType, data, peerNodeID string) (Frame, bool) {
	payload := strings.TrimSpace(data)
	if payload == "" {
		return Frame{}, false
	}
	var frame Frame
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return Frame{}, false
	}
	if strings.TrimSpace(frame.Type) == "" {
		frame.Type = strings.TrimSpace(eventType)
	}
	if strings.TrimSpace(frame.Type) == "" {
		return Frame{}, false
	}
	if strings.TrimSpace(frame.SourceNodeID) == "" {
		// 我们是从该 peer 的流里读到它的：缺省来源即该 peer（防御性兜底）。
		frame.SourceNodeID = peerNodeID
	}
	return frame, true
}

func (s *Subscriber) warn(format string, args ...any) {
	if s == nil || s.cfg.Warn == nil {
		return
	}
	s.cfg.Warn(format, args...)
}

// sleepOrStop waits for d, returning false when the subscription stopped.
func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

// ---------------------------------------------------------------------------
// peerSub：单 peer 的订阅状态
// ---------------------------------------------------------------------------

type peerSub struct {
	nodeID string

	mu        sync.Mutex
	base      string
	authToken string
	lastSeq   uint64
	frames    uint64
	attempts  int
	connected bool
	lastError string

	stop     chan struct{}
	stopOnce sync.Once
}

func newPeerSub(target PeerTarget) *peerSub {
	return &peerSub{
		nodeID:    strings.TrimSpace(target.NodeID),
		base:      strings.TrimSpace(target.BaseURL),
		authToken: strings.TrimSpace(target.Token),
		stop:      make(chan struct{}),
	}
}

func (p *peerSub) baseURL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.base
}

func (p *peerSub) token() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authToken
}

func (p *peerSub) seq() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastSeq
}

// refresh updates the dial address / token of an existing subscription (the
// peer may have restarted with a new port or a rotated token, §5.6 要点 5).
func (p *peerSub) refresh(target PeerTarget) {
	base := strings.TrimSpace(target.BaseURL)
	token := strings.TrimSpace(target.Token)
	p.mu.Lock()
	if base != "" {
		p.base = base
	}
	if token != "" {
		p.authToken = token
	}
	p.mu.Unlock()
}

func (p *peerSub) noteConnected(fanin *Fanin) {
	p.mu.Lock()
	p.connected = true
	p.attempts++
	p.lastError = ""
	base := p.base
	attempts := p.attempts
	p.mu.Unlock()
	if fanin == nil {
		return
	}
	// 每次（重）连都发一帧 joined：消费方据此把该 peer 的实时覆盖翻回「可用」。
	fanin.PublishPeer(p.nodeID, FramePeerJoined, map[string]any{
		"node_id":     p.nodeID,
		"base_url":    base,
		"observed_at": fanin.now(),
		"attempts":    attempts,
	})
}

func (p *peerSub) noteError(err error, fanin *Fanin) {
	p.mu.Lock()
	wasConnected := p.connected
	p.connected = false
	if err != nil {
		p.lastError = err.Error()
	}
	base := p.base
	p.mu.Unlock()
	if !wasConnected || fanin == nil {
		return
	}
	detail := map[string]any{"node_id": p.nodeID, "base_url": base}
	if err != nil {
		detail["error"] = err.Error()
	}
	fanin.PublishPeer(p.nodeID, FramePeerLeft, detail)
}

func (p *peerSub) noteFrame(seq uint64) {
	p.mu.Lock()
	if seq > p.lastSeq {
		p.lastSeq = seq
	}
	p.frames++
	p.mu.Unlock()
}

func (p *peerSub) state() PeerSubscriptionState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PeerSubscriptionState{
		NodeID:    p.nodeID,
		BaseURL:   p.base,
		Connected: p.connected,
		LastSeq:   p.lastSeq,
		Frames:    p.frames,
		Attempts:  p.attempts,
		LastError: p.lastError,
	}
}

func (p *peerSub) stopped() bool {
	select {
	case <-p.stop:
		return true
	default:
		return false
	}
}

// stopNow ends the subscription (idempotent). The run goroutine exits on the
// next stop check and unwinds its HTTP request context.
func (p *peerSub) stopNow() {
	p.stopOnce.Do(func() { close(p.stop) })
}

// ---------------------------------------------------------------------------
// 进程级接线：Host.StartPeerSync
// ---------------------------------------------------------------------------

// PeerSyncConfig tunes the periodic peer discovery loop (§6.2).
type PeerSyncConfig struct {
	// Interval is how often the target set is re-derived from the view
	// (default 5s). Discovery is a read-only directory scan.
	Interval time.Duration
	// MaxPeers caps concurrent subscriptions (default DefaultFaninMaxPeers).
	MaxPeers int
	// IncludeStale also subscribes to stale nodes (diagnostics / tests).
	IncludeStale bool
	// Warn receives degraded-mode warnings; nil means "stay silent".
	Warn func(format string, args ...any)
}

// StartPeerSync launches the process-wide peer subscription loop: every
// Interval it re-derives the target set from the on-disk view and keeps one SSE
// subscription per live loopback peer. The first pass runs immediately so two
// processes that start together see each other as soon as both endpoints exist.
//
// It returns nil when the mesh is disabled (no host / no fan-in), and is safe to
// call once per process: Host.Close stops the loop.
func (h *Host) StartPeerSync(cfg PeerSyncConfig) *Subscriber {
	if h == nil {
		return nil
	}
	fanin := h.Fanin()
	if fanin == nil || !fanin.Enabled() {
		return nil
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultPeerSyncInterval
	}
	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = DefaultFaninMaxPeers
	}
	subscriber := NewSubscriber(SubscriberConfig{
		NodeID:   h.NodeID(),
		Fanin:    fanin,
		MaxPeers: cfg.MaxPeers,
		Warn:     cfg.Warn,
	})
	h.mu.Lock()
	if h.closed || h.stopCh == nil {
		h.mu.Unlock()
		subscriber.Close()
		return nil
	}
	h.subscriber = subscriber
	stopCh := h.stopCh
	paths := h.paths
	selfID := h.nodeID
	workspace := h.cfg.WorkspacePath
	h.mu.Unlock()

	go func() {
		sync := func() {
			view := BuildView(paths, ViewOptions{SelfNodeID: selfID})
			targets := PeerTargetsFromView(view, PeerTargetOptions{
				SelfNodeID:      selfID,
				MaxPeers:        cfg.MaxPeers,
				IncludeStale:    cfg.IncludeStale,
				PreferWorkspace: workspace,
			})
			subscriber.Sync(targets)
		}
		sync()
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				sync()
			}
		}
	}()
	return subscriber
}

// DefaultPeerSyncInterval is how often the peer set is re-derived (§6.2).
const DefaultPeerSyncInterval = 5 * time.Second
