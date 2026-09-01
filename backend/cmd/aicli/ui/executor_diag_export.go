package ui

import (
	"fmt"
	"strings"
)

// ExecutorDiagProvider is a callback that returns the current executor recovery
// diagnostics snapshot. It is set by the chat command (or other caller) after
// it creates the TerminalSessionExecutor, so the pprof HTTP handler can expose
// the recovery-loop ring buffer without a direct dependency on the executor's
// lifecycle.
type ExecutorDiagProvider func() ExecutorRecoveryDiag

var executorDiagProvider ExecutorDiagProvider

// SetExecutorDiagProvider registers the callback for the pprof diagnostics
// endpoint. Only one provider is active at a time; the chat command sets it
// when it attaches the terminal executor.
func SetExecutorDiagProvider(p ExecutorDiagProvider) {
	executorDiagProvider = p
}

// ExecutorDiagSnapshot returns the current executor recovery diagnostics, or
// an empty snapshot if no provider is registered.
func ExecutorDiagSnapshot() ExecutorRecoveryDiag {
	if executorDiagProvider == nil {
		return ExecutorRecoveryDiag{}
	}
	return executorDiagProvider()
}

// ExecutorDiagTextSummary renders the executor recovery diagnostics as a
// compact human-readable text block for the pprof endpoint's ?format=text
// form. It surfaces the derived verdict (Diagnosis) and the counters that
// single-read diagnostics mislead on, plus the most recent ring entry so a
// human can see exactly what the last recovery did without parsing JSON.
func ExecutorDiagTextSummary() string {
	return executorDiagTextSummary(ExecutorDiagSnapshot())
}

func executorDiagTextSummary(d ExecutorRecoveryDiag) string {
	var b strings.Builder
	b.WriteString("executor recovery diag\n")
	fmt.Fprintf(&b, "  diagnosis                : %s\n", d.Diagnosis)
	fmt.Fprintf(&b, "  totalRecoveries          : %d\n", d.TotalRecoveries)
	fmt.Fprintf(&b, "  armedBackoff             : %d\n", d.ArmedBackoff)
	fmt.Fprintf(&b, "  backoffEngaged           : %d\n", d.BackoffEngaged)
	fmt.Fprintf(&b, "  flushesWhileBackoff      : %d\n", d.FlushesWhileBackoff)
	fmt.Fprintf(&b, "  generationAdvancesInWin  : %d\n", d.GenerationAdvancesInWindow)
	fmt.Fprintf(&b, "  frameErrorsInWindow      : %d\n", d.FrameErrorsInWindow)
	fmt.Fprintf(&b, "  scrollbackResetsInWindow : %d\n", d.ScrollbackResetsInWindow)
	fmt.Fprintf(&b, "  recoveriesPerSec         : %.1f\n", d.WindowRecoveriesPerSec)
	if len(d.Entries) > 0 {
		last := d.Entries[len(d.Entries)-1]
		fmt.Fprintf(&b, "  lastSeq                  : %d\n", last.Seq)
		fmt.Fprintf(&b, "  lastBranch               : %s\n", last.Branch)
		fmt.Fprintf(&b, "  lastGeneration           : %d\n", last.Generation)
		fmt.Fprintf(&b, "  lastRevision->after      : %d -> %d\n", last.Revision, last.RevisionAfter)
		fmt.Fprintf(&b, "  lastEpoch                : %d\n", last.TerminalEpoch)
		fmt.Fprintf(&b, "  lastProjectionUnknown    : %v\n", last.ProjectionUnknown)
		fmt.Fprintf(&b, "  lastReconciliationReq    : %v\n", last.ReconciliationReq)
		fmt.Fprintf(&b, "  lastObligationPending    : %v\n", last.ObligationPending)
		fmt.Fprintf(&b, "  lastFullRepaint          : %v\n", last.FullRepaint)
		fmt.Fprintf(&b, "  lastScrollbackReset      : %v\n", last.ScrollbackReset)
		fmt.Fprintf(&b, "  lastFrameErr             : %q\n", last.FrameErr)
		fmt.Fprintf(&b, "  lastBackoffEngaged       : %v\n", last.BackoffEngaged)
		fmt.Fprintf(&b, "  lastFlushedWhileBackoff  : %v\n", last.FlushedWhileBackoff)
		fmt.Fprintf(&b, "  lastArmedBackoff         : %v\n", last.ArmedBackoff)
		fmt.Fprintf(&b, "  lastContinued            : %v\n", last.Continued)
	}
	return b.String()
}
