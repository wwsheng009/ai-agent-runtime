package style

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestProbeOSCDefaultColorsParsesReplies(t *testing.T) {
	var wrote bytes.Buffer
	replies := "\x1b]10;rgb:0000/0000/0000\x07\x1b]11;rgb:ffff/ffff/ffff\x07"
	fg, bg := ProbeOSCDefaultColors(OSCProbeOptions{
		Writer:  &wrote,
		Reader:  bytes.NewReader([]byte(replies)),
		Timeout: time.Second,
	})
	if wrote.String() != oscQueryFG+oscQueryBG {
		t.Fatalf("queries=%q", wrote.String())
	}
	if fg == nil || *fg != (RGB{0, 0, 0}) {
		t.Fatalf("fg=%v", fg)
	}
	if bg == nil || *bg != (RGB{255, 255, 255}) {
		t.Fatalf("bg=%v", bg)
	}
}

func TestProbeOSCDefaultColorsParsesSTAndNoise(t *testing.T) {
	var wrote bytes.Buffer
	// Leading keystroke noise + ST-terminated replies out of order.
	replies := "x\x1b]11;rgb:8080/8080/8080\x1b\\\x1b]10;rgb:ffff/0000/0000\x07"
	fg, bg := ProbeOSCDefaultColors(OSCProbeOptions{
		Writer:  &wrote,
		Reader:  bytes.NewReader([]byte(replies)),
		Timeout: time.Second,
	})
	if fg == nil || *fg != (RGB{255, 0, 0}) {
		t.Fatalf("fg=%v", fg)
	}
	if bg == nil || *bg != (RGB{128, 128, 128}) {
		t.Fatalf("bg=%v", bg)
	}
}

func TestProbeOSCDefaultColorsSkipsUnboundedReader(t *testing.T) {
	var wrote bytes.Buffer
	fg, bg := ProbeOSCDefaultColors(OSCProbeOptions{
		Writer:  &wrote,
		Reader:  blockingReader{},
		Timeout: 20 * time.Millisecond,
	})
	if fg != nil || bg != nil {
		t.Fatalf("unbounded reader must be skipped: fg=%v bg=%v", fg, bg)
	}
	if wrote.Len() != 0 {
		t.Fatalf("must not write queries when read cannot be bounded")
	}
}

func TestProbeOSCDefaultColorsDeadlineTimeout(t *testing.T) {
	var wrote bytes.Buffer
	r := &deadlineReader{blockUntilDeadline: true}
	start := time.Now()
	fg, bg := ProbeOSCDefaultColors(OSCProbeOptions{
		Writer:  &wrote,
		Reader:  r,
		Timeout: 25 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if fg != nil || bg != nil {
		t.Fatalf("timeout should yield nil colors: fg=%v bg=%v", fg, bg)
	}
	if wrote.String() != oscQueryFG+oscQueryBG {
		t.Fatalf("queries should still be written when deadline is available")
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("probe hung too long: %v", elapsed)
	}
}

func TestProbeOSCDefaultColorsNilIO(t *testing.T) {
	fg, bg := ProbeOSCDefaultColors(OSCProbeOptions{})
	if fg != nil || bg != nil {
		t.Fatalf("nil io should no-op")
	}
}

func TestParseOSCColorRepliesFromPartial(t *testing.T) {
	fg, bg := parseOSCColorRepliesFrom([]byte("\x1b]10;rgb:00/00/ff\x07incomplete"))
	if fg == nil || *fg != (RGB{0, 0, 255}) {
		t.Fatalf("fg=%v", fg)
	}
	if bg != nil {
		t.Fatalf("bg should be nil on partial stream, got %v", bg)
	}
}

func TestMakeOSCProbe(t *testing.T) {
	replies := "\x1b]10;rgb:11/22/33\x07\x1b]11;rgb:aa/bb/cc\x07"
	probe := MakeOSCProbe(OSCProbeOptions{
		Writer:  io.Discard,
		Reader:  bytes.NewReader([]byte(replies)),
		Timeout: time.Second,
	})
	fg, bg := probe()
	if fg == nil || bg == nil {
		t.Fatalf("fg=%v bg=%v", fg, bg)
	}
	if *fg != (RGB{0x11, 0x22, 0x33}) || *bg != (RGB{0xaa, 0xbb, 0xcc}) {
		t.Fatalf("fg=%+v bg=%+v", *fg, *bg)
	}
}

// blockingReader never supports deadlines and blocks on Read — must be skipped.
type blockingReader struct{}

func (blockingReader) Read(p []byte) (int, error) {
	select {} // should never be called
}

// deadlineReader implements SetReadDeadline and returns ErrDeadlineExceeded
// after the deadline (or immediately if already expired).
type deadlineReader struct {
	deadline            time.Time
	blockUntilDeadline  bool
	setDeadlineCalls    int
}

func (r *deadlineReader) SetReadDeadline(t time.Time) error {
	r.setDeadlineCalls++
	r.deadline = t
	return nil
}

func (r *deadlineReader) Read(p []byte) (int, error) {
	if r.deadline.IsZero() {
		return 0, errors.New("no deadline")
	}
	if r.blockUntilDeadline {
		d := time.Until(r.deadline)
		if d > 0 {
			time.Sleep(d)
		}
	}
	if !r.deadline.IsZero() && time.Now().After(r.deadline) {
		return 0, errFakeDeadline
	}
	return 0, errFakeDeadline
}

var errFakeDeadline = errors.New("deadline exceeded")

func TestDetectColorProfileLiveProbeFillsGaps(t *testing.T) {
	fg := RGB{10, 20, 30}
	bg := RGB{200, 210, 220}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(string) string { return "" },
		OSCProbe: func() (*RGB, *RGB) {
			f, b := fg, bg
			return &f, &b
		},
	})
	if p.DefaultFG == nil || *p.DefaultFG != fg {
		t.Fatalf("DefaultFG=%v", p.DefaultFG)
	}
	if p.DefaultBG == nil || *p.DefaultBG != bg {
		t.Fatalf("DefaultBG=%v", p.DefaultBG)
	}
	if p.Background != BackgroundLight {
		t.Fatalf("bg kind=%v want light", p.Background)
	}
}

