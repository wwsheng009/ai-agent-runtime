package observability

// Artifact flow metrics: the tool-output → artifact → dereference pipeline
// observability described in
// docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md §11.
// All labels are low-cardinality generic values (layer/disposition/kind/...
// buckets); tool names never enter labels (label discipline, see
// tool_efficiency.go).

import (
	"strings"
)

// Metric names for the artifact cascade pipeline.
const (
	MetricToolOutputArchiveTotal     = "tool_output_archive_total"
	MetricToolOutputTruncationTotal  = "tool_output_truncation_total"
	MetricToolPointerNoticeTotal     = "tool_pointer_notice_total"
	MetricToolArtifactDerefTotal     = "tool_artifact_deref_total"
	MetricToolArtifactDerefMissTotal = "tool_artifact_deref_miss_total"

	MetricToolOutputOriginalBytes     = "tool_output_original_bytes"
	MetricToolOutputModelVisibleBytes = "tool_output_model_visible_bytes"
	MetricToolArtifactDerefBytes      = "tool_artifact_deref_bytes"
)

// Label keys used by artifact-flow metrics (all generic buckets).
const (
	LabelLayer       = "layer"
	LabelDisposition = "disposition"
	LabelTruncatedBy = "truncated_by"
	LabelPage        = "page"
	LabelKind        = "kind"
)

// Layer values for tool_output_archive_total / tool_output_truncation_total.
const (
	ArchiveLayerGateway   = "gateway"
	ArchiveLayerShellDisk = "shell_disk"

	TruncationLayerView         = "l1_view"
	TruncationLayerGrep         = "l1_grep"
	TruncationLayerRender       = "l4_render"
	TruncationLayerShellCapture = "shell_capture"
)

// Disposition values for tool_output_archive_total.
const (
	ArchiveDispositionArchived              = "archived"
	ArchiveDispositionSkippedBelowThreshold = "skipped_below_threshold"
	ArchiveDispositionSkippedReadWindow     = "skipped_read_window"
	ArchiveDispositionSkippedEmpty          = "skipped_empty"
)

// Truncation dimension values (truncated_by).
const (
	TruncatedByBytes = "bytes"
	TruncatedByLines = "lines"
)

// Deref page values for tool_artifact_deref_total.
const (
	DerefPageFirst    = "first"
	DerefPageFollowup = "followup"
)

// Deref miss reasons for tool_artifact_deref_miss_total.
const (
	DerefMissReasonNotFound     = "not_found"
	DerefMissReasonCrossSession = "cross_session"
	DerefMissReasonBadOffset    = "bad_offset"
	DerefMissReasonNoStore      = "no_store"
	DerefMissReasonBadArgs      = "bad_args"
)

// Pointer notice kinds for tool_pointer_notice_total.
const (
	PointerNoticeKindID        = "id"
	PointerNoticeKindPath      = "path"
	PointerNoticeKindDerefHint = "deref_hint"
)

// RecordToolOutputArchive counts gateway / shell-disk archive decisions.
// disposition must be one of the ArchiveDisposition* values; unknown values
// collapse to a bounded "other" cell to keep cardinality stable.
func RecordToolOutputArchive(layer, disposition string) {
	IncrementCounter(MetricToolOutputArchiveTotal, map[string]string{
		LabelLayer:       normalizeArchiveLayer(layer),
		LabelDisposition: normalizeArchiveDisposition(disposition),
	})
}

// RecordToolOutputTruncation counts truncation events per layer and dimension.
func RecordToolOutputTruncation(layer, truncatedBy string) {
	IncrementCounter(MetricToolOutputTruncationTotal, map[string]string{
		LabelLayer:       normalizeTruncationLayer(layer),
		LabelTruncatedBy: normalizeTruncatedBy(truncatedBy),
	})
}

