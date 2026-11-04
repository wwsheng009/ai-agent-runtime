package outputdrain

import "testing"

func TestLedgerLateWriteIsVersionedAndCounted(t *testing.T) {
	ledger := Ledger{
		Version:        1,
		Phase:          PhaseDrained,
		AbsorbedBytes:  12,
		DrainedBytes:   12,
		ConflictPolicy: ConflictDiscardLate,
	}
	next := ledger.AddLate(5)
	if next.Version != 2 || next.Phase != PhaseLateDiscarded || next.LateBytes != 5 || !next.LateDiscarded {
		t.Fatalf("AddLate = %+v", next)
	}
	if next.AbsorbedBytes != 12 || next.DrainedBytes != 12 {
		t.Fatalf("late write changed stable bytes: %+v", next)
	}

	var metrics Metrics
	metrics.Add(next)
	if metrics.Records != 1 || metrics.Versions != 2 || metrics.LateBytes != 5 || metrics.LateDiscarded != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
	if metrics.ConflictPolicies[ConflictDiscardLate] != 1 {
		t.Fatalf("conflict policies = %+v", metrics.ConflictPolicies)
	}
}

func TestLedgerNormalizeKeepsMonotonicDefaults(t *testing.T) {
	ledger := Ledger{LateBytes: 3}
	normalized := ledger.Normalize()
	if normalized.Version != 1 || normalized.Phase != PhaseDrained || !normalized.LateDiscarded {
		t.Fatalf("Normalize = %+v", normalized)
	}
	if normalized.ConflictPolicy != ConflictDiscardLate {
		t.Fatalf("conflict policy = %q", normalized.ConflictPolicy)
	}
}
