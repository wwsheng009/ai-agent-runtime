package output

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ============================================================================
// Phase 2 capture 升级测试（7.2 B/C）
// ============================================================================

// keyedHashSpy 记录调用，返回带前缀的 keyed hash。
type keyedHashSpy struct {
	calls int
}

func (k *keyedHashSpy) hash(bytes []byte) string {
	k.calls++
	return fmt.Sprintf("k%d", k.calls)
}

func captureEnv(entryID, batchID string, seq uint64, bytes string) MirrorEnvelope {
	return MirrorEnvelope{
		MirrorEntryRef: MirrorEntryRef{
			EntryID:            entryID,
			MirrorIndex:        2,
			SinkID:             "capture",
			TargetClass:        TargetClassCapture,
			ProjectionTargetID: "pt-capture",
		},
		RenderBatch: RenderBatch{
			RenderIntent: RenderIntent{
				Kind:  TransactionFrame,
				Bytes: []byte(bytes),
			},
			SessionID: "ses",
			Sequence:  seq,
			BatchID:   batchID,
		},
		Policy:             MirrorBestEffort,
		RequestedApplyMode: MirrorApplyBytes,
		EffectiveApplyMode: MirrorApplyBytes,
	}
}

// TestCaptureKeyedHash：session-keyed hash 注入生效（ContentHash 非公开 SHA）。
func TestCaptureKeyedHash(t *testing.T) {
	spy := &keyedHashSpy{}
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries: 16,
		MaxBytes:   1 << 20,
		KeyedHash:  spy.hash,
	})
	res := c.SubmitMirror(context.Background(), captureEnv("e1", "b1", 1, "data"))
	if res.Status != MirrorApplied {
		t.Fatalf("status: %s", res.Status)
	}
	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("entries: %d", len(entries))
	}
	if entries[0].ContentHash != "k1" {
		t.Fatalf("keyed hash: %q", entries[0].ContentHash)
	}
	if spy.calls != 1 {
		t.Fatalf("keyed hash calls: %d", spy.calls)
	}
	// 完整 schema 字段。
	e := entries[0]
	if e.SchemaVersion != SchemaVersion || e.SessionID != "ses" || e.BatchID != "b1" ||
		e.Sequence != 1 || e.SinkID != "capture" || e.TargetClass != TargetClassCapture ||
		e.ProjectionTargetID != "pt-capture" || e.ObservedPrimaryTargetID != "pt-capture" {
		t.Fatalf("schema identity incomplete: %+v", e)
	}
	if e.MirrorEntryID != "e1" || e.MirrorIndex != 2 || e.Policy != MirrorBestEffort ||
		e.EffectiveApplyMode != MirrorApplyBytes {
		t.Fatalf("mirror identity incomplete: %+v", e)
	}
}

// TestCaptureTruncation：单 batch 上限 → RecordedTruncated + DroppedBytes +
// TruncationReason；payload 保留前缀。
func TestCaptureTruncation(t *testing.T) {
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:     16,
		MaxBytes:       1 << 20,
		StorePayload:   true,
		MaxSingleBatch: 8,
	})
	payload := "0123456789ABCDEF"
	res := c.SubmitMirror(context.Background(), captureEnv("e1", "b1", 1, payload))
	if res.Status != MirrorApplied {
		t.Fatalf("status: %s", res.Status)
	}
	entries := c.Entries()
	if entries[0].Mode != RecordedTruncated {
		t.Fatalf("mode: %s", entries[0].Mode)
	}
	if entries[0].DroppedBytes != len(payload)-8 {
		t.Fatalf("dropped bytes: %d", entries[0].DroppedBytes)
	}
	if entries[0].TruncationReason != "single_batch_limit" {
		t.Fatalf("reason: %q", entries[0].TruncationReason)
	}
	if entries[0].BytesLength != len(payload) {
		t.Fatalf("bytes length must record full original: %d", entries[0].BytesLength)
	}
	got, cls := c.Payload(entries[0].CaptureEntryID)
	if cls != CapturePayloadErrorNone || string(got) != payload[:8] {
		t.Fatalf("truncated payload: %q class=%s", got, cls)
	}
}

// TestCaptureJournalRing：journal 超限丢旧观察记录并计数。
func TestCaptureJournalRing(t *testing.T) {
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries: 8,
		MaxBytes:   1 << 20,
	})
	for i := 0; i < 12; i++ {
		res := c.SubmitMirror(context.Background(), captureEnv(
			fmt.Sprintf("e%d", i), fmt.Sprintf("b%d", i), uint64(i+1), "j"),
		)
		if res.Status != MirrorApplied {
			t.Fatalf("submit %d: %s", i, res.Status)
		}
	}
	j := c.Journal()
	if len(j) > 8 {
		t.Fatalf("journal ring overflow: %d", len(j))
	}
	snap := c.CaptureSnapshot()
	if snap.DroppedBatches < 4 {
		t.Fatalf("expected >=4 dropped batches, got %d", snap.DroppedBatches)
	}
	if snap.SchemaVersion != SchemaVersion || snap.FullCaptureEnabled != false {
		t.Fatalf("snapshot schema: %+v", snap)
	}
	if snap.PayloadItems != 0 || snap.PayloadBytes != 0 {
		t.Fatalf("metadata-only must not retain payload: %+v", snap)
	}
}

