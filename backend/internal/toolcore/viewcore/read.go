package viewcore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/textcodec"
)

const (
	// DefaultLimit is the window size used when the caller omits limit.
	DefaultLimit = 400
	// MaxLimit is the hard cap for an explicitly requested line window. Beyond
	// it the model must page with offset; the result publishes
	// is_truncated/suggested_next_offset for exactly that continuation.
	MaxLimit = 2000

	// MaxLineChars bounds one rendered line. A longer line is cut with an honest
	// marker instead of a bare ellipsis, so the model can see that content was
	// hidden and how much of it.
	MaxLineChars = 2000

	// MaxDecodeBytes bounds in-memory decoding for BOM/UTF-16 files. Larger
	// files keep the streaming path instead of risking a multi-megabyte
	// allocation on every read.
	MaxDecodeBytes int64 = 8 << 20

	// DirPreviewLimit caps the shallow listing produced when view is pointed at
	// a directory instead of a file.
	DirPreviewLimit = 40
)

// longLineMarker replaces the hidden remainder of an over-limit line. It is
// honest by construction: the model sees that content was cut and how many
// characters remain hidden.
const longLineMarker = "…[line truncated: %d more chars]"

// ReaderClampedMarker replaces the hidden remainder of a line whose byte length
// exceeds ReaderLineCapBytes. Unlike longLineMarker it reports bytes (the drain
// path never decodes the hidden part), which is still a countable-loss notice.
const ReaderClampedMarker = "…[line truncated: showing first %d chars of a %d-byte line]"

// TruncReason names why a read window stopped short.
type TruncReason string

const (
	TruncatedByBytes TruncReason = "bytes"
	TruncatedByLines TruncReason = "lines"
)

// Request is one file window. The same shape backs file_path and every
// files[] entry, so single and batch reads share one code path. A negative
// Offset asks for the last |Offset| lines.
type Request struct {
	FilePath string
	Offset   int
	Limit    int
}

// Options carries the surface-owned inputs of a read: the byte budget this
// window may spend and the metric sink for truncation events. Truncated is
// called once per event; a nil reporter records nothing.
type Options struct {
	ByteBudget int
	Truncated  func(reason TruncReason)
	// Access is the file-I/O seam used to open the file. nil means the local
	// filesystem: an unconverted caller keeps its exact pre-seam behaviour, which
	// is what makes the move reviewable as "no behaviour change".
	Access toolcore.Access
	// Facts describes the source when the caller could not hand over the whole file.
	// nil means "nothing is known beyond these bytes", which is exactly today's
	// behaviour: every existing caller keeps the answer it always had.
	Facts *SourceFacts
}

// SourceFacts is the ONE channel through which an off-machine source may state a
// fact about itself, and it exists to stop a worse one: an adapter that fetches a
// line window cannot let the core believe "I ran out of bytes" means "the file ends
// here". The core's rule has always been that `eof` reports the end of the data it
// was given; publishing that about a window would claim an end nobody saw.
//
// It is deliberately not a `EOF bool`. A supplied answer has to be checkable against
// the window the core is holding, so the core derives `eof` from the count instead of
// being told it, and rejects the whole window when the two disagree.
type SourceFacts struct {
	// TotalLines is the line count of the WHOLE source, counted with this core's own
	// rule: a final line without a newline still counts. That is the rule GNU awk's
	// NR follows and NOT the one `wc -l` follows, and the difference is one line on
	// any file that does not end in a newline - which is why `wc -l` is forbidden as
	// a source for this number.
	TotalLines int
	// TotalLinesKnown is false when the source could not say: the core then publishes
	// no total_lines and no eof, rather than guessing either.
	TotalLinesKnown bool
	// StartsAtOffset says these bytes already begin at Request.Offset, so the core
	// must not skip lines to reach the window. A window from another machine has no
	// lines before it; skipping would consume the window, find the data short, and
	// report a perfectly valid deep read as "offset beyond the end of the file".
	StartsAtOffset bool
}

