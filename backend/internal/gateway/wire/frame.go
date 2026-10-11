// Package wire defines the on-the-wire frame format and control messages shared
// by the gateway server (tunnel) and the client-side agent. Both sides must use
// this single implementation so they cannot drift on byte layout.
//
// Frame layout (12-byte header, little-endian), see
// docs/gateway/03-protocol.md §2:
//
//	 0        1        2        3        4        5        6        7
//	+--------+--------+--------+--------+--------+--------+--------+--------+
//	| ver(1) | type(1)| flags(2)|            stream_id(4)   |  payload_len(4) |
//	+--------+--------+--------+--------+--------+--------+--------+--------+
//	|                            payload (payload_len bytes)                  |
//	+-------------------------------------------------------------------------+
//
// The package depends only on the standard library.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ProtocolVersion is the current major version of the frame protocol. A peer
// with a different major version must refuse the connection (HELLO/WELCOME).
const ProtocolVersion uint8 = 1

// HeaderSize is the fixed frame header size in bytes.
const HeaderSize = 12

// MaxPayloadLen is the absolute payload cap accepted by Decode/ReadFrame. It
// protects against a hostile header claiming a huge payload. Peers negotiate a
// smaller working max_frame via WELCOME.
const MaxPayloadLen uint32 = 1 << 24 // 16 MiB

// DefaultMaxFrameSize is the advertised default working frame size.
const DefaultMaxFrameSize = 1 << 20 // 1 MiB

// FrameType enumerates the protocol frame types.
type FrameType uint8

// Frame types (docs/gateway/03-protocol.md §2.1).
const (
	TypeHello         FrameType = 0x01
	TypeWelcome       FrameType = 0x02
	TypeRegister      FrameType = 0x03
	TypeRegisterAck   FrameType = 0x04
	TypeHeartbeat     FrameType = 0x05
	TypeHeartbeatAck  FrameType = 0x06
	TypeSessionUpdate FrameType = 0x07
	TypePing          FrameType = 0x08
	TypePong          FrameType = 0x09
	TypeStreamOpen    FrameType = 0x0A
	TypeStreamData    FrameType = 0x0B
	TypeStreamEnd     FrameType = 0x0C
	TypeStreamReset   FrameType = 0x0D
	TypeWindowUpdate  FrameType = 0x0E
	TypeGoAway        FrameType = 0x0F
	TypeDeregister    FrameType = 0x10
	TypeError         FrameType = 0x11
	TypeNotify        FrameType = 0x12
)

// String renders a frame type for logs/tests.
func (t FrameType) String() string {
	switch t {
	case TypeHello:
		return "HELLO"
	case TypeWelcome:
		return "WELCOME"
	case TypeRegister:
		return "REGISTER"
	case TypeRegisterAck:
		return "REGISTER_ACK"
	case TypeHeartbeat:
		return "HEARTBEAT"
	case TypeHeartbeatAck:
		return "HEARTBEAT_ACK"
	case TypeSessionUpdate:
		return "SESSION_UPDATE"
	case TypePing:
		return "PING"
	case TypePong:
		return "PONG"
	case TypeStreamOpen:
		return "STREAM_OPEN"
	case TypeStreamData:
		return "STREAM_DATA"
	case TypeStreamEnd:
		return "STREAM_END"
	case TypeStreamReset:
		return "STREAM_RESET"
	case TypeWindowUpdate:
		return "WINDOW_UPDATE"
	case TypeGoAway:
		return "GOAWAY"
	case TypeDeregister:
		return "DEREGISTER"
	case TypeError:
		return "ERROR"
	case TypeNotify:
		return "NOTIFY"
	default:
		return fmt.Sprintf("FRAME(0x%02x)", uint8(t))
	}
}

// Frame flags.
const (
	// FlagFin marks the final frame of a stream half-close.
	FlagFin uint16 = 0x0001
	// FlagCompressed marks a compressed payload.
	FlagCompressed uint16 = 0x0002
	// FlagJSON marks a UTF-8 JSON payload (control messages, response headers).
	FlagJSON uint16 = 0x0004
	// FlagMore marks that more data follows on the stream.
	FlagMore uint16 = 0x0008
)

