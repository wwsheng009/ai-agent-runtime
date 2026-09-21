package output

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// ArtifactWriter 约束 gateway 所需的最小 artifact 能力。
type ArtifactWriter interface {
	Put(ctx context.Context, record artifact.Record) (string, error)
}

// RawToolResult 表示工具执行后的原始输出。
type RawToolResult struct {
	SessionID  string
	ToolName   string
	ToolCallID string
	Step       int
	Content    interface{}
	Error      string
	Metadata   map[string]interface{}
	// Args is the raw tool-call argument map. When empty/partial/failed, the
	// gateway compact-injects attempted_args for model recovery contracts.
	Args map[string]interface{}
}

// artifactSourceIDMetadataKey marks a tool result whose content is a window
// into an existing artifact record (produced by artifact_read). Re-archiving
// it would create a pointer whose target itself carries a pointer, growing a
// recursive pointer cascade on every dereference hop.
const artifactSourceIDMetadataKey = "artifact_source_id"

// Envelope 是允许进入上下文窗口的压缩信号。
type Envelope struct {
	ToolName    string
	ToolCallID  string
	OK          bool
	ErrorCode   string
	Retryable   bool
	NextAction  string
	Summary     string
	Error       string
	ArtifactIDs []string
	Metadata    map[string]interface{}
}

// Reducer 负责把原始输出压成可控的 envelope。
type Reducer interface {
	Name() string
	Reduce(ctx context.Context, input ReducedInput) (*Envelope, bool, error)
}

// ReducedInput 是 reducer 的标准输入。
type ReducedInput struct {
	Raw       RawToolResult
	Text      string
	StoredAt  time.Time
	Artifact  string
	ByteCount int
}

// Gateway 在 tool output 与上下文窗口之间插入治理层。
type Gateway struct {
	store    ArtifactWriter
	reducers []Reducer
}

const cacheSafeSummaryMetadataKey = "cache_safe_summary"

// NewGateway 创建 output gateway。
func NewGateway(store ArtifactWriter, reducers ...Reducer) *Gateway {
	normalized := make([]Reducer, 0, len(reducers))
	for _, reducer := range reducers {
		if reducer != nil {
			normalized = append(normalized, reducer)
		}
	}
	if len(normalized) == 0 {
		normalized = append(normalized,
			&GoTestJSONReducer{},
			&GoTestTextReducer{},
			&GitLogReducer{},
			&PlaywrightSnapshotReducer{},
			&JSONReducer{},
			&TableReducer{},
			&LogReducer{},
			NewTextReducer(1200, 16),
		)
	}

	return &Gateway{
		store:    store,
		reducers: normalized,
	}
}