func TestDetectColorProfileDisableSkipsLiveProbe(t *testing.T) {
	called := false
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ: func(k string) string {
			if k == "AICLI_DISABLE_OSC_PROBE" {
				return "1"
			}
			return ""
		},
		OSCProbe: func() (*RGB, *RGB) {
			called = true
			fg, bg := RGB{1, 1, 1}, RGB{2, 2, 2}
			return &fg, &bg
		},
	})
	if called {
		t.Fatal("live probe must not run when AICLI_DISABLE_OSC_PROBE is set")
	}
	if p.DefaultFG != nil || p.DefaultBG != nil {
		t.Fatalf("disabled probe must leave defaults empty: fg=%v bg=%v", p.DefaultFG, p.DefaultBG)
	}
}

func TestDetectColorProfileLiveProbeDoesNotOverrideEnv(t *testing.T) {
	env := map[string]string{
		"AICLI_OSC_FG": "rgb:0000/0000/0000",
		"AICLI_OSC_BG": "rgb:ffff/ffff/ffff",
	}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(k string) string { return env[k] },
		OSCProbe: func() (*RGB, *RGB) {
			fg, bg := RGB{9, 9, 9}, RGB{8, 8, 8}
			return &fg, &bg
		},
	})
	if p.DefaultFG == nil || *p.DefaultFG != (RGB{0, 0, 0}) {
		t.Fatalf("env fg should win: %v", p.DefaultFG)
	}
	if p.DefaultBG == nil || *p.DefaultBG != (RGB{255, 255, 255}) {
		t.Fatalf("env bg should win: %v", p.DefaultBG)
	}
}

func TestDetectColorProfileLiveProbeFillsOnlyMissing(t *testing.T) {
	env := map[string]string{
		"AICLI_OSC_FG": "rgb:0101/0101/0101",
	}
	called := false
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(k string) string { return env[k] },
		OSCProbe: func() (*RGB, *RGB) {
			called = true
			bg := RGB{50, 50, 50}
			return nil, &bg
		},
	})
	if !called {
		t.Fatal("probe should run to fill missing bg")
	}
	if p.DefaultFG == nil || *p.DefaultFG != (RGB{1, 1, 1}) {
		t.Fatalf("fg=%v", p.DefaultFG)
	}
	if p.DefaultBG == nil || *p.DefaultBG != (RGB{50, 50, 50}) {
		t.Fatalf("bg=%v", p.DefaultBG)
	}
	if p.Background != BackgroundDark {
		t.Fatalf("bg kind=%v want dark", p.Background)
	}
}

func TestDetectColorProfileInjectableBeatsLiveProbe(t *testing.T) {
	fg := RGB{3, 3, 3}
	bg := RGB{4, 4, 4}
	called := false
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		DefaultFG:   &fg,
		DefaultBG:   &bg,
		Environ:     func(string) string { return "" },
		OSCProbe: func() (*RGB, *RGB) {
			called = true
			a, b := RGB{9, 9, 9}, RGB{8, 8, 8}
			return &a, &b
		},
	})
	if called {
		t.Fatal("probe should not run when both injectable colors are set")
	}
	if p.DefaultFG == nil || *p.DefaultFG != fg || p.DefaultBG == nil || *p.DefaultBG != bg {
		t.Fatalf("injectable should win: fg=%v bg=%v", p.DefaultFG, p.DefaultBG)
	}
}
