package viewcore

// M1 of docs/plan/envd-view-line-window-design-20261028.md. The claim under test is
// narrow and absolute: handing the core a line window plus the source's line count
// must produce the SAME answer as handing it the whole file. Not "a similar answer",
// not "the same content but different metadata" - every published field, including
// the emptiness and beyond-the-end notes, has to agree.
//
// Everything here is computed against the whole-file render as the oracle, so no
// expectation is a restatement of the implementation.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// windowBytes returns the raw bytes of lines [from..to], 1-based and inclusive,
// keeping terminators exactly as the file has them.
func windowBytes(t *testing.T, body []byte, from, to int) []byte {
	t.Helper()
	if from > to || from < 1 {
		t.Fatalf("bad window %d..%d", from, to)
	}
	line, start := 1, -1
	for index := 0; index < len(body); index++ {
		if line == from && start < 0 {
			start = index
		}
		if body[index] == '\n' {
			if line == to {
				return body[start : index+1]
			}
			line++
		}
	}
	if start >= 0 {
		return body[start:]
	}
	return nil
}

func windowBodies() map[string][]byte {
	var builder strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&builder, "row %02d: payload\n", i)
	}
	plain := []byte(builder.String())
	return map[string][]byte{
		"plain": plain,
		"crlf":  []byte(strings.ReplaceAll(string(plain), "\n", "\r\n")),
		"unterminated": append(append([]byte{}, plain...),
			[]byte("row 40: no newline at all")...),
		"nul":   []byte("a\x00b\nsecond\x00\nthird\nrow3\nrow4\n"),
		"blank": []byte("\n\n\nx\n\n"),
		"empty": {},
		"one":   []byte("only line\n"),
	}
}

// TestWindowPlusFactsMatchesWholeFileRender is the acceptance gate for the whole
// design: same content, same eof, same total, same truncation, same notes.
func TestWindowPlusFactsMatchesWholeFileRender(t *testing.T) {
	budget := 4096
	limit := 5
	for name, body := range windowBodies() {
		total := countLinesWholeFile(body)
		// Offsets are chosen after the count is known: building this expression
		// before the zero check would divide by zero on the empty fixture.
		var offsets []int
		if total == 0 {
			offsets = []int{0, 1, 4}
		} else {
			offsets = []int{0, 1, total / 2, total - 1, total, total + 3}
		}
		for _, offset := range offsets {
			t.Run(fmt.Sprintf("%s/offset=%d", name, offset), func(t *testing.T) {
				request := Request{Offset: offset, Limit: limit}
				whole, wholeStats, wholeErr := ReadTextFromBytes(body, request, Options{ByteBudget: budget})
				if wholeErr != nil {
					t.Fatalf("whole-file render failed: %v", wholeErr)
				}

				from, to := offset+1, offset+limit
				var window []byte
				if from <= total {
					window = windowBytes(t, body, max(from, 1), min(to, max(total, 1)))
				}
				got, stats, err := ReadTextFromBytes(window, request, Options{
					ByteBudget: budget,
					Facts:      &SourceFacts{TotalLines: total, TotalLinesKnown: true, StartsAtOffset: true},
				})
				if err != nil {
					t.Fatalf("window render failed: %v", err)
				}
				if got != whole {
					t.Fatalf("window content differs\n  window: %q\n  whole:  %q", firstChunk(got, 200), firstChunk(whole, 200))
				}
				if stats.EOF != wholeStats.EOF {
					t.Fatalf("eof differs: window %v, whole %v (total=%d offset=%d linesRead=%d)",
						stats.EOF, wholeStats.EOF, total, offset, stats.LinesRead)
				}
				if stats.HasMore != wholeStats.HasMore {
					t.Fatalf("hasMore differs: window %v, whole %v", stats.HasMore, wholeStats.HasMore)
				}
				// total_lines is allowed to be KNOWN on one side and not the other, and
				// the asymmetry has a direction: to build a line window the source had to
				// count every line, while a local read stops at the requested window and
				// therefore never learns the total. Withholding a measured truth to match
				// the other side's ignorance would be the worse kind of parity. What is
				// NOT allowed is the two sides disagreeing on a number they both claim, so
				// the comparison is gated on both knowing.
				if stats.TotalLinesKnown && wholeStats.TotalLinesKnown && stats.TotalLines != wholeStats.TotalLines {
					t.Fatalf("total lines disagree where both claim to know: window %d, whole %d",
						stats.TotalLines, wholeStats.TotalLines)
				}
				if stats.TotalLinesKnown && stats.TotalLines != total {
					t.Fatalf("window total lines = %d, the source holds %d", stats.TotalLines, total)
				}
				if stats.LinesRead != wholeStats.LinesRead {
					t.Fatalf("lines read differ: window %d, whole %d", stats.LinesRead, wholeStats.LinesRead)
				}
				if stats.EmptyFile != wholeStats.EmptyFile {
					t.Fatalf("empty-file flag differs: window %v, whole %v", stats.EmptyFile, wholeStats.EmptyFile)
				}
				// The metadata the model actually reads, not just the Go struct.
				windowMeta := WindowMetadata(request, stats)
				wholeMeta := WindowMetadata(request, wholeStats)
				for _, key := range []string{"offset", "limit", "lines_read", "eof", "is_truncated"} {
					if windowMeta[key] != wholeMeta[key] {
						t.Fatalf("metadata[%s] differs: window %v, whole %v", key, windowMeta[key], wholeMeta[key])
					}
				}
				if wholeValue, ok := wholeMeta["total_lines"]; ok {
					if windowMeta["total_lines"] != wholeValue {
						t.Fatalf("metadata[total_lines] differs while both publish: window %v, whole %v",
							windowMeta["total_lines"], wholeValue)
					}
				}
			})
		}
	}
}

