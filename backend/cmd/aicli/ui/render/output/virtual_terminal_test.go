package output

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEmulator 是 VirtualTerminalSink 契约测试用的内存 emulator。
type fakeEmulator struct {
	mu       sync.Mutex
	geometry TerminalGeometry
	profile  TerminalProfileRef
	bytes    []byte
	width    int
	height   int
	invalid  bool
	applied  int
	ctxCount int
}

func newFakeEmulator(w, h int) *fakeEmulator {
	return &fakeEmulator{width: w, height: h}
}

func (f *fakeEmulator) ApplyContext(g TerminalGeometry, p TerminalProfileRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxCount++
	if g.Width < 1 || g.Height < 1 {
		return NewClassifiedError(DeliveryErrorInvalid, "bad geometry")
	}
	f.geometry = g
	f.profile = p
	f.width, f.height = g.Width, g.Height
	return nil
}

func (f *fakeEmulator) Apply(bytes []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes = append(f.bytes, bytes...)
	f.applied++
	f.invalid = false
	return nil
}

func (f *fakeEmulator) Snapshot() VirtualProjectionSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	validity := ProjectionValid
	if f.invalid {
		validity = ProjectionUnknown
	}
	return VirtualProjectionSnapshot{
		SchemaVersion: SchemaVersion,
		Width:         f.width,
		Height:        f.height,
		Rows:          strings.Split(string(f.bytes), "\n"),
		Validity:      validity,
	}
}

func (f *fakeEmulator) Invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalid = true
}

func virtualEnv(entryID, batchID string, seq uint64, mode MirrorApplyMode, nonAuth bool, bytes string) MirrorEnvelope {
	return MirrorEnvelope{
		MirrorEntryRef: MirrorEntryRef{
			EntryID:            entryID,
			MirrorIndex:        1,
			SinkID:             "virtual",
			TargetClass:        TargetClassVirtual,
			ProjectionTargetID: "pt-virtual",
		},
		RenderBatch: RenderBatch{
			RenderIntent: RenderIntent{
				Kind:  TransactionFrame,
				Bytes: []byte(bytes),
				Terminal: RenderTerminalContext{
					Geometry: TerminalGeometry{Width: 20, Height: 6},
					Profile:  TerminalProfileRef{ID: "ansi", Version: 1},
				},
			},
			SessionID: "ses",
			Sequence:  seq,
			BatchID:   batchID,
		},
		Policy:             MirrorBestEffort,
		RequestedApplyMode: mode,
		EffectiveApplyMode: mode,
		NonAuthoritative:   nonAuth,
	}
}

// TestVirtualSinkApplyBytes：committed 结果 + ApplyBytes mode → bytes 被
// 应用，MirrorApplied，快照 valid。
func TestVirtualSinkApplyBytes(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	res := v.SubmitMirror(context.Background(), virtualEnv("e1", "b1", 1, MirrorApplyBytes, false, "hello"))
	if res.Status != MirrorApplied {
		t.Fatalf("status: %s", res.Status)
	}
	snap := v.Projection()
	if snap.Validity != ProjectionValid {
		t.Fatalf("validity: %s", snap.Validity)
	}
	if snap.ProjectionTargetID != "pt-virtual" || snap.ObservedPrimaryTargetID != "pt-virtual" {
		t.Fatalf("target identity: %+v", snap)
	}
	if snap.LastMirrorEntryID != "e1" || snap.LastBatchID != "b1" || snap.LastSequence != 1 {
		t.Fatalf("entry identity: %+v", snap)
	}
	if emu.applied != 1 || string(emu.bytes) != "hello" {
		t.Fatalf("emulator got %q applied=%d", emu.bytes, emu.applied)
	}
}

// TestVirtualSinkMetadataOnlySkipped：metadata_only mode → 不解释 bytes，
// MirrorSkipped，且不标记 valid。
func TestVirtualSinkMetadataOnlySkipped(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	res := v.SubmitMirror(context.Background(), virtualEnv("e1", "b1", 1, MirrorApplyMetadataOnly, false, "nope"))
	if res.Status != MirrorSkipped {
		t.Fatalf("status: %s", res.Status)
	}
	if emu.applied != 0 {
		t.Fatalf("metadata_only must not apply bytes, applied=%d", emu.applied)
	}
}

