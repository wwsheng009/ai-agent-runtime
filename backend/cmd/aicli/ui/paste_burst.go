package ui

import (
	"os"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	pasteBurstMinChars       = 3
	pasteEnterSuppressWindow = 120 * time.Millisecond
)

var pasteBurstCharInterval = defaultPasteBurstCharInterval()
var pasteBurstActiveIdleTimeout = defaultPasteBurstActiveIdleTimeout()

func defaultPasteBurstCharInterval() time.Duration {
	if runtime.GOOS == "windows" {
		return 30 * time.Millisecond
	}
	if strings.TrimSpace(os.Getenv("WSL_DISTRO_NAME")) != "" || strings.TrimSpace(os.Getenv("WSL_INTEROP")) != "" {
		return 35 * time.Millisecond
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM_PROGRAM")), "vscode") {
		return 35 * time.Millisecond
	}
	return 12 * time.Millisecond
}

func defaultPasteBurstActiveIdleTimeout() time.Duration {
	if runtime.GOOS == "windows" {
		return 60 * time.Millisecond
	}
	if strings.TrimSpace(os.Getenv("WSL_DISTRO_NAME")) != "" || strings.TrimSpace(os.Getenv("WSL_INTEROP")) != "" {
		return 35 * time.Millisecond
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM_PROGRAM")), "vscode") {
		return 35 * time.Millisecond
	}
	return 12 * time.Millisecond
}

type CharDecisionKind int

const (
	CharDecisionNone CharDecisionKind = iota
	CharDecisionBeginBuffer
	CharDecisionBufferAppend
	CharDecisionRetainFirstChar
	CharDecisionBeginBufferFromPending
)

type CharDecision struct {
	Kind       CharDecisionKind
	RetroChars int
}

type FlushResultKind int

const (
	FlushResultNone FlushResultKind = iota
	FlushResultPaste
	FlushResultTyped
)

type FlushResult struct {
	Kind FlushResultKind
	Text string
	Ch   rune
}

type RetroGrab struct {
	StartByte int
	Grabbed   string
}

type pastePendingChar struct {
	ch rune
	at time.Time
}

type PasteBurst struct {
	lastPlainCharTime         time.Time
	consecutivePlainCharBurst int
	burstWindowUntil          time.Time
	buffer                    []rune
	active                    bool
	pendingFirstChar          *pastePendingChar
}

func NewPasteBurst() *PasteBurst {
	return &PasteBurst{}
}

func (b *PasteBurst) RecommendedFlushDelay() time.Duration {
	return recommendedPasteBurstDelay()
}

func (b *PasteBurst) RecommendedActiveFlushDelay() time.Duration {
	return recommendedPasteBurstDelay()
}

func recommendedPasteBurstDelay() time.Duration {
	delay := pasteBurstCharInterval
	if pasteBurstActiveIdleTimeout > delay {
		delay = pasteBurstActiveIdleTimeout
	}
	return delay + time.Millisecond
}

func (b *PasteBurst) IsActive() bool {
	return b != nil && (b.active || len(b.buffer) > 0 || b.pendingFirstChar != nil)
}

func (b *PasteBurst) HasBufferedText() bool {
	return b != nil && (b.active || len(b.buffer) > 0)
}

func (b *PasteBurst) HasPendingFirstChar() bool {
	return b != nil && b.pendingFirstChar != nil
}

func (b *PasteBurst) ContainsNewline() bool {
	if b == nil {
		return false
	}
	for _, r := range b.buffer {
		if r == '\n' {
			return true
		}
	}
	return false
}

func (b *PasteBurst) Empty() bool {
	return !b.IsActive()
}

func (b *PasteBurst) Deadline() time.Time {
	if b == nil {
		return time.Time{}
	}
	if b.HasBufferedText() {
		if b.lastPlainCharTime.IsZero() {
			return time.Time{}
		}
		return b.lastPlainCharTime.Add(pasteBurstActiveIdleTimeout)
	}
	if b.pendingFirstChar != nil {
		if b.pendingFirstChar.at.IsZero() {
			return time.Time{}
		}
		return b.pendingFirstChar.at.Add(pasteBurstCharInterval)
	}
	return time.Time{}
}

