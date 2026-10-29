//go:build ignore

// Command envd_linewindow_spike is the M0 spike of
// docs/plan/envd-view-line-window-design-20261028.md. It changes no production code
// and depends on no unbuilt feature: the questions it answers are answerable at the
// byte level, before the core grows a facts entry point.
//
//  1. AGREEMENT - is the line window that the target's own awk produces exactly the
//     byte slice the shared core would have windowed from the whole file? This is the
//     load-bearing claim: if awk's lines and the core's lines disagree on any sample,
//     the window path cannot feed the core and the design falls back to option D.
//     Also: does awk's END{NR} count match the core's rule (a final line without a
//     newline still counts, which is NOT what wc -l reports)?
//  2. COST - how many fetches and how many bytes does today's doubling-prefix rule
//     really move, versus the window path? The prefix rule is replayed faithfully
//     (same budget, same doubling, same ceiling, same "the core stopped on its own"
//     test) so the number is measured, not estimated.
//
// It runs ON THE TARGET as the unprivileged user, so awk/sed are the target's
// binaries and the files are the target's files. The window path's "bytes" are what
// would cross the wire; the transport itself is one envd process call, whose channel
// behaviour is already covered by envd_live_verify.
//
// Usage (on the target):
//
//	envd_linewindow_spike -dir /home/me/aicli-lw-spike -budget 4096
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
)

// ceiling mirrors envdViewFetchCeiling, which is deliberately equal to the core's own
// in-memory decode bound.
const defaultCeiling int64 = 8 << 20

var cleanupFuncs []func()

func runCleanups() {
	for index := len(cleanupFuncs) - 1; index >= 0; index-- {
		cleanupFuncs[index]()
	}
	cleanupFuncs = nil
}

func fail(check string, err error) {
	fmt.Printf("FAIL %s: %v\n", check, err)
	runCleanups()
	os.Exit(1)
}

type sample struct {
	name    string
	path    string
	body    []byte
	offsets []int
	limit   int
	// excludedByDesign marks encoded files: the window path must not serve them, so
	// their numbers are reported as evidence for the exclusion rather than asserted
	// as agreement.
	excludedByDesign bool
}

func main() {
	dir := flag.String("dir", "", "absolute directory on this machine to hold the fixtures")
	budget := flag.Int("budget", 4096, "the view byte budget to replay")
	ceiling := flag.Int64("ceiling", defaultCeiling, "the fetch ceiling to replay")
	flag.Parse()
	if *dir == "" {
		fail("usage", fmt.Errorf("-dir is required"))
	}
	defer runCleanups()
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fail("create the fixture directory", err)
	}
	cleanupFuncs = append(cleanupFuncs, func() { _ = os.RemoveAll(*dir) })
	if uid := os.Geteuid(); uid == 0 {
		fail("refusing to run as root", fmt.Errorf("uid 0 sees every directory and reads every file; the permission facts below would be meaningless"))
	}

	fmt.Println("=== environment ===")
	for _, tool := range []struct{ name, arg string }{{"awk", "--version"}, {"sed", "--version"}} {
		out, err := exec.Command(tool.name, tool.arg).CombinedOutput()
		if err != nil {
			fail(tool.name+" --version", err)
		}
		fmt.Printf("%s: %s\n", tool.name, firstLine(string(out)))
	}
	link, err := exec.Command("readlink", "-f", "/usr/bin/awk").CombinedOutput()
	if err == nil {
		fmt.Printf("awk resolves to: %s\n", strings.TrimSpace(string(link)))
	}

	samples := buildSamples(*dir)

	fmt.Printf("\n=== AGREEMENT: window bytes and line-count rule vs the core's own view of the whole file (%d samples) ===\n", len(samples))
	failures := 0
	for _, s := range samples {
		problems, notes := checkSample(s)
		if len(problems) == 0 {
			tag := "served"
			if s.excludedByDesign {
				tag = "excluded by design (encoded file)"
			}
			fmt.Printf("PASS %-22s %-8d bytes, %d offsets, %s%s\n", s.name, len(s.body), len(s.offsets), tag, notesSuffix(notes))
			continue
		}
		failures++
		fmt.Printf("FAIL %-22s\n", s.name)
		for _, problem := range problems {
			fmt.Printf("     %s\n", problem)
		}
	}
	if failures > 0 {
		fail("agreement", fmt.Errorf("%d of %d samples disagree; the window path cannot feed the core as designed", failures, len(samples)))
	}

	fmt.Printf("\n=== COST: today's doubling prefix vs one line window ===\n")
	costSamples := buildCostSamples(*dir, *budget)
	for _, s := range costSamples {
		for _, offset := range s.offsets {
			fetches, moved, served := prefixCost(s.path, offset, *budget, *ceiling)
			windowBytes, count, windowBytesN, err := runWindow(s.path, offset+1, offset+s.limit)
			if err != nil {
				fail("window path on "+s.name, err)
			}
			remoteWork := len(s.body)
			verdict := "served"
			if !served {
				verdict = "REFUSED at ceiling"
			}
			ratio := "n/a"
			if moved > 0 && windowBytesN > 0 {
				ratio = fmt.Sprintf("%.0fx less", float64(moved)/float64(windowBytesN))
			}
			fmt.Printf("%-14s offset=%-7d today: %2d fetches, %10d bytes (%s) | window: 1 fetch, %7d bytes, total_lines=%d | remote_work=%d | %s | len=%d\n",
				s.name, offset, fetches, moved, verdict, windowBytesN, count, remoteWork, ratio, len(windowBytes))
		}
	}

	fmt.Println("\n=== TRAP: the operand form awk would read as an assignment ===")
	checkEqualsTrap(*dir)
	fmt.Println("\nSPIKE COMPLETE")
}