// Stats is everything the reader learned about one window. It is the input to
// both the result builder and the host-side bookkeeping (a read ledger or a
// dedup cache needs the same numbers the result publishes).
type Stats struct {
	TotalLines      int
	TotalLinesKnown bool
	LinesRead       int
	HasMore         bool
	EOF             bool
	// ByteBudgetApplied marks windows stopped by the byte budget rather than
	// the line limit, so callers can distinguish the truncation cause.
	ByteBudgetApplied bool
	// LongLinesTruncated counts lines cut by the in-line guard so the honest
	// truncation contract can surface how much was hidden.
	LongLinesTruncated int
	// HiddenBytes is the byte size of the hidden remainder of over-limit
	// in-line-truncated lines within this window.
	HiddenBytes int
	// OriginalBytes is the raw window byte size before the in-line guard.
	OriginalBytes int
	// Encoding is the detected on-disk text encoding used for this read.
	Encoding string
	// Tail marks a read requested with a negative offset (last N lines). When
	// set, WindowStart holds the resolved absolute first line.
	Tail bool
	// TailRequested is the requested tail length before clamping.
	TailRequested int
	// TailClamped marks a tail request clamped to MaxLimit.
	TailClamped bool
	// TailDroppedByBudget / TailDroppedByLimit count kept lines the byte budget
	// or the explicit limit removed from the front of the tail window.
	TailDroppedByBudget int
	TailDroppedByLimit  int
	// WindowStart is the resolved 0-based first line of the returned window
	// (equal to the requested offset for normal reads).
	WindowStart int
	// ReaderClampedLines counts lines whose byte length exceeded
	// ReaderLineCapBytes and were drained (counted, not buffered).
	ReaderClampedLines int
	ReaderClampedBytes int64
	// EmptyFile marks a zero-line file so callers can publish an explicit note
	// instead of an offset-beyond-EOF message.
	EmptyFile bool
}

// ReadText reads one already-authorized path and returns the rendered window
// plus its stats. BOM/UTF-16 files are decoded first; plain UTF-8 streams.
//
// It is the entry point for a caller that holds no accessor: the handler threads
// the seam through ReadTextFromHandle instead, so the context that governs the
// open is the caller's own.
func ReadText(path string, req Request, opts Options) (string, Stats, error) {
	access := opts.Access
	if access == nil {
		access = localio.Access
	}
	file, err := access.Open(context.Background(), path)
	if err != nil {
		return "", Stats{}, err
	}
	defer file.Close()

	return ReadTextFromHandle(file, req, opts)
}

// ReadTextFromHandle renders one window of an open file. It is ReadText with the
// I/O decision taken out, which is the whole point of the seam: the same paging,
// BOM and clamping rules apply to a handle from any backend, so a remote read
// cannot grow a second window implementation.
func ReadTextFromHandle(file toolcore.FileHandle, req Request, opts Options) (string, Stats, error) {
	if enc, hasBOM := probeEncoding(file); hasBOM {
		if info, statErr := file.Stat(); statErr == nil && info.Size() > MaxDecodeBytes {
			return "", Stats{}, fmt.Errorf(
				"文件使用 %s 编码且超过 %d 字节，view 未做整文件解码；请改用 shell/download 转换后查看",
				enc, MaxDecodeBytes,
			)
		}
		data, readErr := io.ReadAll(file)
		if readErr != nil {
			return "", Stats{}, readErr
		}
		// The BOM rule lives in one place: a surface that already holds the bytes
		// (a remote read over an RPC) must decode exactly like this one, or the
		// same file renders as two different texts depending on transport.
		return ReadTextFromBytes(data, req, opts)
	}

	return ReadWindow(file, req, opts)
}

// ReadWindowFromAccess opens one already-authorized path through the seam and
// windows it with the shared reader.
//
// The open lives here rather than inside ReadText so the caller's context governs
// the I/O: a handle that costs an HTTP round trip must die when the call is
// cancelled, and a core cannot honour that if it picks the filesystem itself.
// The returned error is the open error or the read error, undifferentiated, which
// is what lets the handler keep one classification block exactly as it was.
func ReadWindowFromAccess(ctx context.Context, access toolcore.Access, path string, req Request, budget int) (string, Stats, error) {
	if access == nil {
		access = localio.Access
	}
	file, err := access.Open(ctx, path)
	if err != nil {
		return "", Stats{}, err
	}
	defer file.Close()
	return ReadTextFromHandle(file, req, Options{ByteBudget: budget})
}