func (b *PasteBurst) RecentPlainCharDeadline() time.Time {
	if b == nil || b.lastPlainCharTime.IsZero() {
		return time.Time{}
	}
	return b.lastPlainCharTime.Add(pasteBurstCharInterval)
}

func (b *PasteBurst) PlainContinuationDeadline() time.Time {
	if b == nil {
		return time.Time{}
	}
	if !b.lastPlainCharTime.IsZero() {
		return b.lastPlainCharTime.Add(pasteBurstCharInterval)
	}
	return b.burstWindowUntil
}

func (b *PasteBurst) OnPlainChar(ch rune, now time.Time) CharDecision {
	if b == nil {
		return CharDecision{}
	}
	b.notePlainChar(now)

	if b.HasBufferedText() {
		b.burstWindowUntil = now.Add(pasteEnterSuppressWindow)
		return CharDecision{Kind: CharDecisionBufferAppend}
	}

	if b.pendingFirstChar != nil && now.Sub(b.pendingFirstChar.at) <= pasteBurstCharInterval {
		b.active = true
		held := b.pendingFirstChar
		b.pendingFirstChar = nil
		b.buffer = append(b.buffer, held.ch)
		b.burstWindowUntil = now.Add(pasteEnterSuppressWindow)
		return CharDecision{Kind: CharDecisionBeginBufferFromPending}
	}

	if b.consecutivePlainCharBurst >= pasteBurstMinChars {
		return CharDecision{
			Kind:       CharDecisionBeginBuffer,
			RetroChars: b.consecutivePlainCharBurst - 1,
		}
	}

	b.pendingFirstChar = &pastePendingChar{ch: ch, at: now}
	return CharDecision{Kind: CharDecisionRetainFirstChar}
}

func (b *PasteBurst) OnPlainCharNoHold(now time.Time) CharDecision {
	if b == nil {
		return CharDecision{}
	}
	b.notePlainChar(now)

	if b.HasBufferedText() {
		b.burstWindowUntil = now.Add(pasteEnterSuppressWindow)
		return CharDecision{Kind: CharDecisionBufferAppend}
	}

	if b.consecutivePlainCharBurst >= pasteBurstMinChars {
		return CharDecision{
			Kind:       CharDecisionBeginBuffer,
			RetroChars: b.consecutivePlainCharBurst - 1,
		}
	}

	return CharDecision{}
}

func (b *PasteBurst) notePlainChar(now time.Time) {
	if b == nil {
		return
	}
	if !b.lastPlainCharTime.IsZero() && now.Sub(b.lastPlainCharTime) <= pasteBurstCharInterval {
		b.consecutivePlainCharBurst++
	} else {
		b.consecutivePlainCharBurst = 1
	}
	b.lastPlainCharTime = now
}

func (b *PasteBurst) FlushIfDue(now time.Time) FlushResult {
	if b == nil {
		return FlushResult{}
	}
	timeout := pasteBurstCharInterval
	if b.HasBufferedText() {
		timeout = pasteBurstActiveIdleTimeout
	}
	timedOut := !b.lastPlainCharTime.IsZero() && now.Sub(b.lastPlainCharTime) > timeout
	if timedOut && b.HasBufferedText() {
		b.active = false
		out := string(b.buffer)
		b.buffer = b.buffer[:0]
		return FlushResult{Kind: FlushResultPaste, Text: out}
	}
	if timedOut {
		if b.pendingFirstChar != nil {
			ch := b.pendingFirstChar.ch
			b.pendingFirstChar = nil
			return FlushResult{Kind: FlushResultTyped, Ch: ch}
		}
	}
	return FlushResult{}
}

func (b *PasteBurst) AppendNewlineIfActive(now time.Time) bool {
	if b == nil || !b.HasBufferedText() {
		return false
	}
	b.buffer = append(b.buffer, '\n')
	b.burstWindowUntil = now.Add(pasteEnterSuppressWindow)
	return true
}

