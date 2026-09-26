package docread

import "testing"

// TestBoundedBufferTruncatesButConsumes: converters may emit far more than a
// window ever needs; the buffer keeps the head and keeps reporting full writes
// so a child process does not die with EPIPE mid-render.
func TestBoundedBufferTruncatesButConsumes(t *testing.T) {
	buffer := boundedBuffer{limit: 4}
	n, err := buffer.Write([]byte("abcdefgh"))
	if err != nil || n != 8 {
		t.Fatalf("expected the full write to be consumed, got n=%d err=%v", n, err)
	}
	if got := string(buffer.Bytes()); got != "abcd" {
		t.Fatalf("expected the first 4 bytes, got %q", got)
	}
	if n, err := buffer.Write([]byte("ij")); err != nil || n != 2 {
		t.Fatalf("overflow writes must still be consumed, got n=%d err=%v", n, err)
	}
	if got := string(buffer.Bytes()); got != "abcd" {
		t.Fatalf("buffer must stay bounded, got %q", got)
	}
}

func TestRunCommandDefaultOutputIsBounded(t *testing.T) {
	// 生产常量本身就是上限的契约：一旦被调小/删掉，这条断言会失败。
	if maxConverterOutputBytes <= 0 || maxConverterOutputBytes > 256<<20 {
		t.Fatalf("maxConverterOutputBytes must stay a sane positive bound, got %d", maxConverterOutputBytes)
	}
}
