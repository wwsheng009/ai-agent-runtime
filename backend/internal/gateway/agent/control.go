package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// Conn is a live tunnel connection returned by a successful DialFunc. The real
// implementation (WS + wire protocol, see docs/gateway/03-protocol.md) lives in
// the transport package; tests inject a fake.
type Conn interface {
	// NodeID is the node id assigned/confirmed by the gateway.
	NodeID() string
	// ConsoleURL is the gateway console URL for this node.
	ConsoleURL() string
	// Close tears the connection down (best effort).
	Close() error
}

// MountReporter is implemented by connections that can report the gateway's
// mount registration outcome (the WebSocket tunnel does). Optional: older
// transports simply leave Status.Mounts empty.
type MountReporter interface {
	Mounts() []MountStatus
}

// DialFunc establishes a tunnel connection. It is the single seam between the
// state machine and the transport, which keeps this package standard-library
// only and trivially testable.
type DialFunc func(ctx context.Context, cfg Config) (Conn, error)

// Error codes shared with the control surfaces. They map onto the protocol
// error codes documented in docs/gateway/03-protocol.md §7.
const (
	ErrCodeConfigInvalid = "config_invalid"
	ErrCodeDisabled      = "disabled"
	ErrCodeAuthInvalid   = "auth_invalid"
	ErrCodeNetwork       = "network"
	ErrCodeBusy          = "busy"
	ErrCodeInternal      = "internal"
	// ErrCodeOperatorDisconnect reports that automatic reconnect (or a retry
	// loop) was suppressed by an explicit operator disconnect. Manual connect
	// (ConnectManual) clears the suppression and proceeds.
	ErrCodeOperatorDisconnect = "operator_disconnect"
)

// ConnectError classifies a failed connect attempt. Retryable=false (for
// example auth_invalid) tells the host not to auto-retry — retrying a rejected
// token only produces reconnect storms.
type ConnectError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *ConnectError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return "gateway/agent: connect failed (" + e.Code + ")"
	}
	return "gateway/agent: " + e.Message
}

// doneConn is the optional death-signal capability of a Conn: implementations
// that expose Done() (the real wsConn) can be watched for self-inflicted
// termination, enabling the auto-reconnect loop. Minimal fakes omit it.
type doneConn interface{ Done() <-chan struct{} }

// autoReconnectDelays is the reconnect backoff ladder (capped). It is shared by
// the automatic reconnect watcher and ConnectWithRetry so a host retrying the
// first dial backs off exactly like a host recovering a dropped tunnel.
var autoReconnectDelays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}

// connectAttemptTimeout bounds a single dial attempt (handshake included)
// inside a retry loop, so one wedged dial cannot stall the backoff ladder.
const connectAttemptTimeout = 30 * time.Second

// asConnectError normalises an arbitrary dial error into a *ConnectError.
func asConnectError(err error) *ConnectError {
	if err == nil {
		return nil
	}
	var ce *ConnectError
	if errors.As(err, &ce) {
		return ce
	}
	return &ConnectError{Code: ErrCodeNetwork, Message: err.Error(), Retryable: true}
}

// Option customises a Controller (tests use it to inject a clock).
type Option func(*Controller)

// WithClock overrides the time source used for ConnectedAt/LastHeartbeat.
func WithClock(now func() time.Time) Option {
	return func(c *Controller) {
		if now != nil {
			c.now = now
		}
	}
}

// WithWindowAction 注册窗口动作执行器：控制台在管理端点「在新终端打开 / 恢复 /
// 关闭 / 新建」时，动作经 HEARTBEAT_ACK 的 Pending 下发，由这里在宿主本机执行。
//
// 执行器被**串行**调用（单执行器 goroutine），实现里可以直接开进程或调 mesh 编排，
// 不必自己加锁；它跑在心跳读循环之外，因此不能无限阻塞——那会拖住整条连接。
// 不注册即本节点不执行窗口动作（Pending 里的 window:* 被忽略，旧行为逐字节不变）。
func WithWindowAction(handler func(WindowAction)) Option {
	return func(c *Controller) {
		c.cfg.windowHandler = handler
	}
}