// ReadTextFromBytes renders one window of bytes that are already in memory,
// applying the same encoding rule as ReadText: a BOM means decode-then-window,
// plain UTF-8 streams line by line.
//
// It exists so a surface without a local path - the envd backend, which gets the
// bytes over HTTP - reuses this implementation instead of carrying its own idea
// of what a window is. Paging, clamping, numbering and the truncation accounting
// all stay in ReadWindow, which every view surface already shares.
func ReadTextFromBytes(data []byte, req Request, opts Options) (string, Stats, error) {
	if enc, hasBOM := detectBOM(data); hasBOM {
		if opts.Facts != nil && opts.Facts.StartsAtOffset && req.Offset > 0 {
			// An encoded file has to be decoded before it can be cut into lines: a
			// window that starts mid-file arrives as raw UTF-16 bytes whose line
			// boundaries are not where the source put them. Decoding a fragment would
			// render the same file as two different texts depending on transport, so
			// this is refused instead of approximated - encoded files are served whole.
			return "", Stats{}, fmt.Errorf(
				"文件使用 %s 编码，不能按行窗口取偏移 %d：编码文件必须先解码再开窗，切片会改变文本；请整份读取",
				enc, req.Offset,
			)
		}
		if int64(len(data)) > MaxDecodeBytes {
			return "", Stats{}, fmt.Errorf(
				"文件使用 %s 编码且超过 %d 字节，view 未做整文件解码；请改用 shell/download 转换后查看",
				enc, MaxDecodeBytes,
			)
		}
		text, actual := textcodec.Decode(data)
		content, stats, err := ReadWindow(strings.NewReader(text), req, opts)
		stats.Encoding = actual.String()
		return content, stats, err
	}
	return ReadWindow(bytes.NewReader(data), req, opts)
}

// detectBOM is the in-memory twin of probeEncoding, which needs a seekable file.
func detectBOM(data []byte) (textcodec.Encoding, bool) {
	head := data
	if len(head) > 4 {
		head = head[:4]
	}
	enc, _ := textcodec.DetectEncoding(head)
	return enc, enc.HasBOM()
}

// probeEncoding peeks the leading BOM and rewinds the reader.
func probeEncoding(file toolcore.FileHandle) (textcodec.Encoding, bool) {
	var head [4]byte
	n, _ := file.Read(head[:])
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return textcodec.UTF8, false
	}
	enc, _ := textcodec.DetectEncoding(head[:n])
	return enc, enc.HasBOM()
}

