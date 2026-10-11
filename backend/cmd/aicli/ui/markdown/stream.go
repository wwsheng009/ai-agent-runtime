package markdown

import (
	"strings"
	"unicode/utf8"
)

// StreamCollector splits streaming Markdown into a stable prefix that is safe
// to format/render and a holdback suffix that may still change (open fence,
// incomplete table, bare heading lead, etc.).
//
// Design goals:
//   - never re-emit already-stable content as duplicate scrollback
//   - hold incomplete structural units until closed or Finalize
//   - pure text logic; callers run Format/Render on Stable() only
type StreamCollector struct {
	raw    strings.Builder
	stable int // byte offset into raw that is committed stable
	// P2-4（docs/plan/aicli-stream-pipeline-performance-audit-and-optimization-plan-20261011.md §4）：
	// 开放栅栏的增量扫描状态。fenceScanOffset 是已扫完最后一行之后的位置
	// （下一未扫行起点，绝对偏移）；开放栅栏存在时每个 structural delta 只扫
	// 新增完整行，不再从栅栏起点重扫整个 holdback 尾部（长代码块流式的
	// O(n²) 来源）。语义参照 stableMarkdownCut（保持全量实现，测试对照用）。
	fenceScanOffset int
	// fenceOpen 用独立布尔哨兵而非 fenceOpenAt=-1：零值 StreamCollector
	// （var c StreamCollector）必须直接可用，int 零值 0 会被误读为
	// 「栅栏在偏移 0 开启」。
	fenceOpen       bool
	fenceOpenAt     int
	fenceMarker     byte
	fenceOpenLength int
}

// Reset clears collector state.
func (c *StreamCollector) Reset() {
	if c == nil {
		return
	}
	c.raw.Reset()
	c.stable = 0
	c.resetFenceScanState()
}

// Push appends a delta (or full snapshot suffix) and recomputes the stable cut.
// Returns newly stable text since the previous cut (may be empty).
func (c *StreamCollector) Push(delta string) (newlyStable string) {
	if c == nil || delta == "" {
		return ""
	}
	prevStable := c.stable
	before := c.raw.String()
	previousLen := len(before)
	c.raw.WriteString(delta)
	if canExtendStablePlainText(before, previousLen, prevStable, delta) {
		c.stable = c.raw.Len()
	} else if shouldRescanMarkdownDelta(previousLen, delta) {
		c.recompute()
	}
	if c.stable <= prevStable {
		return ""
	}
	all := c.raw.String()
	return all[prevStable:c.stable]
}

func canExtendStablePlainText(before string, previousLen, stable int, delta string) bool {
	if previousLen < 64 || stable != previousLen || markdownStructuralDelta(delta) {
		return false
	}
	return previousLen > 0 && before[previousLen-1] != '\n' && before[previousLen-1] != '\r'
}

func shouldRescanMarkdownDelta(previousLen int, delta string) bool {
	return previousLen < 64 || markdownStructuralDelta(delta)
}

func markdownStructuralDelta(delta string) bool {
	return strings.ContainsAny(delta, "\r\n`~|#>*+-[]()")
}

// SetContent replaces the buffer (snapshot-style streams) and returns newly stable.
func (c *StreamCollector) SetContent(content string) (newlyStable string) {
	if c == nil {
		return ""
	}
	prevStable := c.stable
	prev := c.raw.String()
	if content == prev {
		c.recompute()
		if c.stable <= prevStable {
			return ""
		}
		return c.raw.String()[prevStable:c.stable]
	}
	// Prefer append when content extends previous.
	if strings.HasPrefix(content, prev) {
		return c.Push(content[len(prev):])
	}
	c.raw.Reset()
	c.stable = 0
	c.resetFenceScanState()
	c.raw.WriteString(content)
	c.recompute()
	if c.stable == 0 {
		return ""
	}
	return c.raw.String()[:c.stable]
}

// Stable returns the committed stable prefix.
func (c *StreamCollector) Stable() string {
	if c == nil || c.stable <= 0 {
		return ""
	}
	return c.raw.String()[:c.stable]
}

// Holdback returns the unstable tail.
func (c *StreamCollector) Holdback() string {
	if c == nil {
		return ""
	}
	all := c.raw.String()
	if c.stable >= len(all) {
		return ""
	}
	return all[c.stable:]
}