// WithLogger installs a diagnostic logger for connection lifecycle events:
// connection loss, retry attempts and outcomes, watchdog closes and reconnect
// stops. nil (the default) disables logging. It also feeds the WebSocket
// transport built by NewController.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(c *Controller) {
		if logf != nil {
			c.logf = logf
		}
	}
}

// WithReconnectDelays overrides the retry backoff ladder used by the automatic
// reconnect watcher and ConnectWithRetry (entry N = delay before retry N+1,
// capped at the last entry). Hosts with unusual networks may tune it; tests use
// it to keep runs fast. An empty ladder keeps the package default.
func WithReconnectDelays(delays []time.Duration) Option {
	cleaned := make([]time.Duration, 0, len(delays))
	for _, delay := range delays {
		if delay > 0 {
			cleaned = append(cleaned, delay)
		}
	}
	return func(c *Controller) {
		if len(cleaned) > 0 {
			c.retryDelays = cleaned
		}
	}
}

// Controller owns the connection state machine. All exported methods are safe
// for concurrent use. Connect/Disconnect/Reconnect are serialised through the
// mutex; the blocking dial runs outside the lock so Status stays responsive.
type Controller struct {
	mu   sync.Mutex
	cfg  Config
	dial DialFunc
	now  func() time.Time

	state  State
	conn   Conn
	status Status
	// sessions is the latest per-session projection pushed by the host
	// (UpdateSessions). It feeds the heartbeat digests so a missed
	// SESSION_UPDATE cannot leave the gateway with stale session state.
	sessions []wire.SessionSummary

	// reconnectGen invalidates a stale auto-reconnect watcher: any explicit
	// Disconnect/Connect bumps the generation so only the latest watcher runs.
	reconnectGen uint64
	backoffIdx   int
	// logf receives lifecycle diagnostics (loss, retries, watchdog closes).
	// Set once at construction via WithLogger; nil disables logging.
	logf func(format string, args ...any)
	// retryDelays overrides the backoff ladder. Empty keeps
	// autoReconnectDelays.
	retryDelays []time.Duration

	forwardMu sync.RWMutex
	forward   ForwardConfig

	// watchMu guards the gateway-advertised tail-watch gate (HeartbeatAck
	// pending actions). watchGating stays false until the gateway advertises
	// participation; until then TailWanted defaults to true (P1-a behavior).
	watchMu     sync.Mutex
	watchSet    map[string]struct{}
	watchGating bool

	// notifySuppressMu guards the gateway-derived local-notification
	// suppression rules. They are replaced on REGISTER_ACK/HEARTBEAT_ACK and
	// cleared on disconnect so a dead gateway cannot silently mute local
	// notifications.
	notifySuppressMu       sync.RWMutex
	notifySuppress         []wire.NotifySuppressRule
	notifySuppressObserver func([]wire.NotifySuppressRule)

	// 窗口动作（控制台点「在新终端打开 / 恢复 / 关闭」）。windowHandler 在 New 之后
	// 只读；queue/dropped 由 windowMu 保护。
	windowMu      sync.Mutex
	windowQueue   chan WindowAction
	windowDropped int
	windowHandled int
}

// WindowAction 是一条来自 HEARTBEAT_ACK Pending 的窗口动作。
type WindowAction struct {
	// Kind 是 wire.WindowAction* 之一（open-terminal|resume|new|close）。
	Kind string
	// SessionID 是目标会话；Kind=new 时为空。
	SessionID string
}

// windowActionQueueDepth 是本机待执行窗口动作的缓冲深度。执行器（开终端、停节点）
// 是慢操作，绝不能占用 WS 读循环；缓冲满时丢弃并计数，而不是无限堆积。
const windowActionQueueDepth = 8

