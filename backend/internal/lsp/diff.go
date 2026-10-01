package lsp

import "unicode/utf8"

// spanEdit is a single contiguous replacement: old[start:oldEnd] is replaced
// by new[newStart:newEnd]. All offsets are byte offsets snapped to UTF-8 rune
// boundaries so the protocol position conversion never points into the middle
// of a rune.
type spanEdit struct {
	start    int
	oldEnd   int
	newStart int
	newEnd   int
}

// singleSpanEdit computes the minimal single-span diff between old and new.
// ok=false means the texts are identical or the boundaries cannot be expressed
// as one rune-aligned span (callers fall back to a full-range replacement).
func singleSpanEdit(old, new []byte) (spanEdit, bool) {
	prefix := 0
	for prefix < len(old) && prefix < len(new) && old[prefix] == new[prefix] {
		prefix++
	}
	// Never split a rune: back off to the start of the rune containing the
	// first differing byte.
	for prefix > 0 && prefix < len(old) && !utf8.RuneStart(old[prefix]) {
		prefix--
	}
	if prefix > len(new) {
		prefix = len(new)
	}

	suffix := 0
	for suffix < len(old)-prefix && suffix < len(new)-prefix &&
		old[len(old)-1-suffix] == new[len(new)-1-suffix] {
		suffix++
	}
	oldEnd := len(old) - suffix
	newEnd := len(new) - suffix
	// End offsets must also sit on rune boundaries.
	for oldEnd < len(old) && !utf8.RuneStart(old[oldEnd]) {
		oldEnd++
	}
	for newEnd < len(new) && !utf8.RuneStart(new[newEnd]) {
		newEnd++
	}
	if prefix > oldEnd || prefix > newEnd {
		return spanEdit{}, false
	}
	if prefix == oldEnd && prefix == newEnd {
		return spanEdit{}, false
	}
	return spanEdit{start: prefix, oldEnd: oldEnd, newStart: prefix, newEnd: newEnd}, true
}