// RecordToolPointerNotice counts pointer notice lines appended to model-visible
// tool text (kind=id pointer rows, kind=path notices, kind=deref_hint P2
// continuation hints inside fold markers).
func RecordToolPointerNotice(kind string) {
	IncrementCounter(MetricToolPointerNoticeTotal, map[string]string{
		LabelKind: normalizePointerNoticeKind(kind),
	})
}

// RecordToolArtifactDeref counts artifact_read calls. followup marks
// continuation reads (offset>0); the followup ratio approximates the average
// dereference hop count without cross-call state.
func RecordToolArtifactDeref(followup bool) {
	page := DerefPageFirst
	if followup {
		page = DerefPageFollowup
	}
	IncrementCounter(MetricToolArtifactDerefTotal, map[string]string{
		LabelPage: page,
	})
}

// RecordToolArtifactDerefMiss counts artifact_read failures by bounded reason.
func RecordToolArtifactDerefMiss(reason string) {
	IncrementCounter(MetricToolArtifactDerefMissTotal, map[string]string{
		LabelReason: normalizeDerefMissReason(reason),
	})
}

// RecordToolOutputBytes observes raw/model-visible byte counts on the shared
// byte-size histograms (bytes, not seconds — see §11.2 直方图).
func RecordToolOutputBytes(name string, layer string, bytes int) {
	if bytes < 0 {
		bytes = 0
	}
	GlobalMetrics.GetOrCreateHistogram(name, map[string]string{
		LabelLayer: normalizeByteLayer(name, layer),
	}, byteSizeBuckets()).Observe(float32(bytes))
}

// byteSizeBuckets are byte-domain buckets for output-size histograms.
func byteSizeBuckets() []float32 {
	return []float32{
		256, 1024, 4096, 8192, 12288, 16384, 32768, 65536, 131072, 262144, 524288, 1048576,
	}
}

// normalizeByteLayer bounds layer values per histogram.
func normalizeByteLayer(metric, layer string) string {
	switch metric {
	case MetricToolOutputOriginalBytes, MetricToolOutputModelVisibleBytes:
		return normalizeTruncationLayer(layer)
	case MetricToolArtifactDerefBytes:
		return ArchiveLayerGateway
	default:
		return "other"
	}
}

func normalizeArchiveLayer(layer string) string {
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case ArchiveLayerGateway, ArchiveLayerShellDisk:
		return strings.ToLower(strings.TrimSpace(layer))
	default:
		return "other"
	}
}

func normalizeArchiveDisposition(disposition string) string {
	switch strings.ToLower(strings.TrimSpace(disposition)) {
	case ArchiveDispositionArchived,
		ArchiveDispositionSkippedBelowThreshold,
		ArchiveDispositionSkippedReadWindow,
		ArchiveDispositionSkippedEmpty:
		return strings.ToLower(strings.TrimSpace(disposition))
	default:
		return "other"
	}
}

func normalizeTruncationLayer(layer string) string {
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case TruncationLayerView, TruncationLayerGrep, TruncationLayerRender, TruncationLayerShellCapture:
		return strings.ToLower(strings.TrimSpace(layer))
	default:
		return "other"
	}
}

func normalizeTruncatedBy(by string) string {
	switch strings.ToLower(strings.TrimSpace(by)) {
	case TruncatedByBytes, TruncatedByLines:
		return strings.ToLower(strings.TrimSpace(by))
	default:
		return "other"
	}
}

func normalizePointerNoticeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case PointerNoticeKindID, PointerNoticeKindPath, PointerNoticeKindDerefHint:
		return strings.ToLower(strings.TrimSpace(kind))
	default:
		return "other"
	}
}

func normalizeDerefMissReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case DerefMissReasonNotFound, DerefMissReasonCrossSession, DerefMissReasonBadOffset,
		DerefMissReasonNoStore, DerefMissReasonBadArgs:
		return strings.ToLower(strings.TrimSpace(reason))
	default:
		return "other"
	}
}