// ApplyPending consumes control actions from a HEARTBEAT_ACK. Gating actions:
//
//	tail-gating:on     the gateway participates in watch gating
//	watch:<sessionID>  enable live-tail pushes for the session
//	unwatch:<sessionID> disable them
//	window:<kind>[:<sessionID>]  在本机执行一次窗口动作（见 WithWindowAction）
//
// Unknown actions are ignored (forward compatibility).
func (c *Controller) ApplyPending(actions []string) {
	if c == nil || len(actions) == 0 {
		return
	}
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	if c.watchSet == nil {
		c.watchSet = make(map[string]struct{})
	}
	for _, action := range actions {
		action = strings.TrimSpace(action)
		switch {
		case action == "tail-gating:on":
			c.watchGating = true
		case strings.HasPrefix(action, "watch:"):
			if id := strings.TrimSpace(strings.TrimPrefix(action, "watch:")); id != "" {
				c.watchSet[id] = struct{}{}
			}
		case strings.HasPrefix(action, "unwatch:"):
			delete(c.watchSet, strings.TrimSpace(strings.TrimPrefix(action, "unwatch:")))
		case strings.HasPrefix(action, wire.WindowActionPrefix):
			// 只排队，不在 watchMu 里执行：开一个终端或等进程退出可以花上秒级，
			// 而这里是心跳读循环。
			if kind, sessionID, ok := wire.ParseWindowAction(action); ok {
				c.enqueueWindowAction(WindowAction{Kind: kind, SessionID: sessionID})
			}
		}
	}
}

// enqueueWindowAction 把一条窗口动作交给本机单执行器。缓冲满时丢弃并计数
// （WindowActionStats），宁可漏一次操作也不让心跳循环被拖住。
func (c *Controller) enqueueWindowAction(action WindowAction) {
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	if c.cfg.windowHandler == nil {
		return
	}
	if c.windowQueue == nil {
		c.windowQueue = make(chan WindowAction, windowActionQueueDepth)
		go runWindowActionQueue(c.windowQueue, c.windowQueueStats, c.cfg.windowHandler)
	}
	select {
	case c.windowQueue <- action:
	default:
		c.windowDropped++
	}
}

// windowQueueStats 回写执行/丢弃计数（在 windowMu 内调用）。
func (c *Controller) windowQueueStats(deltaHandled, deltaDropped int) {
	c.windowMu.Lock()
	c.windowHandled += deltaHandled
	c.windowDropped += deltaDropped
	c.windowMu.Unlock()
}

// WindowActionStats 报告本机已执行 / 已丢弃的窗口动作数（/gateway 状态展示用）。
func (c *Controller) WindowActionStats() (handled, dropped int) {
	if c == nil {
		return 0, 0
	}
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	return c.windowHandled, c.windowDropped
}

// runWindowActionQueue 是窗口动作的单执行器：串行消费，避免两个动作同时对同一个
// 会话开两个窗口（open 与 resume 并发到达时最容易撞上）。
func runWindowActionQueue(queue <-chan WindowAction, stats func(handled, dropped int), handler func(WindowAction)) {
	if handler == nil {
		return
	}
	for action := range queue {
		handler(action)
		if stats != nil {
			stats(1, 0)
		}
	}
}

// TailWanted reports whether live-tail pushes for sessionID should be sent.
// It returns true until the gateway advertises gating, so hosts keep the P1-a
// behavior against gateways that do not implement watch control.
func (c *Controller) TailWanted(sessionID string) bool {
	if c == nil {
		return true
	}
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	if !c.watchGating {
		return true
	}
	_, ok := c.watchSet[strings.TrimSpace(sessionID)]
	return ok
}

