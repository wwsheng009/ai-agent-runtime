// Package tokenestimate provides the dependency-free fallback token estimator
// used whenever a provider does not report usage.
//
// Provider-reported usage (prompt_tokens / input_tokens) always wins when it is
// available; this estimator is the fallback for prompt gates, context snapshots
// and usage back-fill.
//
// The model is a pretokenization-class heuristic: text is split into runs
// (word runs, digit runs, symbol runs, whitespace runs, script runs) and each
// run is priced with the tokens-per-rune ratio measured for the target
// encoding. Constants were calibrated (2026-10) against real byte-pair
// tokenizers over mixed corpora — English, Chinese, Japanese, Korean,
// Cyrillic, Vietnamese, Arabic, Thai, Hindi, source code, JSON tool schemas,
// markdown, file paths, digits and emoji:
//
//   - OpenAI cl100k_base / o200k_base / p50k_base via tiktoken 0.14
//     (github.com/openai/tiktoken).
//   - Anthropic's public tokenizer via @anthropic-ai/tokenizer.
//   - Chat message overhead follows the OpenAI cookbook formula
//     (3 tokens per message, 1 per name, 3 reply priming), verified with
//     gpt-tokenizer's encodeChat.
//
// Pure-Go ports of the same encoders exist (e.g. github.com/pkoukk/tiktoken-go
// with an offline BPE loader), but they ship multi-megabyte BPE rank tables;
// this package trades exactness for zero dependencies and binary size, and is
// accurate enough for gates and snapshots (mean relative error ~13% on the
// calibration corpus, worst cases are rare CJK and unusual scripts).
package tokenestimate

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Profile carries the per-encoding constants of the heuristic. Values are
// integer (per-mille for dense scripts, divisors for runs) so estimates are
// deterministic across platforms.
type Profile struct {
	Name string

	// WordDivisor prices a Latin/ASCII letter run: round(runes / WordDivisor).
	WordDivisor int
	// DigitDivisor prices a digit run: ceil(runes / DigitDivisor).
	DigitDivisor int
	// SymbolSameDivisor prices a symbol run of one repeated character.
	SymbolSameDivisor int
	// SymbolMixedDivisor prices a symbol run mixing different characters.
	SymbolMixedDivisor int
	// SpaceDivisor prices extra spaces beyond the first (a single leading
	// space merges into the following word token).
	SpaceDivisor int
	// NewlineDivisor prices a newline run.
	NewlineDivisor int

	// Dense scripts: tokens per 1000 runes.
	HanPerMille           int
	KanaPerMille          int
	HangulPerMille        int
	LatinExtPerMille      int
	CyrillicGreekPerMille int
	OtherLetterPerMille   int
	EmojiPerMille         int

	// Chat message framing.
	MessageOverhead int
	NameOverhead    int
	ReplyPriming    int
}

