package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestEncodeGoldenBytes(t *testing.T) {
	f := Frame{
		Version:  ProtocolVersion,
		Type:     TypeStreamData,
		Flags:    FlagMore,
		StreamID: 1,
		Payload:  []byte("hi"),
	}
	got, err := Encode(f)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := []byte{
		0x01,       // version
		0x0B,       // type STREAM_DATA
		0x08, 0x00, // flags MORE (little-endian)
		0x01, 0x00, 0x00, 0x00, // stream id 1
		0x02, 0x00, 0x00, 0x00, // payload len 2
		'h', 'i',
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encoded bytes mismatch\n got=% x\nwant=% x", got, want)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	f := Frame{Version: ProtocolVersion, Type: TypeRegister, Flags: FlagJSON, StreamID: 0, Payload: []byte(`{"node_id":"n1"}`)}
	raw, err := Encode(f)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != f.Version || got.Type != f.Type || got.Flags != f.Flags || got.StreamID != f.StreamID {
		t.Fatalf("header mismatch: %+v vs %+v", got, f)
	}
	if !bytes.Equal(got.Payload, f.Payload) {
		t.Fatalf("payload mismatch: %q vs %q", got.Payload, f.Payload)
	}
}

func TestDecodeEmptyPayload(t *testing.T) {
	raw, err := Encode(Frame{Version: ProtocolVersion, Type: TypePing, StreamID: 0})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Payload) != 0 {
		t.Fatalf("expected empty payload, got %q", got.Payload)
	}
}

func TestDecodeShortFrame(t *testing.T) {
	if _, err := Decode([]byte{0x01, 0x02, 0x03}); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("expected ErrShortFrame, got %v", err)
	}
}

func TestDecodeDeclaredLenBeyondBuffer(t *testing.T) {
	header := make([]byte, HeaderSize)
	header[0] = ProtocolVersion
	header[1] = byte(TypeRegister)
	binary.LittleEndian.PutUint32(header[8:], 100) // claims 100 bytes, none present
	if _, err := Decode(header); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("expected ErrShortFrame, got %v", err)
	}
}

func TestDecodeTooLarge(t *testing.T) {
	header := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint32(header[8:], MaxPayloadLen+1)
	if _, err := Decode(header); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestEncodeTooLarge(t *testing.T) {
	if _, err := Encode(Frame{Payload: make([]byte, MaxPayloadLen+1)}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestReadWriteFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	frames := []Frame{
		{Version: ProtocolVersion, Type: TypeHello, Flags: FlagJSON, StreamID: 0, Payload: []byte(`{"protocol_version":1}`)},
		{Version: ProtocolVersion, Type: TypeStreamData, Flags: FlagMore, StreamID: 3, Payload: []byte("chunk")},
		{Version: ProtocolVersion, Type: TypeStreamEnd, StreamID: 3},
	}
	for _, f := range frames {
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatalf("write %s: %v", f.Type, err)
		}
	}
	for i, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		if got.Type != want.Type || got.StreamID != want.StreamID || got.Flags != want.Flags {
			t.Fatalf("frame %d header mismatch: %+v vs %+v", i, got, want)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("frame %d payload mismatch: %q vs %q", i, got.Payload, want.Payload)
		}
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF at end of stream, got %v", err)
	}
}

func TestReadFrameTooLarge(t *testing.T) {
	header := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint32(header[8:], MaxPayloadLen+1)
	if _, err := ReadFrame(bytes.NewReader(header)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestFrameTypeString(t *testing.T) {
	cases := map[FrameType]string{
		TypeHello:       "HELLO",
		TypeRegister:    "REGISTER",
		TypeRegisterAck: "REGISTER_ACK",
		TypeStreamData:  "STREAM_DATA",
		TypeNotify:      "NOTIFY",
		FrameType(0x99): "FRAME(0x99)",
	}
	for typ, want := range cases {
		if got := typ.String(); got != want {
			t.Fatalf("FrameType(0x%02x).String() = %q, want %q", uint8(typ), got, want)
		}
	}
}

func TestWindowUpdateRoundTrip(t *testing.T) {
	frame := EncodeWindowUpdate(7, 4096)
	if frame.Type != TypeWindowUpdate || frame.StreamID != 7 {
		t.Fatalf("unexpected frame: %+v", frame)
	}
	delta, ok := DecodeWindowUpdate(frame)
	if !ok || delta != 4096 {
		t.Fatalf("delta = %d ok=%v, want 4096/true", delta, ok)
	}
	if _, ok := DecodeWindowUpdate(Frame{Type: TypePing}); ok {
		t.Fatal("non-window frame must not decode as a window update")
	}
}