// New creates a Controller. The initial state is StateDisabled when the config
// is disabled, otherwise StateDisconnected (the host decides when to Connect,
// typically at startup when AutoConnect is set).
func New(cfg Config, dial DialFunc, opts ...Option) *Controller {
	c := &Controller{
		cfg:  cfg,
		dial: dial,
		now:  time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if len(c.retryDelays) == 0 {
		c.retryDelays = autoReconnectDelays
	}
	if c.now == nil {
		c.now = time.Now
	}
	c.status = Status{
		GatewayURL: strings.TrimSpace(cfg.URL),
		TokenHint:  cfg.TokenHint(),
		Mode:       ModeAuto,
	}
	if cfg.Enabled {
		c.state = StateDisconnected
	} else {
		c.state = StateDisabled
	}
	c.status.State = c.state
	return c
}

// Status returns a copy of the current status snapshot.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Config returns a copy of the current configuration.
func (c *Controller) Config() Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// retryDelay returns the backoff before one retry attempt (0-based index),
// capped at the last ladder entry.
func (c *Controller) retryDelay(attempt int) time.Duration {
	delays := c.retryDelays
	if len(delays) == 0 {
		delays = autoReconnectDelays
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(delays) {
		attempt = len(delays) - 1
	}
	return delays[attempt]
}

// logf0 forwards to the host-supplied logger when one is installed.
func (c *Controller) logf0(format string, args ...any) {
	if c == nil || c.logf == nil {
		return
	}
	c.logf(format, args...)
}

// SetSessions updates the session count reported to the gateway. It is called
// by the host as sessions are added/removed and never changes the connection
// state.
func (c *Controller) SetSessions(n int) {
	if n < 0 {
		n = 0
	}
	c.mu.Lock()
	c.status.Sessions = n
	c.mu.Unlock()
}

// MarkHeartbeat records that the gateway acknowledged a heartbeat.
func (c *Controller) MarkHeartbeat() {
	c.mu.Lock()
	c.status.LastHeartbeat = c.now()
	c.mu.Unlock()
}

// ApplyNotifySuppress replaces the gateway-derived local-notification
// suppression rules. The gateway sends the full set in REGISTER_ACK and every
// HEARTBEAT_ACK; nil/empty clears it. The observer (when set) mirrors the
// rules into the host's connector layer.
func (c *Controller) ApplyNotifySuppress(rules []wire.NotifySuppressRule) {
	if c == nil {
		return
	}
	copied := cloneNotifySuppressRules(rules)
	c.notifySuppressMu.Lock()
	c.notifySuppress = copied
	observer := c.notifySuppressObserver
	c.notifySuppressMu.Unlock()
	if observer != nil {
		observer(cloneNotifySuppressRules(copied))
	}
}

// SetNotifySuppressObserver installs a hook that receives every rule-set
// replacement (including the initial snapshot, if rules already arrived).
// Hosts use it to feed internal/connectors' process-level suppression store.
func (c *Controller) SetNotifySuppressObserver(fn func([]wire.NotifySuppressRule)) {
	if c == nil {
		return
	}
	c.notifySuppressMu.Lock()
	c.notifySuppressObserver = fn
	current := cloneNotifySuppressRules(c.notifySuppress)
	c.notifySuppressMu.Unlock()
	if fn != nil {
		fn(current)
	}
}

// NotifySuppressed reports whether the gateway has taken over this local
// notification (session + local kind). It is empty when disconnected; the
// controller clears rules on connection loss so local sends resume.
func (c *Controller) NotifySuppressed(sessionID, kind string) bool {
	if c == nil {
		return false
	}
	kind = normalizeSuppressKind(kind)
	sessionID = strings.TrimSpace(sessionID)
	c.notifySuppressMu.RLock()
	rules := c.notifySuppress
	c.notifySuppressMu.RUnlock()
	for _, rule := range rules {
		if len(rule.Sessions) > 0 {
			matched := false
			for _, id := range rule.Sessions {
				if strings.EqualFold(strings.TrimSpace(id), sessionID) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if len(rule.Kinds) == 0 {
			return true
		}
		for _, ruleKind := range rule.Kinds {
			if normalizeSuppressKind(ruleKind) == kind {
				return true
			}
		}
	}
	return false
}

func cloneNotifySuppressRules(rules []wire.NotifySuppressRule) []wire.NotifySuppressRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]wire.NotifySuppressRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, wire.NotifySuppressRule{
			Kinds:    append([]string(nil), rule.Kinds...),
			Sessions: append([]string(nil), rule.Sessions...),
		})
	}
	return out
}

func normalizeSuppressKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "completion" {
		return "completed"
	}
	return kind
}

// Connect establishes the tunnel as a config-driven (auto) connection. It is
// used at startup and by automatic reconnect. It is a no-op when already
// connecting or connected, and fails closed with a *ConnectError when the
// gateway is disabled or the configuration is incomplete.
func (c *Controller) Connect(ctx context.Context) error {
	return c.connect(ctx, ModeAuto)
}

// ConnectManual establishes the tunnel as an explicit operator action. It has
// the same semantics as Connect except that Status.Mode reports "manual".
func (c *Controller) ConnectManual(ctx context.Context) error {
	return c.connect(ctx, ModeManual)
}

func (c *Controller) connect(ctx context.Context, mode string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	switch c.state {
	case StateConnecting, StateConnected:
		c.mu.Unlock()
		return nil // idempotent
	case StateDisconnecting:
		c.mu.Unlock()
		return &ConnectError{Code: ErrCodeBusy, Message: "disconnect in progress", Retryable: true}
	case StateDisabled:
		c.mu.Unlock()
		return &ConnectError{Code: ErrCodeDisabled, Message: "gateway is disabled", Retryable: false}
	}
	// An explicit operator disconnect suppresses automatic reconnect. Manual
	// connect (ConnectManual) clears the flag, but the auto path must refuse:
	// otherwise a backoff loop (or a late host retry) would silently undo the
	// operator's decision.
	if c.status.OperatorDisconnected && mode != ModeManual {
		c.mu.Unlock()
		return &ConnectError{
			Code:      ErrCodeOperatorDisconnect,
			Message:   "gateway is manually disconnected; automatic reconnect suppressed",
			Retryable: false,
		}
	}
	if err := c.cfg.Validate(); err != nil {
		c.mu.Unlock()
		return &ConnectError{Code: ErrCodeConfigInvalid, Message: err.Error(), Retryable: false}
	}
	if c.dial == nil {
		c.mu.Unlock()
		return &ConnectError{Code: ErrCodeInternal, Message: "no dial function configured", Retryable: false}
	}

	old := c.conn
	c.conn = nil
	c.transition(StateConnecting)
	c.status.OperatorDisconnected = false
	c.status.Mode = mode
	cfg := c.cfg
	dial := c.dial
	c.mu.Unlock()

	// A reconnect must not inherit the previous connection's suppression set:
	// until the new REGISTER_ACK arrives, local notifications are allowed.
	c.ApplyNotifySuppress(nil)
	if old != nil {
		_ = old.Close()
	}

	conn, err := dial(ctx, cfg)

	c.mu.Lock()
	defer c.mu.Unlock()
	// A concurrent Disconnect may have moved us out of Connecting; honour it.
	if c.state != StateConnecting {
		if conn != nil {
			_ = conn.Close()
		}
		return nil
	}
	if err != nil {
		ce := asConnectError(err)
		c.status.ErrorCode = ce.Code
		c.status.LastError = ce.Message
		c.status.Retryable = ce.Retryable
		c.transition(StateError)
		return ce
	}
	c.conn = conn
	c.status.NodeID = conn.NodeID()
	c.status.ConsoleURL = conn.ConsoleURL()
	if reporter, ok := conn.(MountReporter); ok {
		c.status.Mounts = reporter.Mounts()
	} else {
		c.status.Mounts = nil
	}
	c.status.ConnectedAt = c.now()
	c.status.LastHeartbeat = c.now()
	c.status.LastError = ""
	c.status.ErrorCode = ""
	c.status.Retryable = false
	c.transition(StateConnected)
	c.backoffIdx = 0
	c.armConnectionLostWatcher(conn)
	return nil
}