// TestCaptureStrictCapacityPrimary：primary 声明 full 但超出容量 → zero
// Rejected（strict capacity，不丢内容兼职声称 committed）。
func TestCaptureStrictCapacityPrimary(t *testing.T) {
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:   8,
		MaxBytes:     16,
		StorePayload: true,
	})
	big := make([]byte, 64)
	res := c.Submit(context.Background(), RenderBatch{
		RenderIntent: RenderIntent{Kind: TransactionFrame, Bytes: big},
		SessionID:    "ses",
		Sequence:     1,
		BatchID:      "b1",
	})
	if res.Status != DeliveryRejected || res.ErrorClass != DeliveryErrorOversized {
		t.Fatalf("strict capacity: %s/%s", res.Status, res.ErrorClass)
	}
	if res.Certainty != WriteCertaintyZero {
		t.Fatalf("strict capacity certainty: %s", res.Certainty)
	}
	if len(c.Entries()) != 0 {
		t.Fatal("rejected primary must not leave entries")
	}
}

// TestCapturePrimaryObservedIdentity：capture 作为 primary 时 MirrorEntryID
// 为空、ObservedPrimaryTargetID==ProjectionTargetID、policy/mode 零值。
func TestCapturePrimaryObservedIdentity(t *testing.T) {
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:   8,
		MaxBytes:     1 << 20,
		StorePayload: true,
	})
	res := c.Submit(context.Background(), RenderBatch{
		RenderIntent: RenderIntent{Kind: TransactionFrame, Bytes: []byte("primary-data")},
		SessionID:    "ses",
		Sequence:     2,
		BatchID:      "b2",
	})
	if res.Status != DeliveryCommitted {
		t.Fatalf("primary: %s", res.Status)
	}
	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("entries: %d", len(entries))
	}
	e := entries[0]
	if e.MirrorEntryID != "" || e.MirrorIndex != 0 {
		t.Fatalf("primary must have empty mirror identity: %+v", e)
	}
	if e.ObservedPrimaryTargetID != e.ProjectionTargetID {
		t.Fatalf("observed target: %q != %q", e.ObservedPrimaryTargetID, e.ProjectionTargetID)
	}
	if e.Policy != "" || e.RequestedApplyMode != "" || e.EffectiveApplyMode != "" || e.NonAuthoritative {
		t.Fatalf("primary policy/mode must be zero: %+v", e)
	}
}

// TestCaptureTTL：过期 entry 被清除并计数。
func TestCaptureTTL(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC))
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries: 16,
		MaxBytes:   1 << 20,
		TTL:        10 * time.Second,
	})
	c.SetClock(clock)
	_ = c.SubmitMirror(context.Background(), captureEnv("e1", "b1", 1, "x"))
	if len(c.Entries()) != 1 {
		t.Fatalf("after insert: %d", len(c.Entries()))
	}
	clock.Advance(11 * time.Second)
	_ = c.SubmitMirror(context.Background(), captureEnv("e2", "b2", 2, "y"))
	entries := c.Entries()
	if len(entries) != 1 || entries[0].BatchID != "b2" {
		t.Fatalf("TTL did not evict: %+v", entries)
	}
	snap := c.CaptureSnapshot()
	if snap.Erased < 1 {
		t.Fatalf("erased count: %d", snap.Erased)
	}
}

// TestCaptureBudgetEviction：大 batch 触发预算淘汰——不 panic、统计正确
// （B1 回归）。
func TestCaptureBudgetEviction(t *testing.T) {
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:   16,
		MaxBytes:     24,
		StorePayload: true,
	})
	for i := 0; i < 2; i++ {
		res := c.SubmitMirror(context.Background(), captureEnv(
			fmt.Sprintf("e%d", i), fmt.Sprintf("b%d", i), uint64(i+1), "12345678"),
		)
		if res.Status != MirrorApplied {
			t.Fatalf("seed %d: %s", i, res.Status)
		}
	}
	res := c.SubmitMirror(context.Background(), captureEnv("e9", "b9", 9, "ABCDEFGHIJKLMNOPQRSTUVWX"))
	if res.Status == MirrorFailed {
		t.Fatal("mirror must accept (drop old, record new)")
	}
	snap := c.CaptureSnapshot()
	if snap.DroppedBatches < 1 {
		t.Fatalf("expected dropped batches, got %d", snap.DroppedBatches)
	}
	seen := map[string]bool{}
	for _, e := range c.Entries() {
		if seen[e.CaptureEntryID] {
			t.Fatalf("duplicate entry %q", e.CaptureEntryID)
		}
		seen[e.CaptureEntryID] = true
	}
}