// ReadWindow reads one window from an already-opened stream. It is the single
// window implementation behind every view surface: plain files go through
// ReadText, and derived renders (a document or notebook converted to Markdown)
// reuse it so paging, clamping and accounting behave identically.
func ReadWindow(reader io.Reader, req Request, opts Options) (string, Stats, error) {
	br := NewLineReader(reader)
	stats := Stats{}
	req.Offset, req.Limit = Normalize(req.Offset, req.Limit)
	if opts.ByteBudget <= 0 {
		// The byte budget is a stop condition, not an optional extra: a surface
		// that forgets it must get the tool's own window, never a one-line window.
		opts.ByteBudget = DefaultByteBudget
	}
	if req.Offset < 0 {
		return readTailLines(br, -req.Offset, req.Limit, opts, stats)
	}

	// Skip to the requested offset.
	skipped := 0
	if opts.Facts != nil && opts.Facts.StartsAtOffset {
		// The bytes handed over already begin at the requested line. Skipping would
		// consume the window, run out, and then announce "offset beyond the end of
		// the file" for a deep read that is perfectly valid - the failure mode that
		// makes a remote window unusable unless this flag exists.
		skipped = req.Offset
	}
	for skipped < req.Offset {
		line, err := readLine(br)
		if err != nil {
			return "", stats, err
		}
		if !line.HasBytes {
			stats.EOF = true
			stats.TotalLinesKnown = true
			break
		}
		skipped++
		stats.TotalLines++
	}

	// Read up to limit lines, stopping early once the byte budget is spent.
	var lines []string
	readCount := 0
	accumulated := 0
	for readCount < req.Limit {
		line, err := readLine(br)
		if err != nil {
			return "", stats, err
		}
		if !line.HasBytes {
			break
		}
		stats.TotalLines++
		stats.OriginalBytes += int(line.ContentBytes) + 1 // + newline
		accountLineTruncation(&stats, line, opts)

		lineBytes := len(line.Text) + 12 // rendered prefix "<lineNum>: " + newline
		if readCount > 0 && accumulated+lineBytes > opts.ByteBudget {
			stats.HasMore = true
			stats.ByteBudgetApplied = true
			reportTruncation(opts, TruncatedByBytes)
			break
		}

		lines = append(lines, line.Text)
		accumulated += lineBytes
		readCount++
	}
	stats.LinesRead = readCount

	// Peek instead of consuming: a huge next line must not be drained just to
	// learn that it exists.
	if !stats.HasMore && readCount == req.Limit {
		hasMore, err := br.HasMore()
		if err != nil {
			return "", stats, err
		}
		if hasMore {
			stats.HasMore = true
			reportTruncation(opts, TruncatedByLines)
		}
	}
	if !stats.HasMore {
		stats.TotalLinesKnown = true
		stats.EOF = true
	}
	if opts.Facts != nil {
		// Reconciled before the empty-window branch below, because that branch's
		// wording quotes a line count: with a window the only honest count is the
		// source's, not the few lines that arrived.
		if factsErr := applySourceFacts(&stats, req, opts.Facts); factsErr != nil {
			return "", stats, factsErr
		}
	}

	if readCount == 0 {
		stats.EOF = true
		stats.TotalLinesKnown = true
		stats.WindowStart = req.Offset
		if stats.TotalLines == 0 {
			stats.EmptyFile = true
			return "Note: file is empty (0 lines).", stats, nil
		}
		last := stats.TotalLines - 1
		if req.Offset == stats.TotalLines {
			return fmt.Sprintf("Note: offset %d equals total lines %d; use offset %d to read the last line.", req.Offset, stats.TotalLines, last), stats, nil
		}
		return fmt.Sprintf("Note: offset %d is beyond the end of the file (%d lines); retry with a smaller offset (0..%d).", req.Offset, stats.TotalLines, last), stats, nil
	}

	stats.WindowStart = req.Offset
	return FormatContent(lines, req.Offset), stats, nil
}

// applySourceFacts reconciles what the scan saw with what the source stated, and it
// is the only place where Facts may change an answer.
//
// Two rules shape it. `eof` is derived, never copied: the core decides whether the
// delivered window reaches the stated end instead of trusting a boolean, so a source
// cannot assert an ending it never proved. And any disagreement is an error rather
// than a judgement call, because "believe the window and also believe the count" is
// exactly how a wrong completeness claim ships.
func applySourceFacts(stats *Stats, req Request, facts *SourceFacts) error {
	if !facts.StartsAtOffset {
		// The core was handed data it could read to the end, so its own count stands.
		// A contradicting fact means the source lied about the bytes it passed over;
		// say so rather than picking a winner.
		if facts.TotalLinesKnown && stats.TotalLinesKnown && facts.TotalLines != stats.TotalLines {
			return fmt.Errorf("source facts report %d total lines, the data itself holds %d", facts.TotalLines, stats.TotalLines)
		}
		return nil
	}

	if !facts.TotalLinesKnown {
		if stats.LinesRead == 0 {
			// An empty window with no count is not an answer at any offset: the core
			// cannot tell "past the end" from "the source would not say", and it
			// certainly cannot claim the file is empty.
			return fmt.Errorf("the window at offset %d is empty and the source's line count is unknown", req.Offset)
		}
		// End-of-data is not evidence about the end of the source, so publish neither
		// a total nor an eof. WindowMetadata omits total_lines when it is not known -
		// an absent field is a fact, a wrong one is not.
		// HasMore stays whatever the data proved: with no count there is nothing
		// better to judge continuation against, and this branch is a defensive path
		// (a real window caller has a count), not the intended route.
		stats.TotalLines = 0
		stats.TotalLinesKnown = false
		stats.EOF = false
		return nil
	}

	if facts.TotalLines < 0 {
		return fmt.Errorf("source facts report %d total lines, which cannot be right", facts.TotalLines)
	}
	// 1-based number of the last line this window actually delivered.
	lastLine := req.Offset + stats.LinesRead
	if stats.LinesRead > 0 && lastLine > facts.TotalLines {
		return fmt.Errorf("the window delivers line %d but the source says %d lines exist", lastLine, facts.TotalLines)
	}
	if stats.LinesRead == 0 && req.Offset < facts.TotalLines {
		return fmt.Errorf("the window is empty at offset %d but the source has %d lines", req.Offset, facts.TotalLines)
	}
	stats.TotalLines = facts.TotalLines
	stats.TotalLinesKnown = true
	// Continuation is widened by the source, never narrowed by the window. Without
	// this the two answers disagree on `is_truncated`: a whole-file read peeks past
	// the delivered limit and reports more lines exist, while a window cannot see
	// anything after its last byte and would report "nothing more" for a file that
	// plainly continues. Widening is also safe in the other direction - a budget-cut
	// window keeps the HasMore the core already proved from the data in hand.
	stats.HasMore = stats.HasMore || lastLine < facts.TotalLines
	// Derived, not supplied: eof means every line of the source has been delivered,
	// and that is a comparison between the window's last line and the count.
	stats.EOF = !stats.HasMore
	return nil
}

