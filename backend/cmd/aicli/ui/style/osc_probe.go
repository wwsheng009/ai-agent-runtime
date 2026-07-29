package style

import (
	"bytes"
	"io"
	"strings"
	"time"
)

const (
	// DefaultOSCProbeTimeout bounds live OSC 10/11 queries so startup never hangs.
	DefaultOSCProbeTimeout = 50 * time.Millisecond
	maxOSCProbeRead        = 512
)

// OSC queries for default foreground (10) and background (11).
const (
	oscQueryFG = "\x1b]10;?\x07"
	oscQueryBG = "\x1b]11;?\x07"
)

// OSCProbeFunc is a bounded live probe for terminal default FG/BG colors.
// Implementations must return quickly; nil colors mean unknown / unsupported.
type OSCProbeFunc func() (fg, bg *RGB)

// OSCProbeOptions configures a stream-based OSC 10/11 query.
type OSCProbeOptions struct {
	Writer  io.Writer
	Reader  io.Reader
	Timeout time.Duration
}

type readDeadliner interface {
	SetReadDeadline(t time.Time) error
}

// ProbeOSCDefaultColors writes OSC 10/11 queries and parses replies from Reader.
//
// Safety:
//   - Requires Writer and Reader
//   - Applies SetReadDeadline when available
//   - Refuses unbounded readers (no deadline and not an immediate in-memory reader)
//     so a missing terminal reply cannot hang the process
//   - Caps total bytes read
//
// Returns nil colors when probing is skipped or replies are absent/malformed.
func ProbeOSCDefaultColors(opts OSCProbeOptions) (fg, bg *RGB) {
	if opts.Writer == nil || opts.Reader == nil {
		return nil, nil
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultOSCProbeTimeout
	}

	deadlineOK := false
	if d, ok := opts.Reader.(readDeadliner); ok {
		if err := d.SetReadDeadline(time.Now().Add(timeout)); err == nil {
			deadlineOK = true
			defer func() { _ = d.SetReadDeadline(time.Time{}) }()
		}
	}
	if !deadlineOK && !isImmediateReader(opts.Reader) {
		// Cannot bound the read; skip rather than risk a hang on stdin.
		return nil, nil
	}

	if _, err := io.WriteString(opts.Writer, oscQueryFG+oscQueryBG); err != nil {
		return nil, nil
	}

	var acc []byte
	tmp := make([]byte, 128)
	for fg == nil || bg == nil {
		if len(acc) >= maxOSCProbeRead {
			break
		}
		n, err := opts.Reader.Read(tmp)
		if n > 0 {
			acc = append(acc, tmp[:n]...)
			if len(acc) > maxOSCProbeRead {
				acc = acc[:maxOSCProbeRead]
			}
			fg, bg = parseOSCColorRepliesFrom(acc)
		}
		if err != nil {
			// EOF, deadline exceeded, or other read error ends the probe.
			break
		}
		if n == 0 {
			// Immediate readers signal exhaustion with 0,nil or 0,EOF.
			break
		}
	}
	return fg, bg
}

// MakeOSCProbe returns an OSCProbeFunc that runs ProbeOSCDefaultColors once per call.
func MakeOSCProbe(opts OSCProbeOptions) OSCProbeFunc {
	return func() (fg, bg *RGB) {
		return ProbeOSCDefaultColors(opts)
	}
}

func isImmediateReader(r io.Reader) bool {
	switch r.(type) {
	case *bytes.Reader, *bytes.Buffer, *strings.Reader:
		return true
	default:
		return false
	}
}

// parseOSCColorRepliesFrom scans a raw terminal byte stream for OSC 10/11 color replies.
func parseOSCColorRepliesFrom(buf []byte) (fg, bg *RGB) {
	s := string(buf)
	for len(s) > 0 {
		start := strings.Index(s, "\x1b]")
		if start < 0 {
			// Some decoders strip ESC; accept bare ]<ps>;rgb:... terminators.
			start = findBareOSCStart(s)
			if start < 0 {
				break
			}
		}
		chunk := s[start:]
		end := oscReplyEnd(chunk)
		if end < 0 {
			break
		}
		ps, color, ok := ParseOSCColorReply(chunk[:end])
		if ok {
			c := color
			switch ps {
			case 10:
				if fg == nil {
					fg = &c
				}
			case 11:
				if bg == nil {
					bg = &c
				}
			}
		}
		s = chunk[end:]
	}
	return fg, bg
}

func findBareOSCStart(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != ']' {
			continue
		}
		// ]<digits>;
		j := i + 1
		if j >= len(s) || s[j] < '0' || s[j] > '9' {
			continue
		}
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j < len(s) && s[j] == ';' {
			return i
		}
	}
	return -1
}

func oscReplyEnd(chunk string) int {
	bel := strings.IndexByte(chunk, '\x07')
	st := strings.Index(chunk, "\x1b\\")
	switch {
	case bel >= 0 && (st < 0 || bel <= st):
		return bel + 1
	case st >= 0:
		return st + 2
	default:
		return -1
	}
}
