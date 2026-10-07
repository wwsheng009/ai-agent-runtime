package ui

import "errors"

// terminalWriteClass classifies one physical terminal transaction by the
// strongest fact the write path can prove about the host state. It replaces
// inference ("cursor held => maybe written", "frame error => zero bytes") with
// a single recorded fact per attempt (A1-2 proof record).
type terminalWriteClass uint8

const (
	// terminalWriteNotAttempted: no payload was assembled or no writer was
	// consulted; the host received no bytes of this transaction.
	terminalWriteNotAttempted terminalWriteClass = iota
	// terminalWriteCommitted: the full payload is proven flushed.
	terminalWriteCommitted
	// terminalWriteFailedZero: an attempt ran and the host provably received
	// zero bytes; the same token may be retried safely.
	terminalWriteFailedZero
	// terminalWritePartial: some bytes reached the host (0 < n < len, or a
	// panic interrupted the writer); coverage is unknown.
	terminalWritePartial
	// terminalWriteAbandoned: an already-dispatched syscall was abandoned by
	// shutdown abort. It may still complete later, so the host state is
	// unknown and must be treated as possibly-partial (fail-closed).
	terminalWriteAbandoned
)

// terminalWriteProof is the immutable fact record of one FlushTransaction
// attempt at the session boundary. The executor consumes it to classify the
// claimed history token instead of re-deriving facts from frame/history error
// shapes.
type terminalWriteProof struct {
	Outcome      terminalWriteClass
	FlushedBytes int
	TotalBytes   int
	Err          error
}

// zeroWriteProven reports whether the attempt provably emitted no host bytes,
// which is the only condition under which the same history token may be
// retried in place.
func (p *terminalWriteProof) zeroWriteProven() bool {
	if p == nil {
		return false
	}
	return p.Outcome == terminalWriteFailedZero || p.Outcome == terminalWriteNotAttempted
}

// possiblyWritten reports whether host coverage is unknown or known-partial.
func (p *terminalWriteProof) possiblyWritten() bool {
	if p == nil {
		return false
	}
	return p.Outcome == terminalWritePartial || p.Outcome == terminalWriteAbandoned
}

// classifyTerminalWriteOutcome derives the proof outcome from a direct-writer
// result: the error shape plus the bytes the probe counted. Abandoned aborts
// are detected by the sentinel the abortable writer returns for in-flight
// cancellations.
func classifyTerminalWriteClass(err error, flushedBytes int, mayHavePartiallyWritten bool) terminalWriteClass {
	switch {
	case err == nil && flushedBytes > 0:
		return terminalWriteCommitted
	case err == nil:
		return terminalWriteNotAttempted
	case errors.Is(err, errTerminalWriteAbandoned):
		return terminalWriteAbandoned
	case mayHavePartiallyWritten:
		return terminalWritePartial
	default:
		return terminalWriteFailedZero
	}
}