// windowProgram is the exact command form the design proposes: the line window on
// stdout, the line count on stderr, one process, one file operand.
func windowProgram() string {
	return `NR>=a && NR<=b {print} END {print "LINECOUNT=" NR > "/dev/stderr"}`
}

func buildSamples(dir string) []sample {
	var builder strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&builder, "row %03d: payload\n", i)
	}
	plain := []byte(builder.String())
	noFinal := append(append([]byte{}, plain...), []byte("row 300: unterminated")...)
	crlf := []byte(strings.ReplaceAll(string(plain), "\n", "\r\n"))
	long := []byte(strings.Repeat("L", 4096) + "\ntail line\n")
	mk := func(name string, body []byte, offsets []int, excluded bool) sample {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			fail("write sample "+name, err)
		}
		return sample{name: name, path: path, body: body, offsets: offsets, limit: 5, excludedByDesign: excluded}
	}
	return []sample{
		mk("plain.txt", plain, []int{0, 1, 150, 297, 298, 299, 300, 500}, false),
		mk("no-final-newline.txt", noFinal, []int{0, 299, 300, 301}, false),
		mk("crlf.txt", crlf, []int{0, 100, 298, 299, 300}, false),
		mk("nul.bin", []byte("a\x00b\nsecond\x00\nthird\nrow3\nrow4\nrow5\nrow6\n"), []int{0, 1, 5}, false),
		mk("longline.txt", long, []int{0, 1}, false),
		mk("empty.txt", []byte(""), []int{0, 1}, false),
		mk("one-line.txt", []byte("only\n"), []int{0, 1}, false),
		mk("blank-lines.txt", []byte("\n\n\nx\n\n"), []int{0, 1, 4, 5}, false),
		mk("bom-utf8.txt", append([]byte{0xEF, 0xBB, 0xBF}, []byte("x\ny\nz\n")...), []int{0}, true),
		mk("utf16.txt", append([]byte{0xFF, 0xFE}, []byte("h\x00i\x00\x00\n")...), []int{0}, true),
	}
}