func (b *PasteBurst) NewlineShouldInsertInsteadOfSubmit(now time.Time) bool {
	if b == nil {
		return false
	}
	if b.HasBufferedText() {
		return true
	}
	return !b.burstWindowUntil.IsZero() && !now.After(b.burstWindowUntil)
}

func (b *PasteBurst) ExtendWindow(now time.Time) {
	if b == nil {
		return
	}
	b.burstWindowUntil = now.Add(pasteEnterSuppressWindow)
}

func (b *PasteBurst) BeginWithRetroGrabbed(grabbed string, now time.Time) {
	if b == nil {
		return
	}
	if grabbed != "" {
		b.buffer = append(b.buffer, []rune(grabbed)...)
	}
	b.active = true
	b.burstWindowUntil = now.Add(pasteEnterSuppressWindow)
}

func (b *PasteBurst) BeginBufferFromPending(now time.Time) bool {
	if b == nil || b.active || b.pendingFirstChar == nil {
		return false
	}
	if now.Sub(b.pendingFirstChar.at) > pasteBurstCharInterval {
		return false
	}
	held := b.pendingFirstChar
	b.pendingFirstChar = nil
	b.active = true
	b.buffer = append(b.buffer, held.ch)
	b.burstWindowUntil = now.Add(pasteEnterSuppressWindow)
	return true
}

func (b *PasteBurst) AppendCharToBuffer(ch rune, now time.Time) {
	if b == nil {
		return
	}
	b.buffer = append(b.buffer, ch)
	b.burstWindowUntil = now.Add(pasteEnterSuppressWindow)
}

func (b *PasteBurst) TryAppendCharIfActive(ch rune, now time.Time) bool {
	if b == nil || !b.HasBufferedText() {
		return false
	}
	b.AppendCharToBuffer(ch, now)
	return true
}

func (b *PasteBurst) DecideBeginBuffer(now time.Time, before string, retroChars int) *RetroGrab {
	if b == nil {
		return nil
	}
	startByte := retroStartIndex(before, retroChars)
	grabbed := before[startByte:]
	if !looksPastey(grabbed) {
		return nil
	}
	b.BeginWithRetroGrabbed(grabbed, now)
	return &RetroGrab{StartByte: startByte, Grabbed: grabbed}
}

func (b *PasteBurst) FlushBeforeModifiedInput() string {
	if b == nil || !b.IsActive() {
		return ""
	}
	var builder strings.Builder
	if b.pendingFirstChar != nil {
		builder.WriteRune(b.pendingFirstChar.ch)
	}
	if len(b.buffer) > 0 {
		builder.WriteString(string(b.buffer))
	}
	b.ClearAfterExplicitPaste()
	return builder.String()
}

func (b *PasteBurst) ClearWindowAfterNonChar() {
	if b == nil {
		return
	}
	b.consecutivePlainCharBurst = 0
	b.lastPlainCharTime = time.Time{}
	b.burstWindowUntil = time.Time{}
	b.active = false
	b.pendingFirstChar = nil
}

func (b *PasteBurst) ClearAfterExplicitPaste() {
	if b == nil {
		return
	}
	b.lastPlainCharTime = time.Time{}
	b.consecutivePlainCharBurst = 0
	b.burstWindowUntil = time.Time{}
	b.active = false
	b.buffer = b.buffer[:0]
	b.pendingFirstChar = nil
}

func looksPastey(text string) bool {
	if text == "" {
		return false
	}
	runeCount := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			return true
		}
		runeCount++
	}
	return runeCount >= 16
}

func retroStartIndex(before string, retroChars int) int {
	if retroChars <= 0 {
		return len(before)
	}
	idx := len(before)
	for i := 0; i < retroChars && idx > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(before[:idx])
		if size <= 0 {
			return 0
		}
		idx -= size
	}
	if idx < 0 {
		return 0
	}
	return idx
}

func charBurstCount(now time.Time, prev time.Time) int {
	if prev.IsZero() {
		return 1
	}
	if now.Sub(prev) <= pasteBurstCharInterval {
		return 2
	}
	return 1
}
