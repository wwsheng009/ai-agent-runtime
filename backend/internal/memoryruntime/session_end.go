package memoryruntime

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultSessionEndExtractionTimeout    = 2 * time.Minute
	defaultSessionEndConsolidationTimeout = 2 * time.Minute
)

// SessionEndPipelineOptions separates the extraction/rollup and consolidation
// budgets. Each phase is durably resumable; a timeout never acknowledges an
// uncovered digest prefix.
type SessionEndPipelineOptions struct {
	ExtractionTimeout    time.Duration
	ConsolidationTimeout time.Duration
	Consolidate          bool
}

type SessionEndPipelineResult struct {
	Run           RunResult
	Consolidation ConsolidationResult
	Stage         string
}

func (o SessionEndPipelineOptions) normalized() SessionEndPipelineOptions {
	if o.ExtractionTimeout <= 0 {
		o.ExtractionTimeout = defaultSessionEndExtractionTimeout
	}
	if o.ConsolidationTimeout <= 0 {
		o.ConsolidationTimeout = defaultSessionEndConsolidationTimeout
	}
	return o
}

// RunSessionEndPipeline runs bounded phases independently. The caller should
// pass a lifecycle context (rather than a single short total deadline) so a
// slow extraction does not consume the consolidation budget.
func (r *Runtime) RunSessionEndPipeline(ctx context.Context, sessionID string, options SessionEndPipelineOptions) (SessionEndPipelineResult, error) {
	var result SessionEndPipelineResult
	if r == nil {
		return result, fmt.Errorf("memoryruntime: session-end runtime is nil")
	}
	options = options.normalized()

	extractionCtx, extractionCancel := context.WithTimeout(ctx, options.ExtractionTimeout)
	run, err := r.RunSessionEnd(extractionCtx, sessionID)
	extractionCancel()
	result.Run = run
	if err != nil {
		result.Stage = "extraction"
		return result, fmt.Errorf("memoryruntime: session-end extraction phase failed: %w", err)
	}
	if run.Failed > 0 || run.RollupFailed > 0 {
		result.Stage = "extraction"
		return result, fmt.Errorf("memoryruntime: session-end extraction phase left durable work pending")
	}
	if !options.Consolidate || r.Config.ConsolidationDisabled || r.Store == nil {
		result.Stage = "extraction"
		return result, nil
	}

	consolidationCtx, consolidationCancel := context.WithTimeout(ctx, options.ConsolidationTimeout)
	consolidated, err := NewConsolidator(r.Store, r.Config).Run(consolidationCtx)
	consolidationCancel()
	result.Consolidation = consolidated
	if err != nil {
		result.Stage = "consolidation"
		return result, fmt.Errorf("memoryruntime: session-end consolidation phase failed: %w", err)
	}
	result.Stage = "complete"
	return result, nil
}
