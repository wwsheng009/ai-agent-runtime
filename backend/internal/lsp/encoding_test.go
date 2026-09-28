package lsp

import (
	"bytes"
	"testing"
)

func TestStripBOM(t *testing.T) {
	with := append([]byte{0xEF, 0xBB, 0xBF}, []byte("package main\n")...)
	stripped, bom := StripBOM(with)
	if bom != 3 || !bytes.Equal(stripped, []byte("package main\n")) {
		t.Fatalf("StripBOM(BOM) = %q,%d; want content without BOM,3", stripped, bom)
	}
	plain, bom := StripBOM([]byte("package main\n"))
	if bom != 0 || !bytes.Equal(plain, []byte("package main\n")) {
		t.Fatalf("StripBOM(no BOM) = %q,%d", plain, bom)
	}
}

func TestNegotiateEncodingDefaultsToUTF16(t *testing.T) {
	cases := []struct {
		raw  string
		want PositionEncoding
	}{
		{"", EncodingUTF16},
		{"utf-16", EncodingUTF16},
		{"utf-8", EncodingUTF8},
		{"utf-32", EncodingUTF32},
		{"latin-1", EncodingUTF16},
	}
	for _, tc := range cases {
		if got := NegotiateEncoding(tc.raw); got != tc.want {
			t.Fatalf("NegotiateEncoding(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestProtocolToCanonicalUTF16WithMultibyteText(t *testing.T) {
	// a(1 unit) 中(1) 文(1) b(1), then newline.
	content := []byte("a中文b\n")
	cases := []struct {
		character int
		wantCol   int
	}{
		{0, 0},
		{1, 1},
		{2, 4}, // after 中 (3 bytes)
		{3, 7}, // after 文
		{4, 8},
		{99, 8}, // clamped to line end
	}
	for _, tc := range cases {
		got := ProtocolToCanonical(content, EncodingUTF16, ProtocolPosition{Line: 0, Character: tc.character})
		if got.Column != tc.wantCol {
			t.Fatalf("UTF16 character %d -> byte %d, want %d", tc.character, got.Column, tc.wantCol)
		}
	}
}

func TestProtocolToCanonicalNeverSplitsSurrogatePair(t *testing.T) {
	// a(1) 😀(2 UTF-16 units, 4 bytes) b(1)
	content := []byte("a😀b\n")
	got := ProtocolToCanonical(content, EncodingUTF16, ProtocolPosition{Line: 0, Character: 2})
	if got.Column != 1 {
		t.Fatalf("a column inside a surrogate pair must round down to the rune start, got byte %d", got.Column)
	}
	got = ProtocolToCanonical(content, EncodingUTF16, ProtocolPosition{Line: 0, Character: 3})
	if got.Column != 5 {
		t.Fatalf("after the emoji the byte column must be 5, got %d", got.Column)
	}
}

func TestProtocolToCanonicalUTF8AndUTF32(t *testing.T) {
	content := []byte("a😀b\n")
	// UTF-8 columns are byte columns by definition.
	if got := ProtocolToCanonical(content, EncodingUTF8, ProtocolPosition{Line: 0, Character: 5}); got.Column != 5 {
		t.Fatalf("UTF8 byte column = %d, want 5", got.Column)
	}
	// UTF-32 counts code points: a=1, emoji=2, b=3.
	if got := ProtocolToCanonical(content, EncodingUTF32, ProtocolPosition{Line: 0, Character: 3}); got.Column != 6 {
		t.Fatalf("UTF32 rune column 3 -> byte 6, got %d", got.Column)
	}
}

func TestCanonicalToProtocolRoundTrip(t *testing.T) {
	content := []byte("a中文b\nsecond line\n")
	for _, col := range []int{0, 1, 4, 7, 8} {
		canonical := CanonicalPos{Line: 0, Column: col}
		protocol := CanonicalToProtocol(content, EncodingUTF16, canonical)
		back := ProtocolToCanonical(content, EncodingUTF16, protocol)
		if back.Line != canonical.Line || back.Column != canonical.Column {
			t.Fatalf("round trip column %d: got line=%d col=%d", col, back.Line, back.Column)
		}
	}
	if got := CanonicalToProtocol(content, EncodingUTF16, CanonicalPos{Line: 1, Column: 6}); got.Line != 1 || got.Character != 6 {
		t.Fatalf("second line mapping = %+v", got)
	}
}

func TestCanonicalToProtocolClampsOutOfRange(t *testing.T) {
	content := []byte("abc\ndef\n")
	got := CanonicalToProtocol(content, EncodingUTF16, CanonicalPos{Line: 0, Column: 500})
	if got.Line != 0 || got.Character != 3 {
		t.Fatalf("column must clamp to the line end, got %+v", got)
	}
	got = CanonicalToProtocol(content, EncodingUTF16, CanonicalPos{Line: 9, Column: 0})
	if got.Line != 2 {
		t.Fatalf("line must clamp to the last line, got %+v", got)
	}
}

func TestOffsetAndLineHelpers(t *testing.T) {
	content := []byte("abc\r\ndef\n")
	if got := LineStartOffsets(content); len(got) != 3 || got[0] != 0 || got[1] != 5 || got[2] != 9 {
		t.Fatalf("LineStartOffsets = %v", got)
	}
	// CR is kept inside the line; only the LF separator is dropped.
	if line := LineBytes(content, 0); string(line) != "abc\r" {
		t.Fatalf("LineBytes(0) = %q, want %q", line, "abc\r")
	}
	if line := LineBytes(content, 1); string(line) != "def" {
		t.Fatalf("LineBytes(1) = %q, want %q", line, "def")
	}
	if line := LineBytes(content, 9); line != nil {
		t.Fatalf("LineBytes(out of range) = %q, want nil", line)
	}
	pos := OffsetToCanonical(content, 6) // inside "def"
	if pos.Line != 1 || pos.Column != 1 {
		t.Fatalf("OffsetToCanonical(6) = %+v", pos)
	}
	if offset := CanonicalToOffset(content, pos); offset != 6 {
		t.Fatalf("CanonicalToOffset round trip = %d, want 6", offset)
	}
}