// TestVirtualSinkNonAuthoritative：attempted intent（nonAuth=true）即使
// ApplyBytes 也保持 unknown，绝不 valid。
func TestVirtualSinkNonAuthoritative(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	res := v.SubmitMirror(context.Background(), virtualEnv("e1", "b1", 1, MirrorApplyBytes, true, "attempted"))
	if res.Status != MirrorApplied {
		t.Fatalf("status: %s", res.Status)
	}
	snap := v.Projection()
	if snap.Validity == ProjectionValid {
		t.Fatal("non-authoritative snapshot must not be valid")
	}
	if snap.Validity != ProjectionUnknown {
		t.Fatalf("validity: %s", snap.Validity)
	}
	if !snap.NonAuthoritative {
		t.Fatal("non-authoritative flag must carry")
	}
}

// TestVirtualSinkInvalidate：Invalidate 后快照 unknown；后续 valid apply
// 恢复。
func TestVirtualSinkInvalidate(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	_ = v.SubmitMirror(context.Background(), virtualEnv("e1", "b1", 1, MirrorApplyBytes, false, "ok"))
	v.Invalidate()
	if snap := v.Projection(); snap.Validity != ProjectionUnknown {
		t.Fatalf("after invalidate: %s", snap.Validity)
	}
	_ = v.SubmitMirror(context.Background(), virtualEnv("e2", "b2", 2, MirrorApplyBytes, false, "ok2"))
	if snap := v.Projection(); snap.Validity != ProjectionValid {
		t.Fatalf("after re-apply: %s", snap.Validity)
	}
}

// TestVirtualSinkInvalidGeometry：非法 geometry 在应用任何 bytes 前拒绝。
func TestVirtualSinkInvalidGeometry(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	env := virtualEnv("e1", "b1", 1, MirrorApplyBytes, false, "x")
	env.Terminal.Geometry = TerminalGeometry{Width: 0, Height: 6}
	res := v.SubmitMirror(context.Background(), env)
	if res.Status == MirrorApplied {
		t.Fatal("invalid geometry must not apply")
	}
	if emu.applied != 0 {
		t.Fatalf("emulator got bytes before geometry validation: %d", emu.applied)
	}
}

// TestVirtualSinkPrimarySubmit：virtual 作为 primary 时 LastMirrorEntryID 为
// 空、ObservedPrimaryTargetID==ProjectionTargetID、committed。
func TestVirtualSinkPrimarySubmit(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	res := v.Submit(context.Background(), RenderBatch{
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
	if res.Status != DeliveryCommitted || res.Certainty != WriteCertaintyFull {
		t.Fatalf("primary: %s/%s", res.Status, res.Certainty)
	}
	snap := v.Projection()
	if snap.LastMirrorEntryID != "" {
		t.Fatalf("primary must have empty mirror entry, got %q", snap.LastMirrorEntryID)
	}
	if snap.ObservedPrimaryTargetID != snap.ProjectionTargetID {
		t.Fatalf("primary observed target mismatch: %q != %q", snap.ObservedPrimaryTargetID, snap.ProjectionTargetID)
	}
	if snap.LastSequence != 5 {
		t.Fatalf("last seq: %d", snap.LastSequence)
	}
}

// TestVirtualSinkClosed：close 后 mirror 提交返回 failed。
func TestVirtualSinkClosed(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	if err := v.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	res := v.SubmitMirror(context.Background(), virtualEnv("e1", "b1", 1, MirrorApplyBytes, false, "x"))
	if res.Status != MirrorFailed {
		t.Fatalf("closed status: %s", res.Status)
	}
	snap := v.Snapshot()
	if snap.State != SinkLifecycleClosed {
		t.Fatalf("state: %s", snap.State)
	}
}

// TestVirtualSinkScrollbackLimit：MaxScrollback 截断 detached scrollback。
func TestVirtualSinkScrollbackLimit(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{MaxScrollback: 2})
	for i := 0; i < 5; i++ {
		env := virtualEnv("e", "b", uint64(i+1), MirrorApplyBytes, false, "row")
		// 每次用换行推动 scrollback（fake 简单累积，仅验证截断逻辑）。
		env.Bytes = []byte("row\n")
		_ = v.SubmitMirror(context.Background(), env)
	}
	snap := v.Projection()
	if len(snap.Scrollback) > 2 {
		t.Fatalf("scrollback not truncated: %d", len(snap.Scrollback))
	}
}

// TestVirtualSinkBarrier：空 barrier 推进 context、不解释 bytes、applied。
func TestVirtualSinkBarrier(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	env := virtualEnv("e1", "b1", 1, MirrorApplyBytes, false, "")
	env.Kind = TransactionContextBarrier
	res := v.SubmitMirror(context.Background(), env)
	if res.Status != MirrorApplied {
		t.Fatalf("barrier status: %s", res.Status)
	}
	if emu.applied != 0 {
		t.Fatalf("barrier must not apply bytes")
	}
	_ = time.Now()
}
