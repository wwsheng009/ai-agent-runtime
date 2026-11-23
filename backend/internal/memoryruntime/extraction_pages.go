package memoryruntime

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// planExtractionPages bounds the complete rendered user prompt, including
// framing. Redact before splitting so a secret spanning a boundary cannot leak.
// Whole messages are packed greedily; oversized messages use dedicated UTF-8
// fragments carrying the original canonical ID and sequence.
func planExtractionPages(session SessionRef, transcript Transcript, cfg Config) ([]Transcript, error) {
	budget := extractionByteLimit(cfg)
	headerSize := len(extractionPromptHeader(session))
	if headerSize >= budget {
		return nil, fmt.Errorf("memoryruntime: source byte budget cannot fit extraction header")
	}
	var pages []Transcript
	var messages []TranscriptMessage
	used := headerSize
	flush := func() {
		if len(messages) == 0 {
			return
		}
		page := transcript
		page.Messages = messages
		page.Hash = TranscriptHash(messages)
		pages = append(pages, page)
		messages = nil
		used = headerSize
	}
	for _, original := range transcript.Messages {
		message := original
		message.Content, _ = RedactForPrompt(original.Content)
		if !utf8.ValidString(message.Content) {
			return nil, fmt.Errorf("memoryruntime: canonical message is not valid UTF-8")
		}
		entrySize := len(extractionPromptEntry(message))
		if entrySize <= budget-headerSize {
			if entrySize > budget-used {
				flush()
			}
			messages = append(messages, message)
			used += entrySize
			continue
		}
		flush()
		text := message.Content
		message.FragmentTotal = len(text)
		for offset := 0; offset < len(text); {
			message.FragmentOffset = offset
			message.Content = ""
			capacity := budget - headerSize - len(extractionPromptEntry(message))
			if capacity <= 0 {
				return nil, fmt.Errorf("memoryruntime: source byte budget cannot fit message fragment framing")
			}
			part := truncateUTF8Bytes(text[offset:], capacity)
			if part == "" {
				return nil, fmt.Errorf("memoryruntime: source byte budget cannot fit a UTF-8 rune")
			}
			message.Content = part
			messages = []TranscriptMessage{message}
			flush()
			offset += len(part)
		}
		if len(text) == 0 {
			return nil, fmt.Errorf("memoryruntime: source byte budget cannot fit message framing")
		}
	}
	flush()
	return pages, nil
}

// Byte pages have independent durable jobs, but not independent canonical
// receipts: all fragments must succeed before the enclosing range is complete.
// Use the enclosing source hash/window for candidates, so pages never stale
// each other's records and backtrack invalidation keeps canonical provenance.
func (r *Runtime) runExtractionPages(ctx context.Context, trigger Trigger, session SessionRef, source Transcript, pages []Transcript, sourceWindow string) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	for index, page := range pages {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		key := fmt.Sprintf("session:%s:byte-pages-v1:%s:%s:%d:%s",
			strings.TrimSpace(session.ID), sourceWindow, source.Hash, index, page.Hash)
		part, err := r.runExtractionPage(ctx, trigger, session, source, page, sourceWindow, key, index)
		mergeRunResult(&result, part)
		if err != nil || !part.Completed {
			return result, err
		}
	}
	completeTranscriptResult(&result, source)
	return result, nil
}

func (r *Runtime) runExtractionPage(ctx context.Context, trigger Trigger, session SessionRef, source, page Transcript, sourceWindow, key string, index int) (RunResult, error) {
	result := RunResult{Trigger: trigger}
	job, err := r.Store.EnsureJob(ctx, memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: key, ProjectKey: r.Config.ProjectKey, MaxAttempts: r.Config.MaxAttempts,
	})
	if err != nil {
		return result, err
	}
	if job.Status == memorystore.JobStatusSucceeded {
		if job.LastSuccessWatermark != page.Hash {
			return result, fmt.Errorf("memoryruntime: byte page has no matching success watermark")
		}
		result.Completed, result.Skipped = true, 1
		return result, nil
	}
	if job.Status == memorystore.JobStatusFailed ||
		job.Status == memorystore.JobStatusRunning && job.LeaseUntil.After(r.now()) {
		result.Skipped = 1
		return result, nil
	}
	claimed, err := r.Store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
		WorkerID: r.workerID(), Kind: "phase1_extract", JobKey: key,
		ProjectKey: r.Config.ProjectKey, Limit: 1, Now: r.now(), Lease: r.Config.Lease,
	})
	if err != nil || len(claimed) == 0 {
		result.Skipped = 1
		return result, err
	}
	fail := func(cause error) (RunResult, error) {
		return result, r.failExtractionJob(ctx, claimed[0], cause)
	}
	var extraction ExtractionResult
	err = withJobLeaseHeartbeat(ctx, r.Store, r.Config.Lease, claimed[0],
		func(heartbeatCtx context.Context) error {
			var extractErr error
			extraction, extractErr = r.Extractor.Extract(heartbeatCtx, session, page)
			return extractErr
		})
	if err != nil {
		return fail(err)
	}
	commitCtx, commitCancel := memoryJobCommitContext(ctx)
	defer commitCancel()
	candidates, reasons := r.validateCandidates(extraction, page, r.now())
	result.Skipped += len(reasons)
	result.Ignored = append(result.Ignored, reasons...)
	if err := r.Store.RecordExtractionDiagnostics(commitCtx, extractionDiagnostics(
		claimed[0].ID, r.Config.ProjectKey, session, page, sourceWindow, extraction, reasons, r.now(),
	)); err != nil {
		return fail(err)
	}
	if sourceWindow == "" {
		result.Stale, err = r.Store.MarkSessionSourceChanged(commitCtx, session.ID, r.Config.ProjectKey, source.Hash, source.Revision)
	} else {
		result.Stale, err = r.Store.MarkWindowSourceChanged(commitCtx, session.ID, r.Config.ProjectKey, sourceWindow, source.Hash, source.Revision)
	}
	if err != nil {
		return fail(err)
	}
	for _, candidate := range candidates {
		candidate.SourceHash, candidate.SourceRevision = source.Hash, source.Revision
		candidate.SourceWindow = sourceWindow
		candidate.Metadata = map[string]interface{}{"extraction_page": index, "extraction_page_hash": page.Hash}
		if _, err := r.Store.InsertCandidate(commitCtx, candidate); err != nil {
			if isCandidateSkipError(err) {
				result.Skipped++
				continue
			}
			return fail(err)
		}
		result.Candidates++
	}
	if err := r.Store.CompleteJobForWorker(commitCtx, claimed[0].ID, claimed[0].WorkerID, page.Hash); err != nil {
		return fail(err)
	}
	result.Completed, result.Processed = true, 1
	return result, nil
}
