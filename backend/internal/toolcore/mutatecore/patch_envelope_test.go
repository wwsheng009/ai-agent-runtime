package mutatecore

import (
	"strings"
	"testing"
)

func TestNormalizePatchEnvelopeUnwrapsHeredocAndFence(t *testing.T) {
	raw := strings.Join([]string{
		"<<'EOF'",
		"```",
		PatchBegin,
		PatchAdd + "a.txt",
		"+alpha",
		PatchEnd,
		"```",
		"EOF",
	}, "\n")
	got := NormalizePatchEnvelope(raw)
	want := strings.Join([]string{
		PatchBegin,
		PatchAdd + "a.txt",
		"+alpha",
		PatchEnd,
	}, "\n")
	if got != want {
		t.Fatalf("normalized envelope mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestNormalizePatchEnvelopeSplitsCollapsedMarkers(t *testing.T) {
	raw := PatchBegin + " " + PatchAdd + "a.txt" + PatchEnd
	got := NormalizePatchEnvelope(raw)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected three split lines, got %q", got)
	}
	if lines[0] != PatchBegin || lines[1] != PatchAdd+"a.txt" || lines[len(lines)-1] != PatchEnd {
		t.Fatalf("unexpected split result: %#v", lines)
	}
}

func TestNormalizePatchEnvelopeAcceptsBoundaryVariants(t *testing.T) {
	raw := PatchBegin + " ***\n" + PatchAdd + "a.txt\n+alpha\n" + PatchEnd + "***"
	got := NormalizePatchEnvelope(raw)
	if !strings.HasPrefix(got, PatchBegin+"\n") {
		t.Fatalf("begin variant not normalized: %q", got)
	}
	if !strings.HasSuffix(got, "\n"+PatchEnd) {
		t.Fatalf("end variant not normalized: %q", got)
	}
}
