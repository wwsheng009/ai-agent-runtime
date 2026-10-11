package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// Config configures the tunnel server.
type Config struct {
	// ServerVersion is reported in WELCOME.
	ServerVersion string
	// HeartbeatIntervalSec is advertised in WELCOME (0 leaves the agent default).
	HeartbeatIntervalSec int
	// PublicBaseURL is the externally reachable base URL, used to build the
	// console URL returned in REGISTER_ACK.
	PublicBaseURL string
	// Registry overrides the node registry (defaults to a fresh one).
	Registry *Registry
	// NodeIDFunc overrides node id assignment.
	NodeIDFunc func(wire.Register) string
	// Authenticate validates the upgrade request (device token). nil accepts all.
	Authenticate func(*http.Request) error
	// Logf receives non-fatal diagnostics; nil disables logging.
	Logf func(format string, args ...any)
	// OnRouteDenied is called when a proxy request is rejected by the route
	// allowlist (used for penalty bans). Optional.
	OnRouteDenied func(*http.Request)
	// OnRegistryChanged is called when the node registry changes (register,
	// unregister or session update); used to push live console updates. Optional.
	OnRegistryChanged func()
	// DataDir enables registry persistence (registry.json under DataDir,
	// debounced). Empty disables persistence. Optional.
	DataDir string
	// Upgrader overrides the WebSocket upgrader (tests).
	Upgrader *websocket.Upgrader
}

// Server handles the /gw/agent WebSocket endpoint.
type Server struct {
	cfg      Config
	registry *Registry
	upgrader websocket.Upgrader

	sessMu   sync.RWMutex
	sessions map[string]*session

	// notifyMu guards notifySeen, a bounded set of recent NOTIFY dedup keys
	// (docs/gateway/08-notifications.md §4.1).
	notifyMu   sync.Mutex
	notifySeen map[string]struct{}

	// persist debounces registry snapshots (nil when persistence is disabled).
	persist *persister
}

// NewServer builds a tunnel server.
func NewServer(cfg Config) *Server {
	registry := cfg.Registry
	if registry == nil {
		registry = NewRegistry()
	}
	upgrader := websocket.Upgrader{
		// Agents are non-browser clients and authenticate with a bearer device
		// token, so the Origin check is not the security boundary here.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	if cfg.Upgrader != nil {
		upgrader = *cfg.Upgrader
	}
	return &Server{
		cfg:        cfg,
		registry:   registry,
		upgrader:   upgrader,
		sessions:   make(map[string]*session),
		notifySeen: make(map[string]struct{}),
		persist:    NewPersister(registry, cfg.DataDir, 0),
	}
}

// registerSession records a live connection for reverse proxying.
func (s *Server) registerSession(nodeID string, sess *session) {
	s.sessMu.Lock()
	s.sessions[nodeID] = sess
	s.sessMu.Unlock()
}

// unregisterSession removes the connection only if it is still the registered
// one (a reconnecting node may have replaced it).
func (s *Server) unregisterSession(nodeID string, sess *session) {
	s.sessMu.Lock()
	if s.sessions[nodeID] == sess {
		delete(s.sessions, nodeID)
	}
	s.sessMu.Unlock()
}

// session returns the live connection for a node.
func (s *Server) session(nodeID string) (*session, bool) {
	s.sessMu.RLock()
	defer s.sessMu.RUnlock()
	sess, ok := s.sessions[nodeID]
	return sess, ok
}

// Registry returns the node registry.
func (s *Server) Registry() *Registry { return s.registry }

// PersistSnapshot forces a registry snapshot write (used on shutdown, after the
// debounced scheduled write has been cancelled or not yet fired). No-op when
// persistence is disabled.
func (s *Server) PersistSnapshot() {
	if s == nil || s.persist == nil {
		return
	}
	s.persist.Close()
}