// readTailLines implements a negative offset: it streams the whole file while
// keeping only the last N lines (bounded memory), then trims the kept window
// from the front until it fits the byte budget - the tail must stay the true
// tail, so the oldest kept lines are the ones dropped.
func readTailLines(br *LineReader, requested, limit int, opts Options, stats Stats) (string, Stats, error) {
	stats.TailRequested = requested
	tailLines := requested
	if tailLines > MaxLimit {
		tailLines = MaxLimit
		stats.TailClamped = true
	}
	if tailLines < 1 {
		// -MinInt stays negative when negated (overflow), and 0/negative would
		// panic make(); a tail read keeps at least one line.
		tailLines = 1
	}
	ring := make([]string, tailLines)
	count := 0
	for {
		line, err := readLine(br)
		if err != nil {
			return "", stats, err
		}
		if !line.HasBytes {
			break
		}
		count++
		stats.OriginalBytes += int(line.ContentBytes) + 1
		accountLineTruncation(&stats, line, opts)
		// Slot for the 1-based line number count; orderedTailLines undoes this
		// rotation with count%capacity as the oldest-slot index.
		ring[(count-1)%tailLines] = line.Text
	}
	stats.TotalLines = count
	stats.TotalLinesKnown = true
	stats.EOF = true
	stats.Tail = true
	ordered := orderedTailLines(ring, count)
	kept := ordered
	if len(ordered) > 0 {
		kept = trimTailToByteBudget(ordered, opts.ByteBudget)
		stats.TailDroppedByBudget = len(ordered) - len(kept)
	}
	// An explicit limit also bounds a tail window: the newest lines are the
	// recovery-relevant ones, so the oldest kept lines are dropped.
	if limit > 0 && len(kept) > limit {
		stats.TailDroppedByLimit = len(kept) - limit
		kept = kept[len(kept)-limit:]
	}
	stats.WindowStart = count - len(kept)
	stats.LinesRead = len(kept)
	if len(kept) == 0 {
		stats.EmptyFile = true
		return "Note: file is empty (0 lines).", stats, nil
	}
	return FormatContent(kept, stats.WindowStart), stats, nil
}

// orderedTailLines returns the ring contents oldest-first. When the file has
// fewer lines than the ring capacity the prefix slice is already in order.
func orderedTailLines(ring []string, count int) []string {
	if count == 0 {
		return nil
	}
	capacity := len(ring)
	if count <= capacity {
		return ring[:count]
	}
	start := count % capacity
	ordered := make([]string, 0, capacity)
	ordered = append(ordered, ring[start:]...)
	ordered = append(ordered, ring[:start]...)
	return ordered
}

// trimTailToByteBudget drops the oldest lines until the remaining window fits
// the budget. The newest line is always kept so the result still carries the
// recovery-relevant end of the file.
func trimTailToByteBudget(lines []string, budget int) []string {
	start := len(lines)
	accumulated := 0
	for index := len(lines) - 1; index >= 0; index-- {
		lineBytes := len(lines[index]) + 12
		if start < len(lines) && accumulated+lineBytes > budget {
			break
		}
		accumulated += lineBytes
		start = index
	}
	return lines[start:]
}

