package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const LargePasteCharThreshold = 1000

type PendingPaste struct {
	Placeholder string
	Text        string
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
	c.prunePendingPastes(text)
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
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(c.text) {
		cursor = len(c.text)
	}
	chars := []rune(text)
	c.text = append(c.text[:cursor], append(chars, c.text[cursor:]...)...)
	return cursor + len(chars)
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
		placeholder := c.nextLargePastePlaceholder(charCount)
		c.pendingPastes = append(c.pendingPastes, PendingPaste{
			Placeholder: placeholder,
			Text:        pasted,
		})
		return c.InsertTextAt(cursor, placeholder)
	}
	return c.InsertTextAt(cursor, pasted)
}

func (c *ComposerState) SubmitText() string {
	if c == nil {
		return ""
	}
	text := string(c.text)
	for i := len(c.pendingPastes) - 1; i >= 0; i-- {
		pending := c.pendingPastes[i]
		if pending.Placeholder == "" {
			continue
		}
		text = strings.ReplaceAll(text, pending.Placeholder, pending.Text)
	}
	return text
}

func (c *ComposerState) ClearPendingPastes() {
	if c == nil {
		return
	}
	c.pendingPastes = nil
	c.largePasteCounters = nil
}

func (c *ComposerState) prunePendingPastes(text string) {
	if c == nil || len(c.pendingPastes) == 0 {
		return
	}
	filtered := c.pendingPastes[:0]
	for _, pending := range c.pendingPastes {
		if pending.Placeholder != "" && strings.Contains(text, pending.Placeholder) {
			filtered = append(filtered, pending)
		}
	}
	c.pendingPastes = filtered
}

func (c *ComposerState) nextLargePastePlaceholder(charCount int) string {
	base := fmt.Sprintf("[Pasted Content %d chars]", charCount)
	if c.largePasteCounters == nil {
		c.largePasteCounters = make(map[int]int)
	}
	c.largePasteCounters[charCount]++
	if c.largePasteCounters[charCount] == 1 {
		return base
	}
	return fmt.Sprintf("%s #%d", base, c.largePasteCounters[charCount])
}
