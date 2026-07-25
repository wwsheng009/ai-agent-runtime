package observability

import (
	"testing"
)

func TestRecordToolPreflight(t *testing.T) {
	prev := GlobalMetrics
	GlobalMetrics = NewRegistry()
	t.Cleanup(func() { GlobalMetrics = prev })

	RecordToolPreflight(PreflightReasonRequiredArgs, false)
	RecordToolPreflight(PreflightReasonArgTypes, false)
	RecordToolPreflight(PreflightReasonPathExistence, false)
	RecordToolPreflight(PreflightReasonCircuitOpen, false)
	RecordToolPreflight("", true)
	// Free-form deny reason must not explode labels.
	RecordToolPreflight("something_tool_specific", false)

	assertCounter(t, MetricToolPreflightTotal, map[string]string{
		LabelReason:   PreflightReasonRequiredArgs,
		LabelDecision: "deny",
	}, 1)
	assertCounter(t, MetricToolPreflightTotal, map[string]string{
		LabelReason:   PreflightReasonArgTypes,
		LabelDecision: "deny",
	}, 1)
	assertCounter(t, MetricToolPreflightTotal, map[string]string{
		LabelReason:   PreflightReasonPathExistence,
		LabelDecision: "deny",
	}, 1)
	assertCounter(t, MetricToolPreflightTotal, map[string]string{
		LabelReason:   PreflightReasonCircuitOpen,
		LabelDecision: "deny",
	}, 1)
	assertCounter(t, MetricToolPreflightTotal, map[string]string{
		LabelReason:   PreflightReasonAllow,
		LabelDecision: "allow",
	}, 1)
	assertCounter(t, MetricToolPreflightTotal, map[string]string{
		LabelReason:   PreflightReasonUnknown,
		LabelDecision: "deny",
	}, 1)
}

func TestRecordToolOutcome(t *testing.T) {
	prev := GlobalMetrics
	GlobalMetrics = NewRegistry()
	t.Cleanup(func() { GlobalMetrics = prev })

	RecordToolOutcome(ToolOutcomeSuccess, "SHOULD_IGNORE")
	RecordToolOutcome(ToolOutcomeEmpty, "")
	RecordToolOutcome(ToolOutcomePartial, "")
	RecordToolOutcome(ToolOutcomeFailed, "TOOL_INVALID_ARGS")
	RecordToolOutcome(ToolOutcomeFailed, "")
	RecordToolOutcome("weird", "X")

	assertCounter(t, MetricToolOutcomeTotal, map[string]string{
		LabelOutcome:   ToolOutcomeSuccess,
		LabelErrorCode: "none",
	}, 1)
	assertCounter(t, MetricToolOutcomeTotal, map[string]string{
		LabelOutcome:   ToolOutcomeEmpty,
		LabelErrorCode: "none",
	}, 1)
	assertCounter(t, MetricToolOutcomeTotal, map[string]string{
		LabelOutcome:   ToolOutcomePartial,
		LabelErrorCode: "none",
	}, 1)
	assertCounter(t, MetricToolOutcomeTotal, map[string]string{
		LabelOutcome:   ToolOutcomeFailed,
		LabelErrorCode: "TOOL_INVALID_ARGS",
	}, 1)
	assertCounter(t, MetricToolOutcomeTotal, map[string]string{
		LabelOutcome:   ToolOutcomeFailed,
		LabelErrorCode: "unknown",
	}, 1)
	assertCounter(t, MetricToolOutcomeTotal, map[string]string{
		LabelOutcome:   ToolOutcomeUnknown,
		LabelErrorCode: "none",
	}, 1)
}

func TestRecordToolDispositionReplay(t *testing.T) {
	prev := GlobalMetrics
	GlobalMetrics = NewRegistry()
	t.Cleanup(func() { GlobalMetrics = prev })

	RecordToolDispositionReplay(ToolOutcomeEmpty, 1)
	RecordToolDispositionReplay(ToolOutcomePartial, 2)
	RecordToolDispositionReplay(ToolOutcomeEmpty, 5)
	// success/failed must not be recorded
	RecordToolDispositionReplay(ToolOutcomeSuccess, 3)
	RecordToolDispositionReplay(ToolOutcomeFailed, 3)

	assertCounter(t, MetricToolDispositionReplayTotal, map[string]string{
		LabelOutcome: ToolOutcomeEmpty,
		LabelRepeat:  "1",
	}, 1)
	assertCounter(t, MetricToolDispositionReplayTotal, map[string]string{
		LabelOutcome: ToolOutcomePartial,
		LabelRepeat:  "2",
	}, 1)
	assertCounter(t, MetricToolDispositionReplayTotal, map[string]string{
		LabelOutcome: ToolOutcomeEmpty,
		LabelRepeat:  "3plus",
	}, 1)
	// Ensure success/failed counters were not created.
	if got := GlobalMetrics.GetOrCreateCounter(MetricToolDispositionReplayTotal, map[string]string{
		LabelOutcome: ToolOutcomeSuccess,
		LabelRepeat:  "3plus",
	}).Get(); got != 0 {
		t.Fatalf("expected success disposition replay not counted, got %v", got)
	}
}