// TestAddedFinalNewlineIsInvisibleToTheWindow is the M1 proof for the fact the M0
// spike measured but refused to vouch for: awk's `print` terminates a final line the
// file left unterminated, so the window carries one byte the source never had.
func TestAddedFinalNewlineIsInvisibleToTheWindow(t *testing.T) {
	body := []byte("one\ntwo\nthree-not-terminated")
	request := Request{Offset: 2, Limit: 1}

	whole, wholeStats, err := ReadTextFromBytes(body, request, Options{ByteBudget: 4096})
	if err != nil {
		t.Fatal(err)
	}
	awkStyleWindow := append(windowBytes(t, body, 3, 3), '\n')
	if bytes.HasSuffix(windowBytes(t, body, 3, 3), []byte("\n")) {
		t.Fatal("the fixture's final line must be unterminated for this test to mean anything")
	}
	got, stats, err := ReadTextFromBytes(awkStyleWindow, request, Options{
		ByteBudget: 4096,
		Facts:      &SourceFacts{TotalLines: 3, TotalLinesKnown: true, StartsAtOffset: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != whole {
		t.Fatalf("the newline awk added changed the render: window %q, whole %q", got, whole)
	}
	if stats.EOF != wholeStats.EOF || !stats.EOF {
		t.Fatalf("eof differs or the last line was not recognised as the end: window %v, whole %v", stats.EOF, wholeStats.EOF)
	}
}

// TestWindowWithoutSkipping explains why SourceFacts.StartsAtOffset exists: the same
// bytes handed over without the flag are consumed by the skip loop, and the core
// then announces the read as beyond the end. That would be the silent failure of a
// remote line window.
func TestWindowWithoutSkipping(t *testing.T) {
	var builder strings.Builder
	total := 0
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&builder, "row %03d\n", i)
		total++
	}
	body := []byte(builder.String())
	deep := Request{Offset: 150, Limit: 3}
	window := windowBytes(t, body, 151, 153)

	unflagged, unflaggedStats, err := ReadTextFromBytes(window, deep, Options{ByteBudget: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unflagged, "beyond the end") {
		t.Fatalf("expected the skip loop to misread the window, got %q", firstChunk(unflagged, 200))
	}
	if unflaggedStats.LinesRead != 0 {
		t.Fatalf("unflagged window read %d lines, want 0 (that is the bug the flag prevents)", unflaggedStats.LinesRead)
	}

	flagged, flaggedStats, err := ReadTextFromBytes(window, deep, Options{
		ByteBudget: 4096,
		Facts:      &SourceFacts{TotalLines: total, TotalLinesKnown: true, StartsAtOffset: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "151: row 150\n152: row 151\n153: row 152"
	if flagged != want {
		t.Fatalf("flagged window = %q, want %q", firstChunk(flagged, 200), want)
	}
	if flaggedStats.EOF {
		t.Fatal("a window at line 153 of 200 may not claim eof")
	}
	if flaggedStats.TotalLines != total {
		t.Fatalf("total lines = %d, want the source's %d", flaggedStats.TotalLines, total)
	}
}

// TestFactsContradictionIsRejected is the anti-"two truths" gate: the window and the
// stated count must agree or the whole window is refused, so no answer is published
// that neither source proved.
func TestFactsContradictionIsRejected(t *testing.T) {
	body := []byte("a\nb\nc\nd\ne\n")
	tests := []struct {
		name   string
		data   []byte
		req    Request
		facts  SourceFacts
		wantEr string
	}{
		{"window past the stated end", windowBytes(t, body, 4, 5), Request{Offset: 3, Limit: 2},
			SourceFacts{TotalLines: 4, TotalLinesKnown: true, StartsAtOffset: true}, "but the source says 4 lines exist"},
		{"empty window inside the source", nil, Request{Offset: 2, Limit: 2},
			SourceFacts{TotalLines: 5, TotalLinesKnown: true, StartsAtOffset: true}, "empty at offset 2"},
		{"negative total", windowBytes(t, body, 1, 2), Request{Offset: 0, Limit: 2},
			SourceFacts{TotalLines: -1, TotalLinesKnown: true, StartsAtOffset: true}, "cannot be right"},
		{"whole file contradicts its own count", body, Request{Offset: 0, Limit: 2},
			SourceFacts{TotalLines: 99, TotalLinesKnown: false}, ""},
		{"empty window and unknown count", nil, Request{Offset: 0, Limit: 2},
			SourceFacts{TotalLinesKnown: false, StartsAtOffset: true}, "line count is unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := ReadTextFromBytes(test.data, test.req, Options{
				ByteBudget: 4096,
				Facts:      &test.facts,
			})
			if test.wantEr == "" {
				if err != nil {
					t.Fatalf("expected acceptance, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a rejection, got an answer")
			}
			if !strings.Contains(err.Error(), test.wantEr) {
				t.Fatalf("error %q does not name %q", err.Error(), test.wantEr)
			}
		})
	}
}

// TestUnknownCountPublishesNeitherEofNorTotal pins the conservative branch: an
// adapter that can produce a window but not a count gets content and nothing else.
// WindowMetadata must leave total_lines out rather than inventing one.
func TestUnknownCountPublishesNeitherEofNorTotal(t *testing.T) {
	body := "a\nb\nc\nd\ne\nf\ng\n"
	window := windowBytes(t, []byte(body), 3, 5)
	got, stats, err := ReadTextFromBytes(window, Request{Offset: 2, Limit: 3}, Options{
		ByteBudget: 4096,
		Facts:      &SourceFacts{StartsAtOffset: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "3: c\n4: d\n5: e" {
		t.Fatalf("window = %q", got)
	}
	if stats.EOF {
		t.Fatal("end-of-window is not end-of-source when the count is unknown")
	}
	metadata := WindowMetadata(Request{Offset: 2, Limit: 3}, stats)
	if _, present := metadata["total_lines"]; present {
		t.Fatalf("total_lines published although the source never said: %v", metadata["total_lines"])
	}
	if metadata["eof"] != false {
		t.Fatalf("metadata eof = %v, want false", metadata["eof"])
	}
}

// TestNoFactsKeepsTodaysBehaviour is the "no behaviour change for existing callers"
// evidence at the unit level; the untouched view suite is the rest of it.
func TestNoFactsKeepsTodaysBehaviour(t *testing.T) {
	body := []byte("one\ntwo\nthree\n")
	content, stats, err := ReadTextFromBytes(body, Request{Offset: 0, Limit: 5}, Options{ByteBudget: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if content != "1: one\n2: two\n3: three" {
		t.Fatalf("content = %q", content)
	}
	if !stats.EOF || !stats.TotalLinesKnown || stats.TotalLines != 3 {
		t.Fatalf("unflagged stats changed: %+v", stats)
	}
}

// TestEncodedWindowIsRefused covers the one encoded-file case the core can actually
// see: a window whose own bytes open with a BOM cannot be a mid-file slice of a
// decoded stream, so it is refused with the whole-file recovery route.
//
// The rest of the obligation is NOT here and the test says so plainly: a genuine
// mid-file UTF-16 window carries no BOM (the mark lives at byte 0 of the file), so no
// amount of core-side inspection can identify it. That is why the adapter must probe
// the file's head - four bytes through the seam's offset read - before it is allowed
// to choose the window path at all. Asserting a core-side guarantee that does not
// exist would be the worse failure.
func TestEncodedWindowIsRefused(t *testing.T) {
	utf16 := append([]byte{0xFF, 0xFE}, []byte("h\x00i\x00\nb\x00y\x00\n")...)

	// A window that starts with the mark at a non-zero offset: refused.
	_, _, err := ReadTextFromBytes(utf16, Request{Offset: 1, Limit: 1}, Options{
		ByteBudget: 4096,
		Facts:      &SourceFacts{TotalLines: 2, TotalLinesKnown: true, StartsAtOffset: true},
	})
	if err == nil {
		t.Fatal("a BOM-led window at offset 1 was accepted")
	}
	if !strings.Contains(err.Error(), "整份读取") {
		t.Fatalf("refusal does not name the recovery route: %v", err)
	}

	// Offset 0 of an encoded file stays legal: that is the whole-file path today.
	if _, _, err := ReadTextFromBytes(utf16, Request{Offset: 0, Limit: 5}, Options{
		ByteBudget: 4096,
		Facts:      &SourceFacts{TotalLines: 2, TotalLinesKnown: true, StartsAtOffset: true},
	}); err != nil {
		t.Fatalf("the whole-file encoded path regressed: %v", err)
	}

	// Documented limitation, not a desired outcome: the same bytes without the mark
	// are indistinguishable from UTF-8 text, so the core accepts them. The adapter's
	// head probe is the gate that keeps this out of a real request.
	bare := []byte("h\x00i\x00\nb\x00y\x00\n")
	if _, _, err := ReadTextFromBytes(bare, Request{Offset: 1, Limit: 1}, Options{
		ByteBudget: 4096,
		Facts:      &SourceFacts{TotalLines: 2, TotalLinesKnown: true, StartsAtOffset: true},
	}); err != nil {
		t.Fatalf("expected the core to accept an unmarked mid-file window (the adapter is the gate), got %v", err)
	}
}

// countLinesWholeFile is the core's own rule, read off the data: a final line
// without a newline still counts. It is the number SourceFacts must carry, and the
// one `wc -l` would get wrong on the unterminated fixtures.
func countLinesWholeFile(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	count := bytes.Count(body, []byte("\n"))
	if !bytes.HasSuffix(body, []byte("\n")) {
		count++
	}
	return count
}

func firstChunk(text string, count int) string {
	if len(text) <= count {
		return text
	}
	return text[:count] + "..."
}