// TestCaptureTTLMultiExpired：多条目多过期且过期不连续——蒸发后无重复
// （S1 回归）。
func TestCaptureTTLMultiExpired(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC))
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries: 16,
		MaxBytes:   1 << 20,
		TTL:        10 * time.Second,
	})
	c.SetClock(clock)
	// E1(过期), A(存活), E2(过期), B(存活) 排列：seed 之间推进时间。
	_ = c.SubmitMirror(context.Background(), captureEnv("e1", "b1", 1, "a"))
	clock.Advance(2 * time.Second)
	_ = c.SubmitMirror(context.Background(), captureEnv("a1", "ba", 2, "b"))
	clock.Advance(2 * time.Second)
	_ = c.SubmitMirror(context.Background(), captureEnv("e2", "b2", 3, "c"))
	clock.Advance(2 * time.Second)
	_ = c.SubmitMirror(context.Background(), captureEnv("b1", "bb", 4, "d"))
	clock.Advance(9 * time.Second) // t=15s：e1(0)/a1(2)/e2(4) 过期（TTL 10s），b1(6) 存活
	_ = c.SubmitMirror(context.Background(), captureEnv("c1", "bc", 5, "e"))
	entries := c.Entries()
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.CaptureEntryID] {
			t.Fatalf("duplicate entry after multi-TTL eviction: %q", e.CaptureEntryID)
		}
		seen[e.CaptureEntryID] = true
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 surviving entries (b1+c1), got %d: %+v", len(entries), entries)
	}
}

// TestCaptureMirrorClosed：Close 后 SubmitMirror 返回 failed/closed（S3 回归）。
func TestCaptureMirrorClosed(t *testing.T) {
	c := NewCaptureSink("pt-capture", CaptureOptions{MaxEntries: 8, MaxBytes: 1 << 20})
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	res := c.SubmitMirror(context.Background(), captureEnv("e1", "b1", 1, "x"))
	if res.Status != MirrorFailed || res.ErrorClass != DeliveryErrorClosed {
		t.Fatalf("closed mirror: %s/%s", res.Status, res.ErrorClass)
	}
	if len(c.Entries()) != 0 {
		t.Fatal("closed mirror must not record entries")
	}
}

// TestCaptureMaxEntriesOne：MaxEntries=1 淘汰统计不越界、不 off-by-one
// （S4 回归）。
func TestCaptureMaxEntriesOne(t *testing.T) {
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries: 1,
		MaxBytes:   1 << 20,
	})
	for i := 0; i < 5; i++ {
		res := c.SubmitMirror(context.Background(), captureEnv(
			fmt.Sprintf("e%d", i), fmt.Sprintf("b%d", i), uint64(i+1), "data"),
		)
		if res.Status != MirrorApplied {
			t.Fatalf("seed %d: %s", i, res.Status)
		}
	}
	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("max entries: %d", len(entries))
	}
	if entries[0].BatchID != "b4" {
		t.Fatalf("kept wrong entry: %+v", entries[0])
	}
	snap := c.CaptureSnapshot()
	if snap.DroppedBatches < 4 {
		t.Fatalf("dropped: %d", snap.DroppedBatches)
	}
}

// TestVirtualSinkPrimaryClearsNonAuth：primary 清除 mirror nonAuth 残留
// （S2 回归，7.3 不变量）。
func TestVirtualSinkPrimaryClearsNonAuth(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	res := v.SubmitMirror(context.Background(), virtualEnv("e1", "b1", 1, MirrorApplyBytes, true, "attempted"))
	if res.Status != MirrorApplied {
		t.Fatalf("mirror: %s", res.Status)
	}
	snap := v.Projection()
	if snap.Validity == ProjectionValid || !snap.NonAuthoritative {
		t.Fatalf("mirror nonAuth precondition failed: %+v", snap)
	}
	pRes := v.Submit(context.Background(), RenderBatch{
		RenderIntent: RenderIntent{
			Kind:  TransactionFrame,
			Bytes: []byte("primary"),
			Terminal: RenderTerminalContext{
				Geometry: TerminalGeometry{Width: 20, Height: 6},
			},
		},
		SessionID: "ses",
		Sequence:  5,
		BatchID:   "b5",
	})
	if pRes.Status != DeliveryCommitted {
		t.Fatalf("primary: %s", pRes.Status)
	}
	snap = v.Projection()
	if snap.NonAuthoritative {
		t.Fatal("primary must clear nonAuth residue")
	}
	if snap.Validity == ProjectionValid && snap.NonAuthoritative {
		t.Fatal("valid + nonAuth is illegal (7.3)")
	}
}

// TestVirtualSinkBarrierInvalidGeometry：空 barrier 携带非法 geometry →
// MirrorFailed（S6 回归，不再吞错误）。
func TestVirtualSinkBarrierInvalidGeometry(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	env := virtualEnv("e1", "b1", 1, MirrorApplyBytes, false, "")
	env.Kind = TransactionContextBarrier
	env.Terminal.Geometry = TerminalGeometry{Width: 0, Height: 6}
	res := v.SubmitMirror(context.Background(), env)
	if res.Status != MirrorFailed || res.ErrorClass != DeliveryErrorInvalid {
		t.Fatalf("invalid barrier: %s/%s", res.Status, res.ErrorClass)
	}
}