// Calibrated profiles. Measured tokens-per-rune references (common text):
//
//	English prose      cl100k 0.18  o200k 0.18  anthropic 0.18
//	Chinese prose      cl100k 1.15  o200k 0.76  anthropic 0.95
//	Japanese           cl100k 0.93  o200k 0.72  anthropic ~0.95
//	Korean             cl100k 0.94  o200k 0.66
//	Vietnamese         cl100k 0.55  o200k 0.28
//	Cyrillic           cl100k 0.38  o200k 0.21  anthropic 0.47
//	Arabic/Thai/Hindi  cl100k 0.74-0.98  o200k 0.32-0.37
//	Emoji              cl100k 2.64  o200k 1.57  anthropic 2.14
//	Digits             ~1 token per 3 digits in all encodings.
var (
	// ProfileO200k targets modern OpenAI models (gpt-4o/4.1/4.5, gpt-5,
	// o-series, gpt-oss): the o200k_base encoding.
	ProfileO200k = Profile{
		Name:                  "o200k",
		WordDivisor:           6,
		DigitDivisor:          3,
		SymbolSameDivisor:     8,
		SymbolMixedDivisor:    3,
		SpaceDivisor:          48,
		NewlineDivisor:        8,
		HanPerMille:           850,
		KanaPerMille:          720,
		HangulPerMille:        680,
		LatinExtPerMille:      280,
		CyrillicGreekPerMille: 220,
		OtherLetterPerMille:   350,
		EmojiPerMille:         1600,
		MessageOverhead:       3,
		NameOverhead:          1,
		ReplyPriming:          3,
	}
	// ProfileCL100k targets legacy OpenAI models (gpt-4, gpt-3.5-turbo,
	// text-embedding-ada-002): the cl100k_base encoding.
	ProfileCL100k = Profile{
		Name:                  "cl100k",
		WordDivisor:           6,
		DigitDivisor:          3,
		SymbolSameDivisor:     8,
		SymbolMixedDivisor:    3,
		SpaceDivisor:          48,
		NewlineDivisor:        8,
		HanPerMille:           1300,
		KanaPerMille:          950,
		HangulPerMille:        950,
		LatinExtPerMille:      550,
		CyrillicGreekPerMille: 350,
		OtherLetterPerMille:   1000,
		EmojiPerMille:         2600,
		MessageOverhead:       3,
		NameOverhead:          1,
		ReplyPriming:          3,
	}
	// ProfileAnthropic targets Claude models (Anthropic's public tokenizer).
	ProfileAnthropic = Profile{
		Name:                  "anthropic",
		WordDivisor:           6,
		DigitDivisor:          3,
		SymbolSameDivisor:     8,
		SymbolMixedDivisor:    3,
		SpaceDivisor:          48,
		NewlineDivisor:        8,
		HanPerMille:           1050,
		KanaPerMille:          950,
		HangulPerMille:        900,
		LatinExtPerMille:      500,
		CyrillicGreekPerMille: 470,
		OtherLetterPerMille:   700,
		EmojiPerMille:         2200,
		MessageOverhead:       3,
		NameOverhead:          1,
		ReplyPriming:          3,
	}
	// ProfileGeneric is the balanced default for unknown providers/models:
	// roughly the midpoint of cl100k and o200k, deliberately biased upward
	// (an over-estimate compacts early, an under-estimate can overflow the
	// provider context window).
	ProfileGeneric = Profile{
		Name:                  "generic",
		WordDivisor:           6,
		DigitDivisor:          3,
		SymbolSameDivisor:     8,
		SymbolMixedDivisor:    3,
		SpaceDivisor:          48,
		NewlineDivisor:        8,
		HanPerMille:           1000,
		KanaPerMille:          850,
		HangulPerMille:        800,
		LatinExtPerMille:      400,
		CyrillicGreekPerMille: 300,
		OtherLetterPerMille:   650,
		EmojiPerMille:         2100,
		MessageOverhead:       3,
		NameOverhead:          1,
		ReplyPriming:          3,
	}
)

func (p Profile) normalized() Profile {
	if p.WordDivisor <= 0 {
		return ProfileGeneric
	}
	return p
}

// ProfileFor picks the closest calibrated profile from the provider/model
// identity. Unknown combinations fall back to the conservative generic
// profile.
func ProfileFor(provider, model string) Profile {
	haystack := strings.ToLower(strings.TrimSpace(provider + " " + model))
	switch {
	case strings.Contains(haystack, "claude"), strings.Contains(haystack, "anthropic"):
		return ProfileAnthropic
	case containsAny(haystack,
		"gpt-4o", "gpt-4.1", "gpt-4.5", "gpt-5", "gpt-oss", "chatgpt-4o",
		"o1-", "o3-", "o4-", "o200k"):
		return ProfileO200k
	case containsAny(haystack,
		"gpt-4", "gpt-3.5", "gpt-3", "text-embedding", "davinci", "cl100k"):
		return ProfileCL100k
	default:
		return ProfileGeneric
	}
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// Estimate returns the estimated token count for text under profile.
func Estimate(text string, profile Profile) int {
	if text == "" {
		return 0
	}
	p := profile.normalized()
	total := 0
	hasPending := false
	pendingClass := classOther
	pendingLen := 0
	pendingFirst := rune(0)
	pendingMixed := false

	flush := func() {
		if !hasPending || pendingLen == 0 {
			hasPending = false
			return
		}
		total += runTokens(pendingClass, pendingLen, pendingMixed, p)
		hasPending = false
	}

	for _, r := range text {
		class := classify(r)
		if class == classMark {
			// Combining marks extend the current grapheme; they do not open a
			// new run and are not priced separately.
			continue
		}
		if hasPending && class == pendingClass {
			pendingLen++
			if class == classSymbol && r != pendingFirst {
				pendingMixed = true
			}
			continue
		}
		if hasPending {
			// A single symbol directly before a letter merges into the word
			// pretoken (BPE pattern: optional one non-letter char + letters),
			// e.g. "_user" or "**strong".
			if pendingClass == classSymbol && pendingLen == 1 && isLetterClass(class) {
				hasPending = false
			} else {
				flush()
			}
		}
		pendingClass = class
		pendingLen = 1
		pendingFirst = r
		pendingMixed = false
		hasPending = true
	}
	flush()
	return total
}

// EstimateMessages returns the estimated prompt tokens for a message list,
// including the OpenAI-style per-message framing overhead (3 tokens per
// message, 1 per name). The reply priming (+3) applies to a whole chat
// request, not to a message subset, so callers pricing a complete request add
// Profile.ReplyPriming themselves — keeping subset estimates additive.
func EstimateMessages(messages []types.Message, profile Profile) int {
	if len(messages) == 0 {
		return 0
	}
	p := profile.normalized()
	total := 0
	for index := range messages {
		message := &messages[index]
		total += p.MessageOverhead
		total += Estimate(message.Role, p)
		if len(message.ContentParts) > 0 {
			total += estimateJSON(message.ContentParts, p)
		} else {
			total += Estimate(message.Content, p)
		}
		if len(message.ToolCalls) > 0 {
			total += estimateJSON(message.ToolCalls, p)
		}
		if id := strings.TrimSpace(message.ToolCallID); id != "" {
			total += Estimate(id, p)
		}
	}
	return total
}

func estimateJSON(value interface{}, p Profile) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	serialized := string(encoded)
	if serialized == "null" || serialized == "[]" {
		return 0
	}
	return Estimate(serialized, p)
}