// Raw returns the full accumulated content.
func (c *StreamCollector) Raw() string {
	if c == nil {
		return ""
	}
	return c.raw.String()
}

// Finalize releases the entire buffer as stable (end of stream).
func (c *StreamCollector) Finalize() string {
	if c == nil {
		return ""
	}
	all := c.raw.String()
	prev := c.stable
	c.stable = len(all)
	if prev >= len(all) {
		return ""
	}
	return all[prev:]
}

func (c *StreamCollector) recompute() {
	all := c.raw.String()
	base := c.stable
	if base > len(all) {
		base = len(all)
	}
	// 稳定点之前的区域已经闭合：无开放栅栏/表格，边界为行首，且 partial-lead
	// 判定只看尾部行。因此 stableMarkdownCut 的每个分支在 all[base:] 上都与
	// 全量扫描等价（stableMarkdownCut(all[base:]) + base）。流式路径借此把
	// 每帧的稳定切分扫描限制在「上一稳定点之后的尾部」，而不是整段源。
	cut := base + c.stableCutIncremental(all, base)
	if cut < c.stable {
		// Stable region never shrinks during a stream (avoids flicker/rewrites
		// of already-emitted transcript). Incomplete earlier structures stay
		// held until Finalize.
		return
	}
	c.stable = cut
}

// stableMarkdownCut returns the largest byte offset such that all[:cut] is safe
// to format without depending on incomplete trailing structure.
func stableMarkdownCut(src string) int {
	if src == "" {
		return 0
	}
	// Hold everything while an open fenced code block is unfinished. The
	// opening byte offset comes from the same stateful scan that validates the
	// closing marker, so mismatched marker kinds or shorter runs cannot release
	// mutable code into scrollback.
	if openAt := openMarkdownFenceStart(src); openAt >= 0 {
		return openAt
	}
	// Hold incomplete table (header without separator, or trailing partial row).
	if cut, hold := tableHoldbackCut(src); hold {
		return cut
	}
	// Hold trailing partial line that looks like markdown lead (#- , | , ``` , - ).
	if cut := partialLeadHoldback(src); cut >= 0 {
		return cut
	}
	// Default: everything up to last complete line (keep final partial line).
	if i := strings.LastIndexByte(src, '\n'); i >= 0 {
		return i + 1
	}
	// Single partial line without newline: hold entirely unless plain prose long enough.
	if looksStructuralLead(src) {
		return 0
	}
	// Short plain text: hold to avoid one-char flash; long prose can stream.
	if utf8.RuneCountInString(src) < 24 && !strings.ContainsAny(src, "。.!?;") {
		return 0
	}
	return len(src)
}

// resetFenceScanState 清零增量栅栏扫描状态（Reset / SetContent 整体替换时）。
func (c *StreamCollector) resetFenceScanState() {
	c.fenceScanOffset = 0
	c.fenceOpen = false
	c.fenceOpenAt = 0
	c.fenceMarker = 0
	c.fenceOpenLength = 0
}

// stableCutIncremental 与 stableMarkdownCut(all[base:]) 等价，但开放栅栏检测
// 增量执行：只扫 fenceScanOffset 之后新增的完整行 + O(行长) 的尾部 partial
// 行 opener 补检；重锚（稳定点越过已扫位置，即栅栏闭合后稳定点前进，或
// 整列表重置后越界）时清状态并从 base 重建。表格 / partial-lead / 默认切分
// 与原实现逐分支一致（它们天然只作用于从稳定点起的小尾部）。
func (c *StreamCollector) stableCutIncremental(all string, base int) int {
	src := all[base:]
	if src == "" {
		return 0
	}
	if c.fenceScanOffset < base || c.fenceScanOffset > len(all) {
		c.resetFenceScanState()
		c.fenceScanOffset = base
	}
	c.advanceFenceScan(all)
	if c.fenceOpen {
		return c.fenceOpenAt - base
	}
	if cut, hold := tableHoldbackCut(src); hold {
		return cut
	}
	if cut := partialLeadHoldback(src); cut >= 0 {
		return cut
	}
	if i := strings.LastIndexByte(src, '\n'); i >= 0 {
		return i + 1
	}
	if looksStructuralLead(src) {
		return 0
	}
	if utf8.RuneCountInString(src) < 24 && !strings.ContainsAny(src, "。.!?;") {
		return 0
	}
	return len(src)
}