// lineRead is one logical line produced by readLine. Text is already clamped to
// MaxLineChars; ContentBytes is the full on-disk line length (delimiter
// excluded) even when the hidden remainder was never buffered.
type lineRead struct {
	Text         string
	HasBytes     bool
	EOF          bool
	ContentBytes int64
	HiddenChars  int
	HiddenBytes  int64
	OverClamped  bool
}

// readLine streams one bounded line from the shared reader and applies the
// rune clamp and loss accounting.
func readLine(br *LineReader) (lineRead, error) {
	raw, err := br.Next()
	if err != nil {
		return lineRead{}, err
	}
	line := lineRead{
		HasBytes:     raw.HasBytes,
		EOF:          raw.EOF,
		ContentBytes: raw.ContentBytes,
		OverClamped:  raw.Truncated,
	}
	if !raw.HasBytes {
		return line, nil
	}

	text := string(raw.Bytes)
	if raw.Truncated {
		visible := ClampLinePrefix(text, MaxLineChars)
		line.Text = visible + fmt.Sprintf(ReaderClampedMarker, MaxLineChars, raw.ContentBytes)
		hidden := raw.ContentBytes - int64(len(visible))
		if hidden < 0 {
			hidden = 0
		}
		line.HiddenBytes = hidden
		return line, nil
	}
	if utf8.RuneCountInString(text) > MaxLineChars {
		visible, hiddenBytes := truncateLongLine(text)
		line.Text = visible
		line.HiddenChars = utf8.RuneCountInString(text) - MaxLineChars
		line.HiddenBytes = int64(hiddenBytes)
		return line, nil
	}
	line.Text = text
	return line, nil
}

// accountLineTruncation folds one line's clamp/drain loss into the window stats
// and the truncation metric.
func accountLineTruncation(stats *Stats, line lineRead, opts Options) {
	if line.OverClamped || line.HiddenChars > 0 {
		stats.LongLinesTruncated++
		stats.HiddenBytes += int(line.HiddenBytes)
		reportTruncation(opts, TruncatedByBytes)
	}
	if line.OverClamped {
		stats.ReaderClampedLines++
		stats.ReaderClampedBytes += line.HiddenBytes
	}
}

func reportTruncation(opts Options, reason TruncReason) {
	if opts.Truncated != nil {
		opts.Truncated(reason)
	}
}

// truncateLongLine keeps a rune-safe prefix of an over-limit line and appends
// the honest marker reporting the hidden rune count. It returns the visible
// text and the hidden byte count for loss accounting.
func truncateLongLine(line string) (visible string, hiddenBytes int) {
	runes := []rune(line)
	if len(runes) <= MaxLineChars {
		return line, 0
	}
	marker := fmt.Sprintf(longLineMarker, len(runes)-MaxLineChars)
	markerRunes := len([]rune(marker))
	prefixRunes := MaxLineChars - markerRunes
	if prefixRunes < 0 {
		prefixRunes = 0
	}
	visible = string(runes[:prefixRunes]) + marker
	return visible, len(line) - len(string(runes[:prefixRunes]))
}

// ClampLinePrefix returns the prefix of s up to max runes without splitting a
// rune. It walks rune starts only, so a 256 KiB over-cap prefix costs no
// full-string rune conversion.
func ClampLinePrefix(s string, max int) string {
	if max <= 0 {
		return ""
	}
	count := 0
	for index := range s {
		if count == max {
			return s[:index]
		}
		count++
	}
	return s
}

// FormatContent renders a window exactly as every surface has always done:
// "N: text" with no trailing newline and no header, so the content is the file
// the model asked for and nothing else.
func FormatContent(lines []string, offset int) string {
	if len(lines) == 0 {
		return ""
	}
	var output strings.Builder
	for index, line := range lines {
		fmt.Fprintf(&output, "%d: %s", offset+index+1, line)
		if index < len(lines)-1 {
			output.WriteByte('\n')
		}
	}
	return output.String()
}

// IsBinaryContent reports whether a rendered window is a binary file: more than
// 5% NUL bytes in the first 500 bytes.
func IsBinaryContent(content string) bool {
	if len(content) == 0 {
		return false
	}
	checkLen := 500
	if len(content) < checkLen {
		checkLen = len(content)
	}
	nullCount := 0
	for i := 0; i < checkLen; i++ {
		if content[i] == 0 {
			nullCount++
		}
	}
	return nullCount > checkLen/20
}