// Frame is a decoded protocol frame.
type Frame struct {
	Version  uint8
	Type     FrameType
	Flags    uint16
	StreamID uint32
	Payload  []byte
}

// IsJSON reports whether the payload is JSON.
func (f Frame) IsJSON() bool { return f.Flags&FlagJSON != 0 }

// Errors returned by the codec.
var (
	// ErrShortFrame means the buffer/stream ended before a whole frame arrived.
	ErrShortFrame = errors.New("gateway/wire: short frame")
	// ErrFrameTooLarge means the declared payload exceeds MaxPayloadLen.
	ErrFrameTooLarge = errors.New("gateway/wire: frame too large")
)

// Encode serialises a frame to its wire bytes. The returned slice is freshly
// allocated and safe to retain.
func Encode(f Frame) ([]byte, error) {
	if uint32(len(f.Payload)) > MaxPayloadLen {
		return nil, ErrFrameTooLarge
	}
	buf := make([]byte, HeaderSize+len(f.Payload))
	buf[0] = f.Version
	buf[1] = byte(f.Type)
	binary.LittleEndian.PutUint16(buf[2:], f.Flags)
	binary.LittleEndian.PutUint32(buf[4:], f.StreamID)
	binary.LittleEndian.PutUint32(buf[8:], uint32(len(f.Payload)))
	copy(buf[HeaderSize:], f.Payload)
	return buf, nil
}

// Decode parses a single frame from data. Trailing bytes after the payload are
// ignored; use ReadFrame for stream framing.
func Decode(data []byte) (Frame, error) {
	if len(data) < HeaderSize {
		return Frame{}, ErrShortFrame
	}
	payloadLen := binary.LittleEndian.Uint32(data[8:12])
	if payloadLen > MaxPayloadLen {
		return Frame{}, ErrFrameTooLarge
	}
	if uint64(len(data)) < uint64(HeaderSize)+uint64(payloadLen) {
		return Frame{}, ErrShortFrame
	}
	payload := make([]byte, payloadLen)
	copy(payload, data[HeaderSize:HeaderSize+int(payloadLen)])
	return Frame{
		Version:  data[0],
		Type:     FrameType(data[1]),
		Flags:    binary.LittleEndian.Uint16(data[2:4]),
		StreamID: binary.LittleEndian.Uint32(data[4:8]),
		Payload:  payload,
	}, nil
}

// ReadFrame reads one frame from r. It returns io.EOF (possibly wrapped) when
// the stream ends cleanly before a frame starts.
func ReadFrame(r io.Reader) (Frame, error) {
	var header [HeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	payloadLen := binary.LittleEndian.Uint32(header[8:12])
	if payloadLen > MaxPayloadLen {
		return Frame{}, ErrFrameTooLarge
	}
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, ErrShortFrame
		}
	}
	return Frame{
		Version:  header[0],
		Type:     FrameType(header[1]),
		Flags:    binary.LittleEndian.Uint16(header[2:4]),
		StreamID: binary.LittleEndian.Uint32(header[4:8]),
		Payload:  payload,
	}, nil
}

// WriteFrame serialises and writes one frame to w in a single Write call.
func WriteFrame(w io.Writer, f Frame) error {
	buf, err := Encode(f)
	if err != nil {
		return err
	}
	_, err = w.Write(buf)
	return err
}

// EncodeWindowUpdate builds a WINDOW_UPDATE frame carrying a 4-byte
// little-endian credit delta (docs/gateway/03-protocol.md §2.1).
func EncodeWindowUpdate(streamID, delta uint32) Frame {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, delta)
	return Frame{Version: ProtocolVersion, Type: TypeWindowUpdate, StreamID: streamID, Payload: payload}
}

// DecodeWindowUpdate reads the credit delta from a WINDOW_UPDATE frame.
func DecodeWindowUpdate(f Frame) (uint32, bool) {
	if f.Type != TypeWindowUpdate || len(f.Payload) < 4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(f.Payload), true
}
