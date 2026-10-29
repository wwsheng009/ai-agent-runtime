package envdclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// ReadRange fetches bytes [off, off+size) of one file.
//
// This is the offset read envd already serves - `/files` answers `Range` with 206
// and a Content-Range, measured byte for byte against `head -c`, `tail -c` and
// `dd skip=` - which the client simply never exposed: ReadFile could only ask for a
// prefix. Exposing it is what makes a random-access consumer (a FUSE READ, a
// paged log viewer) cost the window instead of the whole file.
//
// Two rules separate it from ReadFile, and both are about not being clever at the
// caller's expense:
//
//   - A 416 is returned as FilesRangeUnsatisfiable, never retried as a whole-body
//     read. ReadFile may retry because a prefix request that runs past the end is
//     almost always "the whole file, then"; for a mid-file window that "help"
//     would pull an arbitrarily large file into memory to answer a small read.
//     The error carries Total when the reply disclosed it, which is also how a
//     caller resolves a negative offset after the file changed underneath.
//   - size <= 0 means "to the end of the file", and Partial is always relative to
//     the end of *this* window (off+len(Data) < Total), not to its length. Getting
//     that backwards is silent: a middle window would claim it was the end.
//
// Negative off reads from the end (FUSE tail reads ask for this). It resolves
// against the file size with one Stat, because "the last N bytes" is meaningless
// without knowing where the end is.
func (c *Client) ReadRange(ctx context.Context, path string, off, size int64) (*FileRead, error) {
	if off < 0 {
		entry, err := c.Stat(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("envdclient: resolve tail offset for %s: %w", path, err)
		}
		total := entry.SizeBytes()
		if total < 0 {
			return nil, fmt.Errorf("envdclient: cannot resolve a tail offset for %s: no usable size", path)
		}
		off = total + off
		if off < 0 {
			// Fewer bytes exist than were asked for: the file *is* the tail.
			off = 0
		}
	}

	read, err := c.fetch(ctx, path, &byteRange{off: off, size: size})
	if err != nil {
		return nil, err
	}
	if off > 0 && !read.Partial && read.Total >= 0 && off+int64(len(read.Data)) < read.Total {
		// Unreachable by construction; kept because a wrong Partial here is a
		// silent data-loss signal for a paging caller, not a loud one.
		return nil, fmt.Errorf("envdclient: internal inconsistency reading %s at %d: total %d, got %d bytes marked complete",
			path, off, read.Total, len(read.Data))
	}
	return read, nil
}

// Capabilities is what this envd deployment can actually do with file bytes.
//
// Every field is measured, never inferred from a version string: 0.2.11 already
// proved that guessing from source is wrong (its POST rejects a raw body the
// current upstream source accepts, and it ignores gzip that upstream implements).
// A mount or a paged reader decides *before* it starts serving from these, so an
// unsupported operation fails at setup instead of as a thousand runtime errors.
type Capabilities struct {
	// RangeRead is the offset read: 206 + Content-Range on a probe of a real file.
	RangeRead bool
	// RangeWrite would be a partial/in-place write. It stays false unless a probe
	// proves otherwise, because /files POST is documented-and-measured as O_TRUNC
	// of the whole file: claiming it would be a promise the target cannot keep.
	RangeWrite bool
	// ProcessChannel reports whether the process RPC answers, i.e. whether a
	// spawned-command fallback exists at all.
	ProcessChannel bool
	// HasDD reports whether the target has `dd`, which the process-channel write
	// strategy depends on. Probed only when asked for.
	HasDD bool

	// LineWindow reports that the target can produce a LINE window - a slice of a
	// file by line number - together with the file's line count, in one command,
	// with byte-exact fidelity. That combination is what makes it safe for a remote
	// surface to hand the view core a window instead of a growing prefix: the count
	// is what lets the core derive `eof` rather than being told it.
	//
	// Measured, never inferred from a version or from "awk exists": an awk that
	// cannot write /dev/stderr, one that rewrites NUL bytes, and one that counts an
	// unterminated final line differently from the core all fail this probe, and all
	// of them exist in the wild (mawk, busybox awk). False means "not measured or
	// measured absent", which is the only acceptable answer for a read-only caller.
	LineWindow bool
	// MaxReadBytes is the largest window this channel will hand back in one
	// request, or 0 when the deployment reports no such limit. It is measured with
	// a real read, not read from a header: envd advertises none.
	MaxReadBytes int64
}