// DirectoryPreview returns a shallow directory listing for auto-heal when view
// is pointed at a directory instead of a file.
func DirectoryPreview(dirPath string) (string, error) {
	return directoryPreview(context.Background(), localio.Access, dirPath)
}

// directoryPreview is the seam-backed implementation: a listing is a filesystem
// read, so the surface that owns the accessor decides which machine is described.
func directoryPreview(ctx context.Context, access toolcore.Access, dirPath string) (string, error) {
	if access == nil {
		access = localio.Access
	}
	entries, err := access.ReadDir(ctx, dirPath)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += string(filepath.Separator)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	limit := DirPreviewLimit
	if len(names) < limit {
		limit = len(names)
	}
	for i := 0; i < limit; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d: %s", i+1, names[i])
	}
	if len(names) > DirPreviewLimit {
		fmt.Fprintf(&b, "\n... 省略 %d 项（共 %d）", len(names)-DirPreviewLimit, len(names))
	}
	if len(names) == 0 {
		return "(空目录)", nil
	}
	return b.String(), nil
}

// WindowMetadata returns the stable window fields shared by every view path.
// Plain text windows and the derived renders (documents, notebooks) must
// publish the same continuation fields, otherwise the model cannot tell a
// line-truncated window from a byte-budget one and tail reads lose their
// resolved offset.
func WindowMetadata(req Request, stats Stats) map[string]any {
	windowOffset := req.Offset
	windowLimit := req.Limit
	if stats.Tail {
		// Tail reads report the resolved absolute window, not the negative
		// request: continuation metadata must be usable as-is.
		windowOffset = stats.WindowStart
		windowLimit = stats.LinesRead
	}
	metadata := map[string]any{
		"offset":       windowOffset,
		"limit":        windowLimit,
		"lines_read":   stats.LinesRead,
		"eof":          stats.EOF,
		"is_truncated": stats.HasMore,
	}
	if stats.Tail {
		metadata["tail"] = true
		metadata["tail_lines"] = stats.LinesRead
	}
	if stats.EmptyFile {
		metadata["empty"] = true
	}
	if stats.ReaderClampedLines > 0 {
		metadata["reader_clamped_lines"] = stats.ReaderClampedLines
		metadata["reader_clamped_bytes"] = stats.ReaderClampedBytes
	}
	if stats.ByteBudgetApplied {
		metadata["byte_budget_applied"] = true
	}
	if stats.Encoding != "" && stats.Encoding != textcodec.UTF8.String() {
		metadata["encoding"] = stats.Encoding
	}
	// Honest-truncation contract: surface in-line loss instead of a silent
	// "...", so the model knows what it did not see.
	if stats.LongLinesTruncated > 0 {
		metadata["long_lines_truncated"] = stats.LongLinesTruncated
		metadata["hidden_bytes"] = stats.HiddenBytes
	}
	if stats.TotalLinesKnown {
		metadata["total_lines"] = stats.TotalLines
	}
	return metadata
}

// DeliveredWindow describes one delivered window in the terms a host read ledger
// needs. It sits next to WindowMetadata so the rule of what counts as a complete
// window exists once, for every surface.
func DeliveredWindow(req Request, stats Stats, size int64) toolcore.ReadWindow {
	windowOffset := req.Offset
	windowLimit := req.Limit
	if stats.Tail {
		// Tail reads report the resolved absolute window, not the negative
		// request, so a ledger entry can be used as a continuation as-is.
		windowOffset = stats.WindowStart
		windowLimit = stats.LinesRead
	}
	// TotalLines stays zero when the read never scanned to the end: the host must
	// be able to tell "0 lines" from "unknown", and a remembered window must not
	// invent a length its own result never published.
	totalLines := 0
	if stats.TotalLinesKnown {
		totalLines = stats.TotalLines
	}
	return toolcore.ReadWindow{
		Offset:             windowOffset,
		Limit:              windowLimit,
		LinesRead:          stats.LinesRead,
		TotalLines:         totalLines,
		EOF:                stats.EOF,
		More:               stats.HasMore,
		ByteBudgetApplied:  stats.ByteBudgetApplied,
		LongLinesTruncated: stats.LongLinesTruncated,
		Tail:               stats.Tail,
		Empty:              stats.EmptyFile,
		FullRead:           req.Offset == 0 && !stats.HasMore && !stats.ByteBudgetApplied && stats.LongLinesTruncated == 0,
	}
}