// Process 将工具原始输出归档并压缩成可注入上下文的 envelope。
func (g *Gateway) Process(ctx context.Context, result RawToolResult) (*Envelope, error) {
	envelope := &Envelope{
		ToolName:   result.ToolName,
		ToolCallID: result.ToolCallID,
		Error:      strings.TrimSpace(result.Error),
		Metadata:   cloneMap(result.Metadata),
	}
	if envelope.Metadata == nil {
		envelope.Metadata = map[string]interface{}{}
	}
	if kind := toolresult.KindFromMetadata(envelope.Metadata); kind != "" {
		envelope.Metadata[toolresult.MetadataKey] = kind
	}
	if source := toolresult.SourceFromMetadata(envelope.Metadata); source != "" {
		envelope.Metadata[toolresult.SourceKey] = source
	}

	text := stringify(result.Content)
	input := ReducedInput{
		Raw:       result,
		Text:      text,
		StoredAt:  time.Now().UTC(),
		ByteCount: len([]byte(text)),
	}
	envelope.Metadata["raw_bytes"] = input.ByteCount
	envelope.Metadata["byte_count"] = input.ByteCount
	envelope.Metadata["sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))

	// O-1 baseline: raw tool output size at the gateway entrance.
	observability.RecordToolOutputBytes(observability.MetricToolOutputOriginalBytes, observability.TruncationLayerRender, input.ByteCount)

	var processErrs []string
	// §6.1 contract: failed results keep the raw-output pointer (recovery
	// hint), so the P1-1 below-threshold skip only applies to successes.
	failedResult := strings.TrimSpace(result.Error) != ""
	belowArchiveThreshold := input.ByteCount < artifactArchiveMinBytes(result.Metadata)
	if g.store != nil && strings.TrimSpace(text) != "" && !isArtifactReadWindow(result.Metadata) {
		if !failedResult && belowArchiveThreshold {
			// P1-1 archive tiering: tiny results (model already sees the full
			// body, so a pointer is never consumed and dereferenced) skip the
			// store to reduce storage/search-index noise. Search serves large
			// output keyword recovery; small results stay visible in history.
			envelope.Metadata["artifact_skipped"] = "below_threshold"
			observability.RecordToolOutputArchive(observability.ArchiveLayerGateway, observability.ArchiveDispositionSkippedBelowThreshold)
		} else {
			artifactID, err := g.store.Put(ctx, artifact.Record{
				SessionID:  result.SessionID,
				ToolName:   result.ToolName,
				ToolCallID: result.ToolCallID,
				Summary:    preview(text, 400),
				Content:    text,
				Metadata:   cloneMap(result.Metadata),
				CreatedAt:  input.StoredAt,
			})
			if err != nil {
				processErrs = append(processErrs, err.Error())
			} else {
				input.Artifact = artifactID
				envelope.ArtifactIDs = append(envelope.ArtifactIDs, artifactID)
				envelope.Metadata["artifact_id"] = artifactID
				observability.RecordToolOutputArchive(observability.ArchiveLayerGateway, observability.ArchiveDispositionArchived)
			}
		}
	} else if g.store != nil && strings.TrimSpace(text) == "" {
		observability.RecordToolOutputArchive(observability.ArchiveLayerGateway, observability.ArchiveDispositionSkippedEmpty)
	} else if isArtifactReadWindow(result.Metadata) {
		// Dereferenced windows must not re-archive (no-cascade guard); count
		// them so the dereference chain stays visible in the archive mix.
		observability.RecordToolOutputArchive(observability.ArchiveLayerGateway, observability.ArchiveDispositionSkippedReadWindow)
	}

	var handled bool
	for _, reducer := range g.reducers {
		reduced, ok, err := reducer.Reduce(ctx, input)
		if err != nil {
			processErrs = append(processErrs, fmt.Sprintf("%s: %v", reducer.Name(), err))
			continue
		}
		if !ok || reduced == nil {
			continue
		}

		handled = true
		reducerName := reducer.Name()
		envelope = mergeEnvelope(envelope, reduced)
		envelope.Metadata["reducer"] = reducerName
		if input.ByteCount > modelToolTextByteBudget && prefersModelSummaryForLargeText(reducerName) {
			envelope.Metadata["model_summary_preferred"] = true
		}
		break
	}

	if !handled {
		fallback, _, _ := NewTextReducer(1200, 16).Reduce(ctx, input)
		envelope = mergeEnvelope(envelope, fallback)
		envelope.Metadata["reducer"] = "text_truncation"
	}

	if override := cacheSafeSummaryOverride(result.Metadata); override != "" {
		envelope.Summary = override
		envelope.Metadata["summary_override"] = true
	}

	// Promote empty successful invocations so model contracts can distinguish
	// "no matches / no output" from hard failures without tool-specific logic.
	// Mutation successes with empty body still have actionable proof in metadata
	// (mutated_paths / idempotent_replay) and must not be labeled empty.
	// Also honor source/metadata evidence (match_count==0, returned_count==0,
	// explicit empty_result) even when the body carries a short "no matches"
	// message — that is still empty success, not failure.
	if strings.TrimSpace(result.Error) == "" &&
		strings.TrimSpace(toolresult.MutationSummary(envelope.Metadata)) == "" {
		bodyEmpty := strings.TrimSpace(text) == ""
		if bodyEmpty || toolresult.HasEmptySuccessEvidence(envelope.Metadata) {
			toolresult.MarkEmptySuccess(envelope.Metadata)
		}
	}
	// Multi-item partial failures: promote outcome + next_action from generic
	// batch stats (requested/failed/succeeded aliases). Applies even when the
	// tool returned Success with mixed item results, so the model reuses
	// successful items instead of replaying the whole batch unchanged.
	if stats := toolresult.ExtractBatchStats(envelope.Metadata); stats.Failed > 0 &&
		(stats.Succeeded > 0 || stats.Requested > stats.Failed) {
		envelope.Metadata[toolresult.MetadataOutcomeKey] = toolresult.OutcomePartial
		envelope.Metadata[toolresult.MetadataPartialFailureKey] = true
		if stats.Requested > 0 {
			envelope.Metadata[toolresult.MetadataRequestedCountKey] = stats.Requested
		}
		if stats.Failed > 0 {
			envelope.Metadata[toolresult.MetadataFailedCountKey] = stats.Failed
		}
		if stats.Succeeded > 0 {
			envelope.Metadata[toolresult.MetadataSucceededCountKey] = stats.Succeeded
		}
		// Promote compact failed-item rows early so Diagnose can enrich
		// next_action without tool-name branches.
		if items := toolresult.ExtractFailedItems(envelope.Metadata); len(items) > 0 {
			// Store typed rows; ApplyDiagnosticMetadata re-emits map form.
			envelope.Metadata[toolresult.MetadataFailedItemsKey] = items
		}
		if next := strings.TrimSpace(fmt.Sprint(envelope.Metadata[toolresult.MetadataNextActionKey])); next == "" || next == "<nil>" {
			envelope.Metadata[toolresult.MetadataNextActionKey] = toolresult.NextActionForPartialBatch(
				stats.Failed,
				stats.Requested,
				toolresult.ExtractFailedItems(envelope.Metadata),
			)
		}
	}

	// Inject/promote attempted_args only for recovery-relevant dispositions so
	// ordinary success stays free of arg dumps unless preflight already attached
	// them. Prefer nested tool_invocation summaries over re-compacting Args.
	if shouldAttachAttemptedArgs(result.Error, envelope.Metadata) {
		toolresult.EnsureAttemptedArgs(envelope.Metadata, result.Args)
	}

	diagnostic := toolresult.Diagnose(result.ToolName, result.ToolCallID, result.Error, envelope.Metadata)
	envelope.OK = diagnostic.OK
	envelope.ErrorCode = diagnostic.ErrorCode
	envelope.Retryable = diagnostic.Retryable
	envelope.NextAction = diagnostic.NextAction
	toolresult.ApplyDiagnosticMetadata(envelope.Metadata, diagnostic)
	// Keep envelope.NextAction aligned with metadata after promotion (empty/partial
	// recovery guidance may only appear on metadata after ApplyDiagnosticMetadata).
	if next := strings.TrimSpace(fmt.Sprint(envelope.Metadata[toolresult.MetadataNextActionKey])); next != "" && next != "<nil>" {
		if strings.TrimSpace(envelope.NextAction) == "" || len(next) > len(strings.TrimSpace(envelope.NextAction)) {
			envelope.NextAction = next
		}
	}
	// Lightweight efficiency telemetry: disposition + error_code for failed.
	// Generic (no tool-name labels) so sessions can be correlated with the
	// offline efficiency report without exploding metric cardinality.
	outcome := strings.TrimSpace(diagnostic.Outcome)
	if outcome == "" && !diagnostic.OK {
		outcome = toolresult.OutcomeFailed
	}
	observability.RecordToolOutcome(outcome, diagnostic.ErrorCode)

	// O-1: model-visible bytes after reduction (L4 exit); the gap against
	// tool_output_original_bytes quantifies per-call folding.
	observability.RecordToolOutputBytes(observability.MetricToolOutputModelVisibleBytes, observability.TruncationLayerRender, len(envelope.Render()))

	if len(processErrs) > 0 {
		envelope.Metadata["gateway_errors"] = processErrs
	}

	return envelope, joinErrors(processErrs)
}

// isArtifactReadWindow reports whether the raw result is itself a dereferenced
// artifact window (metadata carries artifact_source_id). Such results already
// point at the canonical record; archiving them again would append a second
// pointer line to the full raw output and grow the cascade hop by hop.
func isArtifactReadWindow(metadata map[string]interface{}) bool {
	if len(metadata) == 0 {
		return false
	}
	if value, ok := metadata[artifactSourceIDMetadataKey].(string); ok {
		return strings.TrimSpace(value) != ""
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := nested[artifactSourceIDMetadataKey].(string); ok {
			return strings.TrimSpace(value) != ""
		}
	}
	return false
}

// artifactArchiveMinBytes resolves the P1-1 archive tiering threshold: text
// below this size is skipped by the session archive (metadata records
// artifact_skipped=below_threshold).
//
// The threshold is the model-visible window of the producing tool - its
// declared budget when it has one (shell 32 KiB, view/grep 32 KiB, glob/ls
// 16 KiB), the render-layer backstop otherwise - because that is exactly the
// line below which the model already sees the whole body: a pointer would
// never be dereferenced and only adds store/search-index noise. A fixed
// fraction of the backstop (the old budget/12 ≈ 1 KiB) archived bodies the
// model could read end to end and attached pointers for them.
func artifactArchiveMinBytes(metadata map[string]interface{}) int {
	window := modelToolTextByteBudget
	if declared := toolresult.ModelVisibleBudgetBytes(metadata); declared > window {
		window = declared
	}
	if window > modelToolTextBudgetCeilingBytes {
		window = modelToolTextBudgetCeilingBytes
	}
	return window
}

func prefersModelSummaryForLargeText(reducerName string) bool {
	switch strings.ToLower(strings.TrimSpace(reducerName)) {
	case "go_test_json", "go_test_text", "log_summary":
		return true
	default:
		return false
	}
}

// shouldAttachAttemptedArgs reports whether recovery contracts should surface a
// compact args summary. Ordinary success stays free of attempted_args.
func shouldAttachAttemptedArgs(toolErr string, metadata map[string]interface{}) bool {
	if strings.TrimSpace(toolErr) != "" {
		return true
	}
	if empty, ok := metadata[toolresult.MetadataEmptyResultKey].(bool); ok && empty {
		return true
	}
	outcome := strings.TrimSpace(fmt.Sprint(metadata[toolresult.MetadataOutcomeKey]))
	switch outcome {
	case toolresult.OutcomeEmpty, toolresult.OutcomePartial, toolresult.OutcomeFailed:
		return true
	}
	if partial, ok := metadata[toolresult.MetadataPartialFailureKey].(bool); ok && partial {
		return true
	}
	return false
}

// Render 把 envelope 变成适合放入 tool_result 的文本。
func (e *Envelope) Render() string {
	if e == nil {
		return ""
	}

	parts := make([]string, 0, 3)
	if e.Error != "" {
		parts = append(parts, "Tool execution failed: "+e.Error)
	}
	if strings.TrimSpace(e.Summary) != "" {
		parts = append(parts, strings.TrimSpace(e.Summary))
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func mergeEnvelope(base *Envelope, override *Envelope) *Envelope {
	if base == nil {
		base = &Envelope{}
	}
	if override == nil {
		return base
	}

	if override.ToolName != "" {
		base.ToolName = override.ToolName
	}
	if override.ToolCallID != "" {
		base.ToolCallID = override.ToolCallID
	}
	if override.Summary != "" {
		base.Summary = override.Summary
	}
	if override.Error != "" {
		base.Error = override.Error
	}
	if len(override.ArtifactIDs) > 0 {
		base.ArtifactIDs = append(base.ArtifactIDs, override.ArtifactIDs...)
	}
	base.Metadata = mergeMap(base.Metadata, override.Metadata)

	return base
}

func stringify(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case fmt.Stringer:
		return typed.String()
	default:
		payload, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(payload)
	}
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func mergeMap(left, right map[string]interface{}) map[string]interface{} {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	merged := cloneMap(left)
	if merged == nil {
		merged = map[string]interface{}{}
	}
	for key, value := range right {
		merged[key] = value
	}
	return merged
}

func preview(content string, maxLen int) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if maxLen <= 0 || len(content) <= maxLen {
		return content
	}
	if maxLen <= 3 {
		return content[:maxLen]
	}
	return content[:maxLen-3] + "..."
}

func cacheSafeSummaryOverride(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[cacheSafeSummaryMetadataKey].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	toolMetadata, _ := metadata["tool_metadata"].(map[string]interface{})
	if len(toolMetadata) == 0 {
		return ""
	}
	if value, ok := toolMetadata[cacheSafeSummaryMetadataKey].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return ""
}

func joinErrors(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "; "))
}