func TestSnapshotToolEfficiency(t *testing.T) {
	prev := GlobalMetrics
	GlobalMetrics = NewRegistry()
	t.Cleanup(func() { GlobalMetrics = prev })

	// >=10 samples so rate-based inefficiency flags can fire.
	for i := 0; i < 6; i++ {
		RecordToolPreflight(PreflightReasonAllow, true)
	}
	for i := 0; i < 3; i++ {
		RecordToolPreflight(PreflightReasonRequiredArgs, false)
	}
	RecordToolPreflight(PreflightReasonPathExistence, false)

	// 12 outcomes so rate flags can fire.
	for i := 0; i < 8; i++ {
		RecordToolOutcome(ToolOutcomeSuccess, "")
	}
	RecordToolOutcome(ToolOutcomeEmpty, "")
	RecordToolOutcome(ToolOutcomePartial, "")
	RecordToolOutcome(ToolOutcomeFailed, "TOOL_SHELL_COMPAT")
	RecordToolOutcome(ToolOutcomeFailed, "TOOL_PATH_NOT_FOUND")

	RecordToolDispositionReplay(ToolOutcomeEmpty, 1)
	RecordToolDispositionReplay(ToolOutcomeEmpty, 4)

	snap := SnapshotToolEfficiency()
	if snap.Preflight.Total != 10 {
		t.Fatalf("preflight total: want 10, got %v", snap.Preflight.Total)
	}
	if snap.Preflight.Allow != 6 || snap.Preflight.Deny != 4 {
		t.Fatalf("preflight allow/deny: want 6/4, got %v/%v", snap.Preflight.Allow, snap.Preflight.Deny)
	}
	if snap.Preflight.AllowRate != 0.6 {
		t.Fatalf("preflight allow_rate: want 0.6, got %v", snap.Preflight.AllowRate)
	}
	if snap.Preflight.ByReason[PreflightReasonPathExistence] != 1 {
		t.Fatalf("path_existence reason count: want 1, got %v", snap.Preflight.ByReason[PreflightReasonPathExistence])
	}

	if snap.Outcomes.Total != 12 {
		t.Fatalf("outcomes total: want 12, got %v", snap.Outcomes.Total)
	}
	if snap.Outcomes.ByOutcome[ToolOutcomeSuccess] != 8 {
		t.Fatalf("success count: want 8, got %v", snap.Outcomes.ByOutcome[ToolOutcomeSuccess])
	}
	if snap.Outcomes.ByErrorCode["TOOL_SHELL_COMPAT"] != 1 {
		t.Fatalf("shell_compat error code: want 1, got %v", snap.Outcomes.ByErrorCode["TOOL_SHELL_COMPAT"])
	}
	if snap.Outcomes.SuccessRate <= 0 || snap.Outcomes.SuccessRate >= 1 {
		t.Fatalf("success_rate should be in (0,1), got %v", snap.Outcomes.SuccessRate)
	}
	if snap.Outcomes.NonFailRate <= snap.Outcomes.SuccessRate {
		t.Fatalf("non_fail_rate (%v) should be > success_rate (%v)", snap.Outcomes.NonFailRate, snap.Outcomes.SuccessRate)
	}

	if snap.FailCategories["shell_compat"] != 1 {
		t.Fatalf("fail_categories shell_compat: want 1, got %v", snap.FailCategories["shell_compat"])
	}
	if snap.FailCategories["path_missing"] != 1 {
		t.Fatalf("fail_categories path_missing: want 1, got %v", snap.FailCategories["path_missing"])
	}

	if snap.DispositionReplays.Total != 2 {
		t.Fatalf("replay total: want 2, got %v", snap.DispositionReplays.Total)
	}
	if snap.DispositionReplays.ByRepeat["3plus"] != 1 {
		t.Fatalf("replay 3plus: want 1, got %v", snap.DispositionReplays.ByRepeat["3plus"])
	}

	flagSet := map[string]bool{}
	for _, f := range snap.InefficiencyFlags {
		flagSet[f] = true
	}
	for _, want := range []string{
		"high_preflight_deny_rate",
		"disposition_replays_present",
		"repeated_empty_partial_3plus",
		"shell_compat_failures",
		"path_missing_failures",
		"path_existence_preflight_denies",
	} {
		if !flagSet[want] {
			t.Fatalf("expected inefficiency flag %q, got %v", want, snap.InefficiencyFlags)
		}
	}

	// Empty registry snapshot should not panic and should leave zero totals.
	empty := NewRegistry().SnapshotToolEfficiency()
	if empty.Preflight.Total != 0 || empty.Outcomes.Total != 0 || empty.DispositionReplays.Total != 0 {
		t.Fatalf("empty snapshot should be zeroed, got %+v", empty)
	}
	if empty.InefficiencyFlags == nil {
		t.Fatal("inefficiency_flags should be non-nil empty slice")
	}
}

func assertCounter(t *testing.T, name string, labels map[string]string, want float64) {
	t.Helper()
	got := GlobalMetrics.GetOrCreateCounter(name, labels).Get()
	if got != want {
		t.Fatalf("%s labels=%v: want %v, got %v", name, labels, want, got)
	}
}