type runeClass int

const (
	classOther runeClass = iota
	classMark
	classNewline
	classSpace
	classLatinWord
	classLatinExt
	classCyrillicGreek
	classOtherLetter
	classHan
	classKana
	classHangul
	classDigit
	classEmoji
	classSymbol
)

func classify(r rune) runeClass {
	switch r {
	case '\n', '\r', '\u2028', '\u2029', '\u0085':
		return classNewline
	case '\u200d', '\ufe0f': // ZWJ / variation selector: part of an emoji sequence
		return classEmoji
	}
	if unicode.IsSpace(r) {
		return classSpace
	}
	if unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me) {
		return classMark
	}
	if isEmojiRune(r) {
		return classEmoji
	}
	if unicode.Is(unicode.Han, r) {
		return classHan
	}
	if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
		return classKana
	}
	if unicode.Is(unicode.Hangul, r) {
		return classHangul
	}
	if unicode.IsDigit(r) {
		return classDigit
	}
	if unicode.IsLetter(r) {
		switch {
		case r < 0x0250: // ASCII + Latin-1 Supplement + Latin Extended-A/B
			return classLatinWord
		case r >= 0x1e00 && r <= 0x1eff: // Latin Extended Additional (Vietnamese…)
			return classLatinExt
		case unicode.Is(unicode.Cyrillic, r), unicode.Is(unicode.Greek, r):
			return classCyrillicGreek
		default:
			return classOtherLetter
		}
	}
	return classSymbol
}

func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1f000 && r <= 0x1faff, // emoji, symbols, skin tones, flags
		r >= 0x2600 && r <= 0x27bf,   // misc symbols + dingbats
		r >= 0x2b00 && r <= 0x2bff,   // arrows/symbols
		r >= 0x1f1e6 && r <= 0x1f1ff: // regional indicators
		return true
	}
	return false
}

func isLetterClass(class runeClass) bool {
	switch class {
	case classLatinWord, classLatinExt, classCyrillicGreek, classOtherLetter,
		classHan, classKana, classHangul:
		return true
	default:
		return false
	}
}

func runTokens(class runeClass, runLen int, mixed bool, p Profile) int {
	if runLen <= 0 {
		return 0
	}
	switch class {
	case classNewline:
		return maxInt(1, ceilDiv(runLen, p.NewlineDivisor))
	case classSpace:
		if runLen <= 1 {
			return 0
		}
		return maxInt(1, ceilDiv(runLen-1, p.SpaceDivisor))
	case classHan:
		return maxInt(1, scaled(runLen, p.HanPerMille))
	case classKana:
		return maxInt(1, scaled(runLen, p.KanaPerMille))
	case classHangul:
		return maxInt(1, scaled(runLen, p.HangulPerMille))
	case classLatinExt:
		return maxInt(1, scaled(runLen, p.LatinExtPerMille))
	case classCyrillicGreek:
		return maxInt(1, scaled(runLen, p.CyrillicGreekPerMille))
	case classOtherLetter:
		return maxInt(1, scaled(runLen, p.OtherLetterPerMille))
	case classEmoji:
		return maxInt(1, scaled(runLen, p.EmojiPerMille))
	case classDigit:
		return maxInt(1, ceilDiv(runLen, p.DigitDivisor))
	case classLatinWord:
		return maxInt(1, (runLen+p.WordDivisor/2)/p.WordDivisor)
	case classSymbol:
		if mixed {
			return maxInt(1, ceilDiv(runLen, p.SymbolMixedDivisor))
		}
		return maxInt(1, ceilDiv(runLen, p.SymbolSameDivisor))
	default:
		return 0
	}
}

func ceilDiv(value, divisor int) int {
	if divisor <= 0 {
		divisor = 1
	}
	return (value + divisor - 1) / divisor
}

// scaled returns ceil(runes * perMille / 1000).
func scaled(runes, perMille int) int {
	return (runes*perMille + 999) / 1000
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