// advanceFenceScan 从 fenceScanOffset 扫描到 all 的最后一个换行为止的完整行，
// 维护开放栅栏状态；partial 尾行留待其完成后扫描，另做 O(行长) opener 补检。
func (c *StreamCollector) advanceFenceScan(all string) {
	lastNewline := strings.LastIndexByte(all, '\n')
	scanEnd := lastNewline + 1
	if scanEnd < c.fenceScanOffset {
		scanEnd = c.fenceScanOffset
	}
	for offset := c.fenceScanOffset; offset < scanEnd; {
		lineEnd := strings.IndexByte(all[offset:scanEnd], '\n')
		if lineEnd < 0 {
			break
		}
		lineEnd += offset
		line := strings.TrimSuffix(all[offset:lineEnd], "\r")
		marker, runLength, rest, ok := markdownFenceRun(line)
		if ok {
			switch {
			case c.fenceOpen && offset == c.fenceOpenAt:
				// opener 行由 partial 补检注册后在本轮补全：以完整行刷新
				// marker/长度（更长的反引号串抬高闭合门槛），不作闭合判定。
				c.fenceMarker = marker
				c.fenceOpenLength = runLength
			case !c.fenceOpen:
				if marker != '`' || !strings.Contains(rest, "`") {
					c.fenceOpen = true
					c.fenceOpenAt = offset
					c.fenceMarker = marker
					c.fenceOpenLength = runLength
				}
			case marker == c.fenceMarker && runLength >= c.fenceOpenLength && strings.TrimSpace(rest) == "":
				c.fenceOpen = false
				c.fenceMarker = 0
				c.fenceOpenLength = 0
			}
		}
		offset = lineEnd + 1
		c.fenceScanOffset = offset
	}
	// 尾部 partial 行 opener 补检：全量参照扫描包含 partial 行，增量路径用
	// O(行长) 补齐，保证 ~~~ 等不在 partial-lead 白名单内的 opener 不漏判。
	if !c.fenceOpen && lastNewline+1 < len(all) {
		line := strings.TrimSuffix(all[lastNewline+1:], "\r")
		if marker, runLength, rest, ok := markdownFenceRun(line); ok &&
			(marker != '`' || !strings.Contains(rest, "`")) {
			c.fenceOpen = true
			c.fenceOpenAt = lastNewline + 1
			c.fenceMarker = marker
			c.fenceOpenLength = runLength
		}
	}
}

func openMarkdownFenceStart(src string) int {
	openAt := -1
	var openMarker byte
	openLength := 0
	for offset := 0; offset <= len(src); {
		lineEnd := strings.IndexByte(src[offset:], '\n')
		next := len(src) + 1
		if lineEnd < 0 {
			lineEnd = len(src)
		} else {
			lineEnd += offset
			next = lineEnd + 1
		}
		line := strings.TrimSuffix(src[offset:lineEnd], "\r")
		marker, runLength, rest, ok := markdownFenceRun(line)
		if ok {
			if openMarker == 0 {
				// Backtick fence info strings cannot themselves contain a
				// backtick. Treating such a line as an opener would diverge
				// from Goldmark and hold ordinary prose indefinitely.
				if marker != '`' || !strings.Contains(rest, "`") {
					openAt = offset
					openMarker = marker
					openLength = runLength
				}
			} else if marker == openMarker && runLength >= openLength && strings.TrimSpace(rest) == "" {
				openAt = -1
				openMarker = 0
				openLength = 0
			}
		}
		if next > len(src) {
			break
		}
		offset = next
	}
	return openAt
}

func markdownFenceRun(line string) (marker byte, runLength int, rest string, ok bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent >= len(line) {
		return 0, 0, "", false
	}
	line = stripMarkdownBlockquotePrefix(line[indent:])
	if line == "" {
		return 0, 0, "", false
	}
	marker = line[0]
	if marker != '`' && marker != '~' {
		return 0, 0, "", false
	}
	end := 0
	for end < len(line) && line[end] == marker {
		end++
	}
	if end < 3 {
		return 0, 0, "", false
	}
	return marker, end, line[end:], true
}