// checkSample compares awk's window against the byte slice the core would have taken
// from the whole file, and compares awk's count against the core's line rule.
func checkSample(s sample) ([]string, string) {
	var problems []string
	var notes []string
	totalRef := countLinesCoreRule(s.body)
	reterminated, beyondEOF := 0, 0
	for _, offset := range s.offsets {
		from, to := offset+1, offset+s.limit
		window, count, _, err := runWindow(s.path, from, to)
		if err != nil {
			problems = append(problems, fmt.Sprintf("offset %d: window error %v", offset, err))
			continue
		}
		if count != totalRef {
			problems = append(problems, fmt.Sprintf("offset %d: awk LINECOUNT=%d, the core's rule says %d", offset, count, totalRef))
		}
		want := sliceBytes(s.body, from, to)
		if !bytes.Equal(window, want) {
			// awk's `print` re-terminates every record, so an unterminated final line
			// comes back with a newline the file never had. The core's windower strips
			// terminators when it emits lines, so the rendered answer is the same - but
			// this is recorded, not assumed: M1's core-level differential is the place
			// that claim gets proven.
			if len(want) > 0 && !bytes.HasSuffix(want, []byte("\n")) && bytes.Equal(window, append(append([]byte{}, want...), '\n')) {
				reterminated++
				continue
			}
			problems = append(problems, fmt.Sprintf("offset %d: window bytes differ\n  awk : %x\n  want: %x", offset, window, want))
		}
		if from > count {
			beyondEOF++
		}
	}
	if reterminated > 0 {
		notes = append(notes, fmt.Sprintf("%d window(s) came back with awk's added final newline (core-insensitive, to be proven in M1)", reterminated))
	}
	if beyondEOF > 0 {
		notes = append(notes, fmt.Sprintf("%d offset(s) start past the reported total (the self-consistency check's job)", beyondEOF))
	}
	if s.excludedByDesign {
		notes = append(notes, "encoded file: byte agreement holds but the design still excludes it (a cut prefix would change the decoded text)")
	}
	return problems, strings.Join(notes, "; ")
}

// runWindow executes the proposed command. It returns the window bytes, the reported
// total line count, and the byte length of the window.
func runWindow(path string, from, to int) ([]byte, int, int, error) {
	if to < from {
		to = from
	}
	cmd := exec.Command("awk", "-v", "a="+strconv.Itoa(from), "-v", "b="+strconv.Itoa(to), windowProgram(), path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return nil, 0, 0, fmt.Errorf("start awk: %w", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return nil, 0, 0, fmt.Errorf("%w (stderr: %s)", err, firstChars(stderr.String(), 200))
		}
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, 0, 0, fmt.Errorf("awk timed out")
	}
	count := -1
	for _, line := range strings.Split(stderr.String(), "\n") {
		if strings.HasPrefix(line, "LINECOUNT=") {
			parsed, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "LINECOUNT=")))
			if parseErr != nil {
				return nil, 0, 0, fmt.Errorf("unparsable line count %q", line)
			}
			count = parsed
		}
	}
	if count < 0 {
		return nil, 0, 0, fmt.Errorf("no line count reported; stderr=%q", firstChars(stderr.String(), 200))
	}
	return stdout.Bytes(), count, stdout.Len(), nil
}

// prefixCost replays today's rule: read the first `want` bytes, ask the core whether
// it could serve the window from that prefix (whole file, or it stopped on its own
// budget), otherwise double. Returns fetches, bytes moved, and whether it serves.
func prefixCost(path string, offset, budget int, ceiling int64) (int, int64, bool) {
	size, err := fileSize(path)
	if err != nil {
		fail("stat "+path, err)
	}
	want := int64(budget)
	if want > size {
		want = size
	}
	fetches := 0
	var moved int64
	for {
		data, readErr := readPrefix(path, want)
		fetches++
		if readErr != nil {
			fail("prefix read: "+readErr.Error(), readErr)
		}
		moved += int64(len(data))
		complete := want >= size
		request := viewcore.Request{Offset: offset, Limit: 5}
		request.Offset, request.Limit = viewcore.Normalize(request.Offset, request.Limit)
		_, stats, coreErr := viewcore.ReadTextFromBytes(data, request, viewcore.Options{ByteBudget: budget})
		if coreErr != nil {
			return fetches, moved, false
		}
		if complete || stats.HasMore {
			return fetches, moved, true
		}
		if want >= ceiling || want >= size {
			return fetches, moved, false
		}
		want *= 2
		if want > size {
			want = size
		}
		if want > ceiling {
			want = ceiling
		}
	}
}

