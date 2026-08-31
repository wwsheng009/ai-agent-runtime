package output

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCapturePayloadAccessExplicitHandle：10.4/7.2——payload 读取必须经
// AcquirePayload 显式 handle（带 purpose/LimitBytes/TTL）；hash-only 与
// non-authoritative payload 拒绝；revoke 后稳定返回 Revoked。
func TestCapturePayloadAccessExplicitHandle(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC))
	opts := CaptureOptions{
		MaxEntries:       16,
		MaxBytes:         1 << 20,
		StorePayload:     true,
		PayloadHandleTTL: 10 * time.Second,
	}
	c := NewCaptureSink("pt-capture", opts)
	c.SetClock(clock)

	// 写入一笔 full payload（authoritative）。
	res := c.SubmitMirror(context.Background(), captureEnv("e1", "b1", 1, "full-payload-bytes"))
	if res.Status != MirrorApplied {
		t.Fatalf("mirror: %s", res.Status)
	}
	entryID := c.Entries()[0].CaptureEntryID

	// 1. 需要 authorizer 且被拒绝 → Unauthorized。
	deny := &fixedAuthorizer{err: errors.New("denied")}
	got := c.AcquirePayload(context.Background(), CapturePayloadRequest{
		CaptureEntryID: entryID,
		Access:         CapturePayloadIncluding,
		LimitBytes:     1024,
	}, deny)
	if got.ErrorClass != CapturePayloadErrorUnauthorized {
		t.Fatalf("denied authorize must be unauthorized: %+v", got)
	}

	// 2. authorized 成功：完整 payload + TTL + ActiveHandleCount=1。
	got = c.AcquirePayload(context.Background(), CapturePayloadRequest{
		CaptureEntryID: entryID,
		Access:         CapturePayloadIncluding,
		LimitBytes:     1024,
	}, nil)
	if got.ErrorClass != CapturePayloadErrorNone {
		t.Fatalf("acquire: %+v", got)
	}
	if string(got.Payload) != "full-payload-bytes" {
		t.Fatalf("payload mismatch: %q", got.Payload)
	}
	if got.Mode != RecordedFullAvailable {
		t.Fatalf("mode: %s", got.Mode)
	}
	if got.ExpiresAt.IsZero() || !got.ExpiresAt.After(clock.Now()) {
		t.Fatalf("handle must carry future TTL: %+v", got)
	}
	if snap := c.CaptureSnapshot(); snap.ActiveHandleCount != 1 {
		t.Fatalf("active handles: %d", snap.ActiveHandleCount)
	}

	// 3. handle 读取可用（用 HandleID 凭证）。
	data, ec := c.PayloadHandle(got.HandleID)
	if ec != CapturePayloadErrorNone {
		t.Fatalf("handle read: %s", ec)
	}
	if string(data) != "full-payload-bytes" {
		t.Fatalf("handle payload: %q", data)
	}
	// 过期后 handle 失效（稳定 NotFound，不退回 journal）。
	clock.Advance(11 * time.Second)
	if _, ec := c.PayloadHandle(got.HandleID); ec != CapturePayloadErrorNotFound {
		t.Fatalf("expired handle must yield NotFound, got %s", ec)
	}
	if snap := c.CaptureSnapshot(); snap.ActiveHandleCount != 0 {
		t.Fatalf("expired handle must not count: %d", snap.ActiveHandleCount)
	}

	// 4. hash-only payload → NotFound（无 payload 可申请）。
	optsNoPayload := CaptureOptions{MaxEntries: 16, MaxBytes: 1 << 20, StorePayload: false, PayloadHandleTTL: 10 * time.Second}
	c2 := NewCaptureSink("pt-capture2", optsNoPayload)
	c2.SetClock(clock)
	_ = c2.SubmitMirror(context.Background(), captureEnv("e2", "b2", 2, "hash-only"))
	eid2 := c2.Entries()[0].CaptureEntryID
	got2 := c2.AcquirePayload(context.Background(), CapturePayloadRequest{
		CaptureEntryID: eid2,
		Access:         CapturePayloadIncluding,
	}, nil)
	if got2.ErrorClass != CapturePayloadErrorNotFound {
		t.Fatalf("hash-only must not expose payload: %+v", got2)
	}

	// 5. non-authoritative payload → 拒绝（7.2，attempted bytes 不提供）。
	optsNA := CaptureOptions{MaxEntries: 16, MaxBytes: 1 << 20, StorePayload: true, PayloadHandleTTL: 10 * time.Second}
	c3 := NewCaptureSink("pt-capture3", optsNA)
	c3.SetClock(clock)
	env := captureEnv("e3", "b3", 3, "non-auth-bytes")
	env = mirrorEnvelopeWithNonAuthoritative(env, true)
	_ = c3.SubmitMirror(context.Background(), env)
	eid3 := c3.Entries()[0].CaptureEntryID
	got3 := c3.AcquirePayload(context.Background(), CapturePayloadRequest{
		CaptureEntryID: eid3,
		Access:         CapturePayloadIncluding,
	}, nil)
	if got3.ErrorClass != CapturePayloadErrorUnauthorized {
		t.Fatalf("non-authoritative payload must be refused: %+v", got3)
	}
}

