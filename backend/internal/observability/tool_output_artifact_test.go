package observability

import (
	"testing"
)

func TestArtifactFlowRecordersNormalizeLabels(t *testing.T) {
	prev := GlobalMetrics
	GlobalMetrics = NewRegistry()
	t.Cleanup(func() { GlobalMetrics = prev })

	RecordToolOutputArchive(ArchiveLayerGateway, ArchiveDispositionArchived)
	RecordToolOutputArchive(ArchiveLayerGateway, ArchiveDispositionSkippedBelowThreshold)
	RecordToolOutputArchive(ArchiveLayerShellDisk, ArchiveDispositionSkippedReadWindow)
	// Unknown layer/disposition must collapse into bounded "other" cells.
	RecordToolOutputArchive("weird-layer", "mystery")
	RecordToolOutputArchive("", "")

	assertCounter(t, MetricToolOutputArchiveTotal, map[string]string{
		LabelLayer: ArchiveLayerGateway, LabelDisposition: ArchiveDispositionArchived,
	}, 1)
	assertCounter(t, MetricToolOutputArchiveTotal, map[string]string{
		LabelLayer: ArchiveLayerGateway, LabelDisposition: ArchiveDispositionSkippedBelowThreshold,
	}, 1)
	assertCounter(t, MetricToolOutputArchiveTotal, map[string]string{
		LabelLayer: ArchiveLayerShellDisk, LabelDisposition: ArchiveDispositionSkippedReadWindow,
	}, 1)
	assertCounter(t, MetricToolOutputArchiveTotal, map[string]string{
		LabelLayer: "other", LabelDisposition: "other",
	}, 2)

	RecordToolOutputTruncation(TruncationLayerView, TruncatedByBytes)
	RecordToolOutputTruncation(TruncationLayerGrep, TruncatedByLines)
	RecordToolOutputTruncation(TruncationLayerRender, TruncatedByBytes)
	RecordToolOutputTruncation("unknown-layer", TruncatedByBytes)

	assertCounter(t, MetricToolOutputTruncationTotal, map[string]string{
		LabelLayer: TruncationLayerView, LabelTruncatedBy: TruncatedByBytes,
	}, 1)
	assertCounter(t, MetricToolOutputTruncationTotal, map[string]string{
		LabelLayer: "other", LabelTruncatedBy: TruncatedByBytes,
	}, 1)

	RecordToolPointerNotice(PointerNoticeKindID)
	RecordToolPointerNotice(PointerNoticeKindPath)
	RecordToolPointerNotice(PointerNoticeKindDerefHint)
	RecordToolPointerNotice("bogus")
	assertCounter(t, MetricToolPointerNoticeTotal, map[string]string{LabelKind: PointerNoticeKindID}, 1)
	assertCounter(t, MetricToolPointerNoticeTotal, map[string]string{LabelKind: "other"}, 1)

	RecordToolArtifactDeref(false)
	RecordToolArtifactDeref(false)
	RecordToolArtifactDeref(true)
	assertCounter(t, MetricToolArtifactDerefTotal, map[string]string{LabelPage: DerefPageFirst}, 2)
	assertCounter(t, MetricToolArtifactDerefTotal, map[string]string{LabelPage: DerefPageFollowup}, 1)

	RecordToolArtifactDerefMiss(DerefMissReasonNotFound)
	RecordToolArtifactDerefMiss(DerefMissReasonCrossSession)
	RecordToolArtifactDerefMiss("made-up")
	assertCounter(t, MetricToolArtifactDerefMissTotal, map[string]string{LabelReason: DerefMissReasonNotFound}, 1)
	assertCounter(t, MetricToolArtifactDerefMissTotal, map[string]string{LabelReason: "other"}, 1)

	RecordToolOutputBytes(MetricToolOutputOriginalBytes, TruncationLayerRender, 4096)
	RecordToolOutputBytes(MetricToolOutputModelVisibleBytes, TruncationLayerRender, 1024)
	RecordToolOutputBytes(MetricToolArtifactDerefBytes, ArchiveLayerGateway, 2048)
	// Negative observations clamp to zero instead of poisoning the histogram.
	RecordToolOutputBytes(MetricToolOutputOriginalBytes, TruncationLayerRender, -5)

	hist := GlobalMetrics.GetOrCreateHistogram(MetricToolOutputOriginalBytes, map[string]string{
		LabelLayer: TruncationLayerRender,
	}, nil)
	if hist == nil || hist.GetCount() != 2 {
		t.Fatalf("original bytes histogram count: want 2, got %+v", hist)
	}
}

