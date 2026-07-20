package ui

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const LargePasteCharThreshold = 1000

type PendingPaste struct {
	Placeholder string
	Text        string
	start       int
	end         int
}

type ComposerState struct {
	text               []rune
	pendingPastes      []PendingPaste
	largePasteCounters map[int]int
}

func NewComposerState() *ComposerState {
	return &ComposerState{}
}

func NormalizePastedText(text string) string {
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func (c *ComposerState) SetText(text string) {
	if c == nil {
		return
	}
	c.text = append(c.text[:0], []rune(text)...)
	c.reconcilePendingPasteRanges()
}

func (c *ComposerState) Text() string {
	if c == nil {
		return ""
	}
	return string(c.text)
}

func (c *ComposerState) PendingPastes() []PendingPaste {
	if c == nil || len(c.pendingPastes) == 0 {
		return nil
	}
	out := make([]PendingPaste, len(c.pendingPastes))
	copy(out, c.pendingPastes)
	return out
}

func (c *ComposerState) InsertTextAt(cursor int, text string) int {
	if c == nil || text == "" {
		return cursor
	}
	cursor = c.clampCursor(cursor)
	chars := []rune(text)
	c.text = append(c.text[:cursor], append(chars, c.text[cursor:]...)...)
	c.shiftPendingPastesForInsert(cursor, len(chars))
	return cursor + len(chars)
}

func (c *ComposerState) DeleteRange(start int, end int) int {
	if c == nil || len(c.text) == 0 {
		return 0
	}
	start, end = c.normalizeRange(start, end)
	if start == end {
		return start
	}
	c.text = append(c.text[:start], c.text[end:]...)
	c.shiftPendingPastesForDelete(start, end)
	return start
}

func (c *ComposerState) ReplaceText(text string) int {
	if c == nil {
		return 0
	}
	c.text = append(c.text[:0], []rune(text)...)
	c.pendingPastes = c.pendingPastes[:0]
	return len(c.text)
}

func (c *ComposerState) HandlePasteAt(cursor int, pasted string) int {
	if c == nil {
		return cursor
	}
	pasted = NormalizePastedText(pasted)
	if pasted == "" {
		return cursor
	}
	charCount := utf8.RuneCountInString(pasted)
	if charCount > LargePasteCharThreshold {
		placeholder := c.nextLargePastePlaceholder(pasted, charCount)
		cursor = c.clampCursor(cursor)
		placeholderLen := utf8.RuneCountInString(placeholder)
		c.pendingPastes = append(c.pendingPastes, PendingPaste{
			Placeholder: placeholder,
			Text:        pasted,
			start:       cursor,
			end:         cursor + placeholderLen,
		})
		return c.InsertTextAt(cursor, placeholder)
	}
	return c.InsertTextAt(cursor, pasted)
}

func (c *ComposerState) SubmitText() string {
	if c == nil {
		return ""
	}
	if len(c.pendingPastes) == 0 {
		return string(c.text)
	}
	pending := c.validPendingPastesInTextOrder()
	if len(pending) == 0 {
		return string(c.text)
	}
	var builder strings.Builder
	last := 0
	for _, paste := range pending {
		if paste.start < last || paste.end > len(c.text) {
			continue
		}
		builder.WriteString(string(c.text[last:paste.start]))
		builder.WriteString(paste.Text)
		last = paste.end
	}
	builder.WriteString(string(c.text[last:]))
	return builder.String()
}

func (c *ComposerState) ClearPendingPastes() {
	if c == nil {
		return
	}
	c.pendingPastes = nil
	c.largePasteCounters = nil
}

func (c *ComposerState) nextLargePastePlaceholder(pasted string, charCount int) string {
	lineCount := strings.Count(pasted, "\n") + 1
	base := fmt.Sprintf("[已粘贴 %d 字符 / %d 行]", charCount, lineCount)
	if c.largePasteCounters == nil {
		c.largePasteCounters = make(map[int]int)
	}
	c.largePasteCounters[charCount]++
	if c.largePasteCounters[charCount] == 1 {
		return base
	}
	return fmt.Sprintf("%s #%d", base, c.largePasteCounters[charCount])
}

func (c *ComposerState) clampCursor(cursor int) int {
	if c == nil {
		return 0
	}
	if cursor < 0 {
		return 0
	}
	if cursor > len(c.text) {
		return len(c.text)
	}
	return cursor
}

func (c *ComposerState) normalizeRange(start int, end int) (int, int) {
	if c == nil {
		return 0, 0
	}
	start = c.clampCursor(start)
	end = c.clampCursor(end)
	if end < start {
		start, end = end, start
	}
	return start, end
}

func (c *ComposerState) shiftPendingPastesForInsert(cursor int, count int) {
	if c == nil || count <= 0 || len(c.pendingPastes) == 0 {
		return
	}
	filtered := c.pendingPastes[:0]
	for _, pending := range c.pendingPastes {
		if pending.start < 0 || pending.end <= pending.start {
			continue
		}
		if cursor < pending.start {
			pending.start += count
			pending.end += count
		} else if cursor > pending.start && cursor < pending.end {
			continue
		} else if cursor == pending.start {
			if c.pendingPasteMatchesRange(pending.start+count, pending.end+count, pending.Placeholder) {
				pending.start += count
				pending.end += count
			} else if !c.pendingPasteMatchesRange(pending.start, pending.end, pending.Placeholder) {
				continue
			}
		}
		filtered = append(filtered, pending)
	}
	c.pendingPastes = filtered
}

func (c *ComposerState) shiftPendingPastesForDelete(start int, end int) {
	if c == nil || len(c.pendingPastes) == 0 || end <= start {
		return
	}
	removed := end - start
	filtered := c.pendingPastes[:0]
	for _, pending := range c.pendingPastes {
		if pending.end <= start {
			filtered = append(filtered, pending)
			continue
		}
		if pending.start >= end {
			pending.start -= removed
			pending.end -= removed
			filtered = append(filtered, pending)
			continue
		}
	}
	c.pendingPastes = filtered
}

func (c *ComposerState) reconcilePendingPasteRanges() {
	if c == nil || len(c.pendingPastes) == 0 {
		return
	}
	filtered := c.pendingPastes[:0]
	for _, pending := range c.pendingPastes {
		if c.pendingPasteMatchesRange(pending.start, pending.end, pending.Placeholder) {
			filtered = append(filtered, pending)
		}
	}
	c.pendingPastes = filtered
}

func (c *ComposerState) validPendingPastesInTextOrder() []PendingPaste {
	if c == nil || len(c.pendingPastes) == 0 {
		return nil
	}
	valid := make([]PendingPaste, 0, len(c.pendingPastes))
	for _, pending := range c.pendingPastes {
		if c.pendingPasteMatchesRange(pending.start, pending.end, pending.Placeholder) {
			valid = append(valid, pending)
		}
	}
	sort.SliceStable(valid, func(i, j int) bool {
		return valid[i].start < valid[j].start
	})
	return valid
}

func (c *ComposerState) pendingPasteMatchesRange(start int, end int, placeholder string) bool {
	if c == nil || placeholder == "" || start < 0 || end > len(c.text) || start >= end {
		return false
	}
	return string(c.text[start:end]) == placeholder
}