func tableHoldbackCut(src string) (cut int, hold bool) {
	lines := strings.Split(src, "\n")
	offsets := make([]int, len(lines))
	for i := 1; i < len(lines); i++ {
		offsets[i] = offsets[i-1] + len(lines[i-1]) + 1
	}

	for i := 0; i < len(lines); i++ {
		if !isTableRowCandidate(lines[i]) {
			continue
		}

		// A trailing pipe row may be a header whose delimiter row has not
		// arrived yet. Keep it mutable so the renderer never commits the header
		// as a paragraph and then has to rewrite it as a table.
		if i == len(lines)-1 || (i == len(lines)-2 && lines[i+1] == "" && strings.HasSuffix(src, "\n")) {
			return offsets[i], true
		}

		if !isTableSeparator(lines[i+1]) {
			// An unfinished delimiter row can still become valid with the next
			// delta. A newline proves it is ordinary text and releases the header.
			if i+1 == len(lines)-1 && !strings.HasSuffix(src, "\n") {
				return offsets[i], true
			}
			continue
		}

		j := i + 2
		for j < len(lines) && isTableRowCandidate(lines[j]) {
			j++
		}
		if j >= len(lines) ||
			(j == len(lines)-1 && lines[j] == "" && strings.HasSuffix(src, "\n")) ||
			(j == len(lines)-1 && !strings.HasSuffix(src, "\n")) {
			// Once recognized, the complete table remains one mutable tail until
			// a blank or non-table line closes it, matching Codex's holdback model.
			return offsets[i], true
		}
		// This table is closed. Skip its body, but continue scanning because a
		// later table may itself be the current mutable tail.
		if j > i+2 {
			i = j - 1
		}
	}
	return 0, false
}

func isTableRowCandidate(line string) bool {
	segments, ok := markdownTableSegments(stripMarkdownBlockquotePrefix(line))
	if !ok {
		return false
	}
	for _, segment := range segments {
		if strings.TrimSpace(segment) != "" {
			return true
		}
	}
	return false
}

func isTableSeparator(line string) bool {
	segments, ok := markdownTableSegments(stripMarkdownBlockquotePrefix(line))
	if !ok || len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		segment = strings.TrimPrefix(segment, ":")
		segment = strings.TrimSuffix(segment, ":")
		if len(segment) < 3 || strings.Trim(segment, "-") != "" {
			return false
		}
	}
	return true
}

func markdownTableSegments(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}
	hasOuterPipe := strings.HasPrefix(line, "|") || strings.HasSuffix(line, "|")
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	segments := make([]string, 0, strings.Count(line, "|")+1)
	start := 0
	for index := 0; index < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		if line[index] == '|' {
			segments = append(segments, line[start:index])
			start = index + 1
		}
	}
	segments = append(segments, line[start:])
	if !hasOuterPipe && len(segments) <= 1 {
		return nil, false
	}
	return segments, true
}

func stripMarkdownBlockquotePrefix(line string) string {
	rest := strings.TrimLeft(line, " \t")
	for strings.HasPrefix(rest, ">") {
		rest = strings.TrimLeft(strings.TrimPrefix(rest, ">"), " \t")
	}
	return rest
}

func partialLeadHoldback(src string) int {
	if strings.HasSuffix(src, "\n") {
		return -1
	}
	i := strings.LastIndexByte(src, '\n')
	var partial string
	var cut int
	if i < 0 {
		partial = src
		cut = 0
	} else {
		partial = src[i+1:]
		cut = i + 1
	}
	if looksStructuralLead(partial) {
		return cut
	}
	return -1
}

func looksStructuralLead(s string) bool {
	trim := strings.TrimLeft(s, " \t")
	if trim == "" {
		return false
	}
	if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
		return true
	}
	if strings.HasPrefix(trim, "|") {
		return true
	}
	if strings.HasPrefix(trim, "#") {
		return true
	}
	if strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* ") || strings.HasPrefix(trim, "+ ") {
		// Incomplete list item without much content: hold.
		return !strings.Contains(trim, "\n") && utf8.RuneCountInString(trim) < 8
	}
	return false
}