func TestSnapshotArtifactFlowAggregation(t *testing.T) {
	prev := GlobalMetrics
	GlobalMetrics = NewRegistry()
	t.Cleanup(func() { GlobalMetrics = prev })

	// Archives: 1 archived gateway, 3 skipped below threshold, 1 shell disk.
	RecordToolOutputArchive(ArchiveLayerGateway, ArchiveDispositionArchived)
	for i := 0; i < 3; i++ {
		RecordToolOutputArchive(ArchiveLayerGateway, ArchiveDispositionSkippedBelowThreshold)
	}
	RecordToolOutputArchive(ArchiveLayerShellDisk, ArchiveDispositionArchived)

	// Truncations: 2 view + 1 grep + 1 l4_render -> gap ratio 0.25.
	RecordToolOutputTruncation(TruncationLayerView, TruncatedByBytes)
	RecordToolOutputTruncation(TruncationLayerView, TruncatedByLines)
	RecordToolOutputTruncation(TruncationLayerGrep, TruncatedByBytes)
	RecordToolOutputTruncation(TruncationLayerRender, TruncatedByBytes)

	RecordToolPointerNotice(PointerNoticeKindID)
	RecordToolPointerNotice(PointerNoticeKindDerefHint)

	// Deref: 2 first + 3 followup -> followup ratio 0.6 (heavy), 5 samples
	// reaches the deref flag sample floor.
	RecordToolArtifactDeref(false)
	RecordToolArtifactDeref(false)
	RecordToolArtifactDeref(true)
	RecordToolArtifactDeref(true)
	RecordToolArtifactDeref(true)
	RecordToolArtifactDerefMiss(DerefMissReasonNotFound)

	snap := SnapshotToolEfficiency()
	flow := snap.ArtifactFlow

	if flow.Archives.Total != 5 {
		t.Fatalf("archives total: want 5, got %v", flow.Archives.Total)
	}
	if flow.Archives.ByDisposition[ArchiveDispositionSkippedBelowThreshold] != 3 {
		t.Fatalf("skipped_below_threshold: want 3, got %v", flow.Archives.ByDisposition[ArchiveDispositionSkippedBelowThreshold])
	}
	if flow.Archives.ByLayer[ArchiveLayerShellDisk] != 1 {
		t.Fatalf("shell_disk layer: want 1, got %v", flow.Archives.ByLayer[ArchiveLayerShellDisk])
	}

	if flow.Truncations.Total != 4 {
		t.Fatalf("truncations total: want 4, got %v", flow.Truncations.Total)
	}
	if flow.Truncations.ByLayer[TruncationLayerRender] != 1 {
		t.Fatalf("l4_render truncations: want 1, got %v", flow.Truncations.ByLayer[TruncationLayerRender])
	}
	if flow.L1L4GapRatio != 0.25 {
		t.Fatalf("l1_l4_gap_ratio: want 0.25, got %v", flow.L1L4GapRatio)
	}

	if flow.PointerNotice[PointerNoticeKindID] != 1 || flow.PointerNotice[PointerNoticeKindDerefHint] != 1 {
		t.Fatalf("pointer notice kinds: %+v", flow.PointerNotice)
	}

	if flow.Deref.Total != 5 {
		t.Fatalf("deref total: want 5, got %v", flow.Deref.Total)
	}
	if r := flow.Deref.FollowupRatio; r < 0.59 || r > 0.61 {
		t.Fatalf("followup_ratio: want ~0.6, got %v", r)
	}
	if flow.Deref.MissByReason[DerefMissReasonNotFound] != 1 {
		t.Fatalf("deref miss not_found: want 1, got %v", flow.Deref.MissByReason[DerefMissReasonNotFound])
	}

	flagSet := map[string]bool{}
	for _, f := range snap.InefficiencyFlags {
		flagSet[f] = true
	}
	if !flagSet["artifact_deref_heavy"] {
		t.Fatalf("expected artifact_deref_heavy flag (followup ratio > 0.30), got %v", snap.InefficiencyFlags)
	}
	if flagSet["l1_l4_gap_present"] {
		// Truncation samples are below the minSamples floor (4 < 10), so the
		// gap flag must not fire even though the ratio is 25%.
		t.Fatalf("l1_l4_gap_present should not fire below sample floor, got %v", snap.InefficiencyFlags)
	}

	// archive_skipped_majority: skipped 3/5 = 60% < 80% -> must not fire.
	if flagSet["archive_skipped_majority"] {
		t.Fatalf("archive_skipped_majority should not fire at 60%% skipped, got %v", snap.InefficiencyFlags)
	}
}

func TestSnapshotArtifactFlowFlagsFireAtThresholds(t *testing.T) {
	prev := GlobalMetrics
	GlobalMetrics = NewRegistry()
	t.Cleanup(func() { GlobalMetrics = prev })

	// archive_skipped_majority: 9 skipped / 10 total = 90%.
	for i := 0; i < 9; i++ {
		RecordToolOutputArchive(ArchiveLayerGateway, ArchiveDispositionSkippedBelowThreshold)
	}
	RecordToolOutputArchive(ArchiveLayerGateway, ArchiveDispositionArchived)

	// l1_l4_gap_present: >=10 truncation samples with l4_render > 5%.
	// 1 render / 10 total = 10%.
	RecordToolOutputTruncation(TruncationLayerRender, TruncatedByBytes)
	for i := 0; i < 9; i++ {
		RecordToolOutputTruncation(TruncationLayerView, TruncatedByBytes)
	}

	snap := SnapshotToolEfficiency()
	flagSet := map[string]bool{}
	for _, f := range snap.InefficiencyFlags {
		flagSet[f] = true
	}
	if !flagSet["archive_skipped_majority"] {
		t.Fatalf("expected archive_skipped_majority at 90%% skipped, got %v", snap.InefficiencyFlags)
	}
	if !flagSet["l1_l4_gap_present"] {
		t.Fatalf("expected l1_l4_gap_present with 10%% l4_render share, got %v", snap.InefficiencyFlags)
	}

	// Empty registry snapshot keeps artifact_flow maps non-nil.
	empty := NewRegistry().SnapshotToolEfficiency()
	if empty.ArtifactFlow.Archives.ByLayer == nil || empty.ArtifactFlow.Truncations.ByLayer == nil ||
		empty.ArtifactFlow.PointerNotice == nil || empty.ArtifactFlow.Deref.MissByReason == nil {
		t.Fatalf("empty artifact flow snapshot maps must be non-nil: %+v", empty.ArtifactFlow)
	}
}