// CloseAll drains every live agent connection: each agent receives GOAWAY and
// closes, its registry entry is marked stale (preserving the last-known
// sessions/attention for the snapshot the standby restores), and in-flight
// proxy streams fail with a clean reset. Agents reconnect (their retryable
// connection-lost path) to the next gateway instance and flip the entry back
// to live — this is the active/passive failover handoff. Entries are marked
// rather than removed so a graceful shutdown leaves the same snapshot a crash
// would; the reason string is advisory metadata for the agent's logs.
func (s *Server) CloseAll(reason string) {
	if s == nil {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "gateway shutting down"
	}
	s.sessMu.Lock()
	sessions := make(map[string]*session, len(s.sessions))
	for id, sess := range s.sessions {
		sessions[id] = sess
	}
	s.sessMu.Unlock()
	for nodeID, sess := range sessions {
		_ = sess.writeJSON(wire.TypeGoAway, 0, wire.GoAway{
			Code:    "shutdown",
			Message: reason,
		})
		_ = sess.ws.Close()
		sess.closeAll("node_offline", reason)
		if cur, ok := s.registry.Get(nodeID); ok && !cur.Stale {
			cur.Stale = true
			s.registry.Upsert(cur)
		}
	}
	s.notifyRegistryChangedAndPersist()
}

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}

func (s *Server) notifyRegistryChanged() {
	if s.cfg.OnRegistryChanged != nil {
		s.cfg.OnRegistryChanged()
	}
}

// notifyRegistryChangedAndPersist fans out a registry change to the callback
// and schedules a debounced snapshot write.
func (s *Server) notifyRegistryChangedAndPersist() {
	s.notifyRegistryChanged()
	if s.persist != nil {
		s.persist.Schedule()
	}
}

// ServeHTTP upgrades the connection and serves the tunnel.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Authenticate != nil {
		if err := s.cfg.Authenticate(r); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	if err := s.serveConn(ws, r); err != nil {
		s.logf("gateway tunnel: connection ended: %v", err)
	}
}