// TestCapturePayloadLimitBytesTruncates：LimitBytes 超限截断为 truncated mode。
func TestCapturePayloadLimitBytesTruncates(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC))
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:       16,
		MaxBytes:         1 << 20,
		StorePayload:     true,
		PayloadHandleTTL: time.Minute,
	})
	c.SetClock(clock)
	_ = c.SubmitMirror(context.Background(), captureEnv("e1", "b1", 1, "0123456789"))
	entryID := c.Entries()[0].CaptureEntryID
	got := c.AcquirePayload(context.Background(), CapturePayloadRequest{
		CaptureEntryID: entryID,
		Access:         CapturePayloadIncluding,
		LimitBytes:     4,
	}, nil)
	if got.ErrorClass != CapturePayloadErrorNone {
		t.Fatalf("acquire: %+v", got)
	}
	if got.Mode != RecordedTruncated {
		t.Fatalf("mode must be truncated: %s", got.Mode)
	}
	if string(got.Payload) != "0123" {
		t.Fatalf("truncated payload: %q", got.Payload)
	}
}

// TestCapturePayloadRevoke：RevokePayload 后读取稳定返回 revoked 类别。
func TestCapturePayloadRevoke(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC))
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:       16,
		MaxBytes:         1 << 20,
		StorePayload:     true,
		PayloadHandleTTL: time.Minute,
	})
	c.SetClock(clock)
	_ = c.SubmitMirror(context.Background(), captureEnv("e1", "b1", 1, "revocable"))
	entryID := c.Entries()[0].CaptureEntryID
	got := c.AcquirePayload(context.Background(), CapturePayloadRequest{
		CaptureEntryID: entryID,
		Access:         CapturePayloadIncluding,
	}, nil)
	if got.ErrorClass != CapturePayloadErrorNone {
		t.Fatalf("acquire: %+v", got)
	}
	// 吊销 handle：读取返回 Revoked，且 active 计数剔除（10.4）。
	c.RevokePayload(got.HandleID)
	if _, ec := c.PayloadHandle(got.HandleID); ec != CapturePayloadErrorRevoked {
		t.Fatalf("revoked handle must yield Revoked, got %s", ec)
	}
	if snap := c.CaptureSnapshot(); snap.ActiveHandleCount != 0 {
		t.Fatalf("revoked handle must not count: %d", snap.ActiveHandleCount)
	}
}

// fixedAuthorizer 恒返回固定错误。
type fixedAuthorizer struct{ err error }

func (f *fixedAuthorizer) Authorize(context.Context, CapturePayloadRequest) error { return f.err }

// mirrorEnvelopeWithNonAuthoritative 标记 envelope 为 non-authoritative。
func mirrorEnvelopeWithNonAuthoritative(env MirrorEnvelope, na bool) MirrorEnvelope {
	env.NonAuthoritative = na
	return env
}