func buildCostSamples(dir string, budget int) []sample {
	// Three sizes that matter: one the budget can already cover cheaply, one the
	// ceiling can reach, and one it cannot (today's refusal case).
	sizes := []struct {
		name     string
		lines    int
		offsets  []int
		lineText string
	}{
		{name: "small-12kB", lines: 300, offsets: []int{0, 100, 295}, lineText: "row %03d: payload"},
		{name: "mid-4.9MB", lines: 120000, offsets: []int{0, 1000, 60000}, lineText: "row %06d: payload for paging checks"},
		{name: "big-12.3MB", lines: 300000, offsets: []int{0, 100000, 299990}, lineText: "%08d: " + strings.Repeat("x", 30)},
	}
	samples := make([]sample, 0, len(sizes))
	for _, spec := range sizes {
		var builder strings.Builder
		builder.Grow(spec.lines * 41)
		for i := 0; i < spec.lines; i++ {
			fmt.Fprintf(&builder, spec.lineText+"\n", i)
		}
		name := spec.name + ".txt"
		path := filepath.Join(dir, name)
		body := []byte(builder.String())
		if err := os.WriteFile(path, body, 0o600); err != nil {
			fail("write cost sample "+name, err)
		}
		samples = append(samples, sample{name: name, path: path, body: body, offsets: spec.offsets, limit: 5})
	}
	return samples
}

// checkEqualsTrap proves the design's absolute-path precondition is load-bearing,
// by trying to make awk read stdin instead of the file.
func checkEqualsTrap(dir string) {
	path := filepath.Join(dir, "a=b.txt")
	body := []byte("first\nsecond\nthird\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		fail("write the trap fixture", err)
	}
	want := []byte("first\nsecond\n")
	window, count, _, err := runWindow(path, 1, 2)
	if err != nil {
		fail("absolute operand with an embedded '='", err)
	}
	if !bytes.Equal(window, want) {
		fail("absolute operand with an embedded '='", fmt.Errorf("window=%q want=%q", window, want))
	}
	fmt.Printf("PASS absolute path containing '=': window %q total=%d (the assignment trap did not fire)\n", window, count)

	relative, err := filepath.Rel(dir, path)
	if err != nil {
		fail("relative form", err)
	}
	relWindow, relCount, _, relErr := runWindow(relative, 1, 2)
	if relErr != nil {
		fmt.Printf("note: relative operand errored outright: %v\n", relErr)
		return
	}
	if !bytes.Equal(relWindow, want) {
		fmt.Printf("CONFIRMED TRAP: relative operand %q gave window %q (total=%d) - awk read it as an assignment.\n"+
			"      The design's absolute-path precondition is not a style preference.\n", relative, relWindow, relCount)
		return
	}
	fmt.Printf("note: relative operand agreed on this awk build (%q); the precondition is kept anyway\n", relative)
}

// --- helpers, local so the spike adds no production surface ---

// sliceBytes returns the raw bytes of lines [from..to] (1-based, inclusive), which is
// exactly what the core would have seen for that window inside the whole file.
func sliceBytes(body []byte, from, to int) []byte {
	if from > to || from < 1 {
		return nil
	}
	line := 1
	start := -1
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
	// The final line may have no newline: it still ends the file.
	if start >= 0 {
		return body[start:]
	}
	return nil
}

// countLinesCoreRule is viewcore's rule, from view.go:63: a final line without a
// newline counts as a line. wc -l does not agree on that file, which is why the
// design forbids wc -l as a count source.
func countLinesCoreRule(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	count := bytes.Count(body, []byte("\n"))
	if !bytes.HasSuffix(body, []byte("\n")) {
		count++
	}
	return count
}

func readPrefix(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if limit <= 0 {
		return nil, nil
	}
	buffer := make([]byte, limit)
	read, readErr := io.ReadFull(file, buffer)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return nil, readErr
	}
	return buffer[:read], nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

func notesSuffix(notes string) string {
	if notes == "" {
		return ""
	}
	return " | " + notes
}

func firstChars(text string, count int) string {
	if len(text) <= count {
		return text
	}
	return text[:count] + "..."
}