// ConnectWithRetry establishes the tunnel like Connect, but keeps retrying
// retryable failures with the same capped backoff ladder as automatic
// reconnect. It blocks until:
//   - connected (nil; the connection-lost watcher then maintains the tunnel),
//   - a terminal (retryable=false) failure, such as auth_invalid or an
//     operator disconnect (the *ConnectError is returned),
//   - ctx is done (ctx.Err() is returned).
//
// Hosts that start their agent before the network/gateway is guaranteed
// reachable (aicli-remote-tools serve) use this instead of a single Connect, so
// a failed first dial no longer leaves the node offline until a restart.
func (c *Controller) ConnectWithRetry(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, connectAttemptTimeout)
		err := c.Connect(attemptCtx)
		cancel()
		if err == nil {
			if st := c.Status(); st.Connected() {
				c.logf0("gateway agent: connected as node %s", st.NodeID)
				return nil
			}
			// A concurrent Disconnect/Configure won the race; do not report a
			// success that does not exist.
			code := ErrCodeBusy
			message := "connect aborted by concurrent state change"
			if c.Status().OperatorDisconnected {
				code = ErrCodeOperatorDisconnect
				message = "gateway is manually disconnected; automatic reconnect suppressed"
			}
			return &ConnectError{Code: code, Message: message, Retryable: false}
		}
		ce := asConnectError(err)
		if !ce.Retryable {
			c.logf0("gateway agent: connect failed (terminal, %s): %s; not retrying", ce.Code, ce.Message)
			return ce
		}
		delay := c.retryDelay(attempt)
		c.logf0("gateway agent: connect attempt %d failed (%s): %s; retrying in %s", attempt+1, ce.Code, ce.Message, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// armConnectionLostWatcher spawns the auto-reconnect loop for a freshly
// established connection: when the connection dies on its own (gateway GOAWAY,
// network drop), the controller enters Reconnecting and re-dials with a capped
// backoff until it reconnects or the operator intervenes. Generation counting
// ensures only the newest watcher acts; conns without Done() (minimal fakes)
// are not watched, so hosts/tests keep full manual control.
func (c *Controller) armConnectionLostWatcher(conn Conn) {
	dc, ok := conn.(doneConn)
	if !ok || dc == nil {
		return
	}
	done := dc.Done()
	if done == nil {
		return
	}
	c.reconnectGen++
	gen := c.reconnectGen
	go func() {
		for {
			<-done

			c.mu.Lock()
			if c.reconnectGen != gen || c.conn != conn {
				c.mu.Unlock()
				return
			}
			// Detach the dead connection before the loss bookkeeping: the guard
			// above (and HandleConnectionLost's own teardown) key off c.conn.
			c.conn = nil
			if c.status.OperatorDisconnected {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			c.HandleConnectionLost(ErrCodeNetwork, "connection lost", true)
			c.logf0("gateway agent: connection lost; auto-reconnect started")

			var lastErr error
			for attempt := 1; ; attempt++ {
				c.mu.Lock()
				if c.reconnectGen != gen {
					c.mu.Unlock()
					return
				}
				delay := c.retryDelay(c.backoffIdx)
				c.backoffIdx++
				c.mu.Unlock()

				if lastErr != nil {
					c.logf0("gateway agent: reconnect attempt %d failed: %v; retrying in %s", attempt-1, lastErr, delay)
				}
				time.Sleep(delay)

				ctx, cancel := context.WithTimeout(context.Background(), connectAttemptTimeout)
				err := c.Connect(ctx)
				cancel()
				if err == nil {
					c.logf0("gateway agent: reconnected on attempt %d (node %s)", attempt, c.Status().NodeID)
					return // new watcher armed by Connect
				}
				if ce, ok := err.(*ConnectError); ok && !ce.Retryable {
					c.logf0("gateway agent: auto-reconnect stopped: %v", ce)
					return
				}
				lastErr = err
			}
		}
	}()
}

// Disconnect closes the tunnel and sets the operator-disconnected flag, which
// suppresses automatic reconnect until an explicit Connect. It never writes
// configuration: "disconnect this run" and "disable in config" are distinct.
func (c *Controller) Disconnect(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	c.status.OperatorDisconnected = true
	// Invalidate any in-flight auto-reconnect loop: an operator disconnect must
	// win over a drop that is already backing off. The connect() guard refuses
	// an auto dial even if the loop reaches its next attempt first.
	c.reconnectGen++
	switch c.state {
	case StateDisabled:
		c.mu.Unlock()
		return nil
	case StateDisconnected:
		c.mu.Unlock()
		return nil
	case StateDisconnecting:
		c.mu.Unlock()
		return nil
	}
	c.transition(StateDisconnecting)
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()

	// Operator disconnect: the gateway is no longer the sender; let local
	// notifications resume immediately.
	c.ApplyNotifySuppress(nil)
	if conn != nil {
		_ = conn.Close()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.NodeID = ""
	c.status.ConsoleURL = ""
	c.status.Mounts = nil
	c.status.ConnectedAt = time.Time{}
	if c.state == StateDisconnecting {
		c.transition(StateDisconnected)
	}
	return nil
}

// Reconnect performs a disconnect followed by a connect, so the operator flag
// is cleared and a fresh tunnel is established. The disconnect flag is only
// transient here.
func (c *Controller) Reconnect(ctx context.Context) error {
	if err := c.Disconnect(ctx); err != nil {
		return err
	}
	return c.ConnectManual(ctx)
}

// HandleConnectionLost is called by the host when a live tunnel drops. It
// applies the reconnect policy:
//   - operator-disconnected: settle to Disconnected (no retry),
//   - terminal failure (retryable=false, e.g. auth_invalid): settle to Error,
//   - otherwise: enter Reconnecting and let the host schedule a retry.
func (c *Controller) HandleConnectionLost(code, message string, retryable bool) {
	c.mu.Lock()

	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.status.NodeID = ""
	c.status.ConsoleURL = ""
	c.status.Mounts = nil
	c.status.ConnectedAt = time.Time{}
	c.status.ErrorCode = code
	c.status.LastError = message
	c.status.Retryable = retryable

	switch {
	case c.status.OperatorDisconnected:
		c.transition(StateDisconnected)
	case !retryable:
		c.transition(StateError)
	default:
		c.status.ReconnectCount++
		c.transition(StateReconnecting)
	}
	c.mu.Unlock()
	// The gateway cannot delegate while the tunnel is down: clear the rules so
	// local bot notifications resume instead of being silently muted.
	c.ApplyNotifySuppress(nil)
}

// Configure replaces the configuration and reconciles the enabled flag with the
// current state. Disabling a live connection triggers a disconnect; enabling a
// disabled controller moves it to Disconnected (ready to Connect). It does not
// auto-connect.
func (c *Controller) Configure(cfg Config) error {
	c.mu.Lock()
	c.cfg = cfg
	c.status.TokenHint = cfg.TokenHint()
	c.status.GatewayURL = strings.TrimSpace(cfg.URL)
	wasDisabled := c.state == StateDisabled
	switch {
	case !cfg.Enabled:
		if wasDisabled {
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		_ = c.Disconnect(context.Background())
		c.mu.Lock()
		c.transition(StateDisabled)
	case wasDisabled:
		c.transition(StateDisconnected)
	}
	c.mu.Unlock()
	return nil
}