// BuildResult assembles the canonical single-file view result from one window:
// the window metadata, the file identity and the continuation advisories.
func BuildResult(absPath string, size int64, req Request, content string, stats Stats) *toolcore.Result {
	metadata := WindowMetadata(req, stats)
	metadata["file_path"] = absPath
	metadata["file_size"] = size
	result := &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    content,
		Metadata:   metadata,
	}
	AttachEfficiencyHints(result.Metadata, &result.Content, req, stats)
	return result
}

// AttachEfficiencyHints stamps continuation metadata and a soft advisory when a
// large leading window is truncated. This is non-blocking guidance only. It
// works on the metadata/content pair so the plain text path and the derived
// renders share one implementation.
func AttachEfficiencyHints(metadata map[string]any, content *string, req Request, stats Stats) {
	if metadata == nil || content == nil {
		return
	}
	// offset_resolved publishes the window the reader actually opened; for a
	// tail read that is the resolved absolute line, not the negative request.
	metadata["offset_resolved"] = stats.WindowStart
	if stats.Tail {
		// Tail windows are already the cheapest way to read a file's end, but a
		// clamped/cropped tail still needs the "read earlier" route: otherwise
		// the model has no way back into the part it never saw.
		if stats.TailClamped || stats.TailDroppedByBudget > 0 || stats.TailDroppedByLimit > 0 {
			if stats.TailClamped {
				metadata["tail_clamped"] = true
			}
			if stats.TailDroppedByBudget > 0 {
				metadata["tail_lines_dropped_by_budget"] = stats.TailDroppedByBudget
			}
			if stats.TailDroppedByLimit > 0 {
				metadata["tail_lines_dropped_by_limit"] = stats.TailDroppedByLimit
			}
			metadata["tail_window_start"] = stats.WindowStart
			if stats.WindowStart > 0 {
				earlier := stats.WindowStart - DefaultLimit
				if earlier < 0 {
					earlier = 0
				}
				metadata["suggested_earlier_offset"] = earlier
				advisory := fmt.Sprintf(
					"[efficiency] tail window starts at line %d (lines_read=%d); for earlier content use offset=%d limit<=%d.",
					stats.WindowStart+1, stats.LinesRead, earlier, DefaultLimit,
				)
				appendNote(content, advisory)
			}
		}
		return
	}
	if stats.HasMore {
		nextOffset := req.Offset + stats.LinesRead
		if nextOffset < req.Offset {
			nextOffset = req.Offset
		}
		metadata["suggested_next_offset"] = nextOffset
	}
	// Soft-warn when a leading window still truncated: default-size windows (or
	// byte-budget-stopped windows) need continuation guidance; explicit small
	// ranges are already efficient; EOF windows need no advisory.
	if !stats.HasMore || req.Offset != 0 {
		return
	}
	if req.Limit < DefaultLimit && !stats.ByteBudgetApplied {
		return
	}
	nextOffset, _ := metadata["suggested_next_offset"].(int)
	metadata["efficiency_advisory"] = "prefer_offset_limit"
	advisory := fmt.Sprintf(
		"[efficiency] File continues past this window (is_truncated=true, lines_read=%d). Prefer a smaller limit for large files, or continue with offset=%d limit<=%d. For multiple independent files, use view.files in one call instead of repeated full-file views.",
		stats.LinesRead,
		nextOffset,
		DefaultLimit,
	)
	appendNote(content, advisory)
}

// appendNote appends a bracketed note to the result content, creating content
// when the window itself was empty.
func appendNote(content *string, note string) {
	if content == nil {
		return
	}
	if strings.TrimSpace(*content) == "" {
		*content = note
		return
	}
	*content = strings.TrimRight(*content, "\n") + "\n\n" + note
}

// Normalize clamps a requested line limit into the supported range. A negative
// offset is meaningful (it asks for the last |offset| lines) and passes through
// untouched; readTailLines clamps the tail length it implies.
func Normalize(offset, limit int) (int, int) {
	if limit < 1 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return offset, limit
}
