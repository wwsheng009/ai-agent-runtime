// Package envdclient is a small Connect-RPC client for envd.
//
// It speaks the subset of the envd API that ai-agent-runtime needs to use envd as
// a remote execution transport: process lifecycle (start / stream / signal), the
// filesystem family (stat / list / mkdir / remove / move / watch) and the host
// metrics endpoint.
//
// Design constraints that shaped this package:
//
//   - Standard library only. envd is reached over plain HTTP/1.1 or HTTP/2, and
//     Connect's unary and server-streaming shapes are expressible with net/http
//     plus a five-byte envelope. Pulling in a generated protobuf runtime would add
//     a large dependency to the runtime for a few dozen fields.
//   - No policy. envd does not jail paths and does not filter commands, so every
//     caller in this repository must validate the path or command BEFORE calling
//     here. This package deliberately offers no sandbox options, because pretending
//     it could enforce them would be worse than not offering them.
//   - Fail loudly. A silent empty result is indistinguishable from an empty
//     directory; every transport and protocol problem returns an error.
package envdclient

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Connect streaming envelope flags.
//
// A stream body is a sequence of frames, each of which is one flag byte, a
// four-byte big-endian payload length, then that many bytes of payload. The flag
// distinguishes a normal message from the terminal frame that carries either
// metadata or an error.
const (
	// flagMessage marks an ordinary message payload.
	flagMessage byte = 0x00
	// flagEndStream marks the terminal frame. Its payload is a JSON object with
	// either "error" or "metadata", never a message.
	flagEndStream byte = 0x02
	// flagCompressed marks a compressed payload. envd does not compress JSON
	// streams, so encountering it means the server and this client disagree about
	// content encoding; report rather than silently misparse.
	flagCompressed byte = 0x01

	// frameHeaderBytes is the fixed envelope header size.
	frameHeaderBytes = 5
	// maxFramePayload bounds one frame so a corrupt or hostile length prefix
	// cannot make the client allocate unbounded memory. envd's own largest
	// practical payload is a directory listing or an output chunk.
	maxFramePayload = 16 << 20
)

// EncodeFrame wraps payload in a Connect stream envelope.
func EncodeFrame(flag byte, payload []byte) []byte {
	frame := make([]byte, frameHeaderBytes+len(payload))
	frame[0] = flag
	binary.BigEndian.PutUint32(frame[1:frameHeaderBytes], uint32(len(payload)))
	copy(frame[frameHeaderBytes:], payload)
	return frame
}

// EncodeJSONFrame marshals v and wraps it in a message frame.
func EncodeJSONFrame(v any) ([]byte, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return EncodeFrame(flagMessage, payload), nil
}

// Frame is one decoded Connect stream frame.
type Frame struct {
	Flags   byte
	Payload []byte
}

// ReadFrame reads and decodes one envelope frame.
//
// It returns io.EOF at a clean end of stream (no partial header). A truncated
// header or payload returns io.ErrUnexpectedEOF so a dropped connection is never
// mistaken for a normal completion.
func ReadFrame(r io.Reader) (Frame, error) {
	header := make([]byte, frameHeaderBytes)
	if _, err := io.ReadFull(r, header); err != nil {
		if err == io.EOF {
			return Frame{}, io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return Frame{}, fmt.Errorf("truncated stream envelope header: %w", err)
		}
		return Frame{}, fmt.Errorf("read stream envelope header: %w", err)
	}
	flags := header[0]
	length := binary.BigEndian.Uint32(header[1:frameHeaderBytes])
	if length > maxFramePayload {
		return Frame{}, fmt.Errorf("stream envelope declares %d bytes, above the %d byte limit", length, maxFramePayload)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Frame{}, fmt.Errorf("read stream envelope payload of %d bytes: %w", length, err)
	}
	return Frame{Flags: flags, Payload: payload}, nil
}

// endStreamEnvelope is the terminal frame body.
//
// Connect puts an error or trailing metadata here. Only one of the two fields is
// ever set.
type endStreamEnvelope struct {
	Error    *ConnectError   `json:"error,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// ConnectError is a Connect error payload.
//
// Code is one of the Connect codes (unauthenticated, invalid_argument,
// not_found, permission_denied, internal, unavailable, ...). envd returns
// unauthenticated for a missing user and invalid_argument for a malformed
// request, so callers can distinguish "I am misconfigured" from "the request was
// wrong" without string matching.
type ConnectError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	} `json:"details,omitempty"`
}

// Error implements error.
func (e *ConnectError) Error() string {
	if e == nil {
		return "envd: unknown error"
	}
	if e.Code == "" {
		return "envd: " + e.Message
	}
	return fmt.Sprintf("envd: %s: %s", e.Code, e.Message)
}

// IsUnauthenticated reports whether the error is a missing or bad credential.
func (e *ConnectError) IsUnauthenticated() bool {
	return e != nil && e.Code == "unauthenticated"
}

// IsNotFound reports whether the error is an absent path or process.
func (e *ConnectError) IsNotFound() bool {
	return e != nil && (e.Code == "not_found" || e.Code == "not_found_error")
}

// parseEndStream turns a terminal frame payload into an error, or nil when the
// stream completed without one.
func parseEndStream(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	var envelope endStreamEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode end-stream frame: %w", err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	return nil
}