func (s *Server) serveConn(ws *websocket.Conn, r *http.Request) error {
	// HELLO → WELCOME.
	frame, err := readFrame(ws)
	if err != nil {
		return err
	}
	if frame.Type != wire.TypeHello {
		_ = writeJSON(ws, wire.TypeError, wire.ErrorMessage{Code: wire.CodeInternalError, Message: "expected HELLO"})
		return fmt.Errorf("expected HELLO, got %s", frame.Type)
	}
	hello, err := wire.DecodePayload[wire.Hello](frame)
	if err != nil {
		return err
	}
	if hello.ProtocolVersion != wire.ProtocolVersion {
		_ = writeJSON(ws, wire.TypeError, wire.ErrorMessage{
			Code:    wire.CodeProtocolVersionUnsupported,
			Message: fmt.Sprintf("unsupported protocol version %d", hello.ProtocolVersion),
		})
		return fmt.Errorf("protocol version %d unsupported", hello.ProtocolVersion)
	}
	if err := writeJSON(ws, wire.TypeWelcome, wire.Welcome{
		ServerVersion:        s.cfg.ServerVersion,
		HeartbeatIntervalSec: s.cfg.HeartbeatIntervalSec,
		MaxFrame:             int(wire.DefaultMaxFrameSize),
		Capabilities:         []string{"resume", "flow_control"},
	}); err != nil {
		return err
	}

	// REGISTER → REGISTER_ACK.
	frame, err = readFrame(ws)
	if err != nil {
		return err
	}
	if frame.Type != wire.TypeRegister {
		_ = writeJSON(ws, wire.TypeError, wire.ErrorMessage{Code: wire.CodeInternalError, Message: "expected REGISTER"})
		return fmt.Errorf("expected REGISTER, got %s", frame.Type)
	}
	reg, err := wire.DecodePayload[wire.Register](frame)
	if err != nil {
		return err
	}
	nodeID := s.assignNodeID(reg)
	node := &Node{
		NodeID:        nodeID,
		Kind:          reg.Kind,
		Name:          reg.Name,
		DeviceName:    reg.DeviceName,
		Version:       reg.Version,
		OS:            reg.OS,
		Arch:          reg.Arch,
		Capabilities:  reg.Capabilities,
		Routes:        reg.Routes,
		Sessions:      reg.Sessions,
		RemoteAddr:    remoteAddr(r),
		RegisteredAt:  nowUTC(),
		LastHeartbeat: nowUTC(),
	}
	s.registry.Upsert(node)
	s.notifyRegistryChangedAndPersist()
	defer func() {
		// Only downgrade our own registration: identify the entry by the
		// RemoteAddr observed at handshake (the heartbeat loop rewrites
		// LastHeartbeat, so time comparisons are unusable). The entry becomes a
		// stale last-known snapshot — identical to the crash path — and the
		// reconnecting agent flips it back to live. Removing outright would
		// erase the attention state that the snapshot (and, on failover, the
		// standby's Seed) depends on; skipping the downgrade would leak a live
		// row when the owner never returns.
		if cur, ok := s.registry.Get(nodeID); ok && !cur.Stale && cur.RemoteAddr == node.RemoteAddr {
			cur.Stale = true
			s.registry.Upsert(cur)
		}
		s.notifyRegistryChangedAndPersist()
	}()

	sess := newSession(nodeID, ws)
	s.registerSession(nodeID, sess)
	defer func() {
		s.unregisterSession(nodeID, sess)
		sess.closeAll("node_offline", "agent connection closed")
	}()

	if err := writeJSON(ws, wire.TypeRegisterAck, wire.RegisterAck{
		NodeID:         nodeID,
		ConsoleURL:     s.consoleURL(nodeID),
		AcceptedRoutes: reg.Routes,
	}); err != nil {
		return err
	}

	// Control loop.
	for {
		frame, err := readFrame(ws)
		if err != nil {
			return err
		}
		switch frame.Type {
		case wire.TypeHeartbeat:
			if updated, ok := s.registry.Get(nodeID); ok {
				updated.LastHeartbeat = nowUTC()
				s.registry.Upsert(updated)
			}
			if err := writeJSON(ws, wire.TypeHeartbeatAck, wire.HeartbeatAck{ServerTime: nowUTC()}); err != nil {
				return err
			}
		case wire.TypePing:
			if err := writeJSON(ws, wire.TypePong, map[string]any{}); err != nil {
				return err
			}
		case wire.TypeDeregister, wire.TypeGoAway:
			return nil
		case wire.TypeStreamData:
			if frame.IsJSON() {
				headers, decodeErr := wire.DecodePayload[wire.ResponseHeaders](frame)
				if decodeErr == nil {
					sess.dispatch(frame.StreamID, streamEvent{
						kind:    streamHeaders,
						status:  headers.Status,
						headers: headers.Headers,
					})
				}
				continue
			}
			sess.dispatch(frame.StreamID, streamEvent{kind: streamData, data: frame.Payload})
		case wire.TypeStreamEnd:
			sess.dispatch(frame.StreamID, streamEvent{kind: streamEnd})
		case wire.TypeStreamReset:
			reset, _ := wire.DecodePayload[wire.StreamReset](frame)
			sess.dispatch(frame.StreamID, streamEvent{kind: streamReset, code: reset.Code, message: reset.Message})
		case wire.TypeSessionUpdate:
			update, decodeErr := wire.DecodePayload[wire.SessionUpdate](frame)
			if decodeErr == nil {
				s.applySessionUpdate(nodeID, update)
			}
		case wire.TypeNotify:
			notify, decodeErr := wire.DecodePayload[wire.Notify](frame)
			if decodeErr == nil {
				s.applyNotify(nodeID, notify)
			}
		default:
			// Unknown frames are ignored for forward compatibility.
		}
	}
}

func (s *Server) assignNodeID(reg wire.Register) string {
	if s.cfg.NodeIDFunc != nil {
		return s.cfg.NodeIDFunc(reg)
	}
	if id := strings.TrimSpace(reg.NodeID); id != "" {
		return id
	}
	return "node-" + kindOrDefault(reg.Kind) + "-" + randomHex(6)
}

func (s *Server) consoleURL(nodeID string) string {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.PublicBaseURL), "/")
	if base == "" || nodeID == "" {
		return ""
	}
	return base + "/n/" + nodeID + "/web/"
}

func kindOrDefault(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "agent"
	}
	return kind
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Second) }

func remoteAddr(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.RemoteAddr
}

func readFrame(ws *websocket.Conn) (wire.Frame, error) {
	_, data, err := ws.ReadMessage()
	if err != nil {
		return wire.Frame{}, err
	}
	return wire.Decode(data)
}

func writeFrame(ws *websocket.Conn, f wire.Frame) error {
	data, err := wire.Encode(f)
	if err != nil {
		return err
	}
	return ws.WriteMessage(websocket.BinaryMessage, data)
}

func writeJSON(ws *websocket.Conn, t wire.FrameType, v any) error {
	f, err := wire.NewJSONFrame(t, 0, v)
	if err != nil {
		return err
	}
	return writeFrame(ws, f)
}
