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
		cfg:      cfg,
		registry: registry,
		upgrader: upgrader,
		sessions: make(map[string]*session),
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

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
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
	defer s.registry.Remove(nodeID)

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
