package tunnel

import (
	"sync"

	"github.com/gorilla/websocket"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// streamEventKind classifies an event delivered to a proxy waiting on a stream.
type streamEventKind int

const (
	streamHeaders streamEventKind = iota
	streamData
	streamEnd
	streamReset
)

// streamEvent is one event of a reverse-proxy stream.
type streamEvent struct {
	kind    streamEventKind
	status  int
	headers map[string][]string
	data    []byte
	code    string
	message string
}

// session is one live agent connection and owns its stream table.
type session struct {
	nodeID  string
	ws      *websocket.Conn
	writeMu sync.Mutex

	mu           sync.Mutex
	nextStreamID uint32
	streams      map[uint32]chan streamEvent
}

func newSession(nodeID string, ws *websocket.Conn) *session {
	return &session{
		nodeID:       nodeID,
		ws:           ws,
		nextStreamID: 1,
		streams:      make(map[uint32]chan streamEvent),
	}
}

// allocStream reserves a gateway-initiated stream id and its event channel.
func (s *session) allocStream() (uint32, chan streamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextStreamID
	s.nextStreamID += 2 // odd ids for gateway-initiated streams
	ch := make(chan streamEvent, 256)
	s.streams[id] = ch
	return id, ch
}

func (s *session) releaseStream(id uint32) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

// dispatch delivers an event to the stream's consumer. It blocks when the
// consumer is slow; flow control (WINDOW_UPDATE) lands in a later step.
func (s *session) dispatch(id uint32, ev streamEvent) {
	s.mu.Lock()
	ch := s.streams[id]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- ev
}

// closeAll fails every in-flight stream (connection ended).
func (s *session) closeAll(code, message string) {
	s.mu.Lock()
	streams := s.streams
	s.streams = make(map[uint32]chan streamEvent)
	s.mu.Unlock()
	for _, ch := range streams {
		select {
		case ch <- streamEvent{kind: streamReset, code: code, message: message}:
		default:
		}
	}
}

func (s *session) writeFrame(f wire.Frame) error {
	data, err := wire.Encode(f)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ws.WriteMessage(websocket.BinaryMessage, data)
}

func (s *session) writeJSON(t wire.FrameType, streamID uint32, v any) error {
	f, err := wire.NewJSONFrame(t, streamID, v)
	if err != nil {
		return err
	}
	return s.writeFrame(f)
}