// ProbeCapabilities measures the file-byte capabilities of this deployment.
//
// samplePath must name a readable existing file: a range read cannot be proven on
// a missing path, and inventing one (write a probe file) would leave a side effect
// on a target that may be read-only. Pass "" to skip the read probe; RangeRead is
// then reported false, because "not measured" and "measured absent" must not share
// a value with "measured present".
//
// withProcess asks the target to run two trivial commands, so it is opt-in: the
// answer decides whether a command-spawning write path is even available, and a
// read-only caller should not spawn processes to find out.
func (c *Client) ProbeCapabilities(ctx context.Context, samplePath string, withProcess bool) (Capabilities, error) {
	if c == nil {
		return Capabilities{}, errors.New("envdclient: no client")
	}
	caps := Capabilities{}

	if samplePath != "" {
		// Ask for one byte at offset zero, then one byte further in: the first
		// proves the endpoint answers, the second proves the offset is honoured
		// rather than the whole body arriving with a decorative header.
		head, err := c.ReadRange(ctx, samplePath, 0, 1)
		if err != nil {
			if IsFilesChannelIssue(err) {
				return caps, err
			}
			return caps, fmt.Errorf("envdclient: probe range read on %s: %w", samplePath, err)
		}
		caps.RangeRead = head.StatusCode == 206 || head.StatusCode == 200
		if head.Total > 1 {
			second, secondErr := c.ReadRange(ctx, samplePath, 1, 1)
			if secondErr != nil {
				return caps, fmt.Errorf("envdclient: probe offset read on %s: %w", samplePath, secondErr)
			}
			// A mid-file window that came back from byte 0 is not a range read.
			caps.RangeRead = caps.RangeRead && second.StatusCode == 206
		}
		caps.MaxReadBytes = int64(len(head.Data))
	}

	if withProcess {
		caps.ProcessChannel = true
		if _, _, _, err := c.probeCommand(ctx, "/bin/sh", []string{"-c", "exit 0", "probe"}); err != nil {
			caps.ProcessChannel = false
		} else {
			_, _, code, err := c.probeCommand(ctx, "/bin/sh", []string{"-c", "command -v dd >/dev/null 2>&1", "probe"})
			caps.HasDD = err == nil && code == 0
			// A line window is a command-level capability, so it can only be measured
			// on a target that answered the channel probe at all. Ordering matters for
			// the cost, not the truth: one failing spawn is enough to know there is no
			// command path here, and paying three more on a target that has none is
			// how a capability probe becomes the slowest part of a call.
			if caps.ProcessChannel {
				lineWindow, _, _, _, probeErr := c.ProbeLineWindow(ctx)
				caps.LineWindow = probeErr == nil && lineWindow
			}
		}
	}
	return caps, nil
}

// probeCommand runs one short-lived command and reports its exit status. Unlike
// Run it never carries payload, so the buffering question does not arise.
func (c *Client) probeCommand(ctx context.Context, cmd string, args []string) (stdout []byte, stderr []byte, code int32, err error) {
	return c.Run(ctx, StartRequest{Process: ProcessConfig{Cmd: cmd, Args: args}})
}

const (
	// LineWindowProbeScript builds its own two-record sample on awk's stdin - one
	// record containing a NUL byte, and a final line with no newline - then asks the
	// same program shape the real read uses: the window on stdout, the total count on
	// /dev/stderr. It opens no file and writes nothing, so a probe leaves no trace on
	// a target that may be read-only.
	//
	// Exported, together with the two expectations below, so the live verification
	// driver measures the identical thing instead of carrying a copy that can drift
	// away from what the client actually asks for.
	LineWindowProbeScript = `printf 'a\000b\nlast-unterminated' | awk -v a=1 -v b=2 'NR>=a && NR<=b {print} END {print "LINECOUNT=" NR > "/dev/stderr"}'`

	// LineWindowProbeStdout is the byte-exact answer. It requires three things at
	// once: NUL survives the process channel and awk's record handling unchanged, the
	// unterminated final line is delivered (so the count rule can be checked against
	// it), and awk's `print` adds the newline the core then strips - the one
	// difference a window has from the raw file, proven harmless at byte level.
	LineWindowProbeStdout = "a\x00b\nlast-unterminated\n"

	// LineWindowProbeStderr is the side-channel marker. An awk that cannot write
	// /dev/stderr - the common mawk and busybox case - fails here while still
	// producing a plausible window, which is precisely the trap: a window whose total
	// is unknown must never be allowed to speak about the end of the file.
	LineWindowProbeStderr = "LINECOUNT=2"
)

// ProbeLineWindow measures whether this target can serve a byte-exact line window
// with its line count. It returns the verdict plus the raw evidence, because "the
// probe said no" is not actionable for an operator: the actual stdout and stderr are
// what show whether awk is missing, whether NUL was rewritten, or whether the count
// channel is absent.
func (c *Client) ProbeLineWindow(ctx context.Context) (supported bool, stdout, stderr []byte, code int32, err error) {
	if c == nil {
		return false, nil, nil, -1, errors.New("envdclient: no client")
	}
	stdout, stderr, code, err = c.probeCommand(ctx, "/bin/sh", []string{"-c", LineWindowProbeScript})
	if err != nil {
		return false, stdout, stderr, code, err
	}
	return lineWindowReport(stdout, stderr, code), stdout, stderr, code, nil
}

// lineWindowReport is the acceptance predicate, pure so every way it can refuse has
// a test. All three conditions are load-bearing and each catches a different real
// deployment: a missing awk (exit code), a non-GNU awk that rewrites NUL or counts
// lines differently (stdout bytes), and an awk without /dev/stderr (stderr marker).
func lineWindowReport(stdout, stderr []byte, code int32) bool {
	if code != 0 {
		return false
	}
	if !bytes.Equal(stdout, []byte(LineWindowProbeStdout)) {
		return false
	}
	return bytes.Contains(stderr, []byte(LineWindowProbeStderr))
}
