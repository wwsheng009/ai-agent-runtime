package output

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// ============================================================================
// CaptureSink（7.2）
// ============================================================================

// CapturedDelivery 是 capture 中一笔已保留的 delivery。
type CapturedDelivery struct {
	CaptureEntryID     string
	BatchID            string
	RecordID           string
	Sequence           uint64
	RouteEpoch         uint64
	ProjectionTargetID string
	Transaction        TransactionKind
	Mode               RecordedPayloadMode // metadata_only/hash_only/truncated/full_available
	BytesLength        int
	Hash               string
	CapturedAt         time.Time
	ExpiresAt          time.Time
}

// CaptureSnapshot 是 capture store 的可观察快照。
type CaptureSnapshot struct {
	Descriptor     TargetDescriptor
	State          SinkLifecycleState
	OwnerSessionID string
	Retained       int
	Entries        int
	MaxEntries     int
	MaxBytes       int
	Erased         uint64
	Dropped        uint64
}

// CaptureOptions 配置 capture store。
type CaptureOptions struct {
	MaxEntries   int
	MaxBytes     int               // 保留 payload 的字节预算
	TTL          time.Duration     // entries 过期时长；<=0 表示不过期
	StorePayload bool              // true = 保留 bytes（full_available）；false = hash_only
	Redact       func([]byte) bool // 返回 true 表示应重写/排除 payload（安全敏感）
}

// CaptureSink 是有界 capture sink：镜像是自己的 target，有独立 maxEntries/
// maxBytes/TTL；按 cap 淘汰最旧条目不阻塞 delivery。
type CaptureSink struct {
	desc    TargetDescriptor
	opts    CaptureOptions
	clock   Clock
	mu      sync.Mutex
	state   SinkLifecycleState
	owner   string
	entries []CapturedDelivery
	store   map[string]*capturedPayload
	bytes   int
	dropped uint64
	erased  uint64
}

type capturedPayload struct {
	data []byte
	hash string
}

// NewCaptureSink 创建 capture sink。ProjectionTargetID 与 primary 隔离，
// 由 route 配置给定。
func NewCaptureSink(projectionTargetID string, opts CaptureOptions) *CaptureSink {
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = 256
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 1 << 20
	}
	c := &CaptureSink{
		desc: TargetDescriptor{
			SinkID:             "capture",
			Class:              TargetClassCapture,
			ProjectionTargetID: projectionTargetID,
		},
		opts:  opts,
		clock: SystemClock{},
		state: SinkLifecycleOpen,
		store: make(map[string]*capturedPayload),
	}
	return c
}

func (c *CaptureSink) Descriptor() TargetDescriptor { return c.desc }

// SetClock 注入时钟（测试用；默认 SystemClock）。
func (c *CaptureSink) SetClock(clk Clock) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if clk != nil {
		c.clock = clk
	}
}

func (c *CaptureSink) Snapshot() SinkSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return SinkSnapshot{
		Descriptor:     c.desc,
		State:          c.state,
		OwnerSessionID: c.owner,
		RetainedBytes:  c.bytes,
	}
}

func (c *CaptureSink) Abort(AbortProof) error { return nil }

func (c *CaptureSink) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = SinkLifecycleClosed
	return nil
}

// SetOwner 由 route/connection 设置 owner session。
func (c *CaptureSink) SetOwner(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.owner = sessionID
}

// SubmitMirror 只保留 policy=best_effort/attempted 且 primary committed 的
// batch（outcome-aware 由 gateway 决定后才会调用本方法）。
func (c *CaptureSink) SubmitMirror(_ context.Context, env MirrorEnvelope) MirrorSinkResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	// capture 的“接受”是对整批 delivery 的记录（元数据/hash/full 都是完整
	// 成功；保留策略只影响 RetainedBytes，不影响接受），因此 proof 统一：
	// status=applied、certainty=full、AcceptedBytes=len。
	res := MirrorSinkResult{
		Status:         MirrorApplied,
		ErrorClass:     DeliveryErrorNone,
		AttemptedBytes: len(env.Bytes),
		AcceptedBytes:  len(env.Bytes),
	}
	// 淘汰过期。
	if c.opts.TTL > 0 {
		now := c.clock.Now()
		kept := c.entries[:0]
		for _, e := range c.entries {
			if !e.ExpiresAt.IsZero() && e.ExpiresAt.Before(now) {
				c.evaporateLocked(e.CaptureEntryID)
				c.erased++
				continue
			}
			kept = append(kept, e)
		}
		c.entries = kept
	}
	// 容量淘汰（最旧）。
	entryID := randomID("ce")
	mode := RecordedMetadataOnly
	if c.opts.StorePayload {
		mode = RecordedFullAvailable
	}
	hash := sha256.Sum256(env.Bytes)
	hashStr := hex.EncodeToString(hash[:])
	if c.opts.Redact != nil && c.opts.Redact(env.Bytes) {
		// 敏感内容：保留 hash + 元数据，不保留字节原文；仍算整批接受。
		mode = RecordedHashOnly
	} else if c.opts.StorePayload {
		// 预算控制。
		allow := c.opts.MaxBytes - c.bytes
		if len(env.Bytes) > allow {
			// 最旧淘汰直到放得下。
			for len(env.Bytes) > c.opts.MaxBytes-c.bytes && len(c.entries) > 0 {
				c.evaporateLocked(c.entries[0].CaptureEntryID)
				c.dropped++
			}
		}
		if len(env.Bytes) <= c.opts.MaxBytes-c.bytes {
			c.store[entryID] = &capturedPayload{data: env.Bytes, hash: hashStr}
			c.bytes += len(env.Bytes)
			mode = RecordedFullAvailable
		} else {
			// 预算不足：不保留 payload，降级为 hash-only（记录元数据 + hash，
			// 不声明 full proof）。
			mode = RecordedHashOnly
			c.store[entryID] = &capturedPayload{hash: hashStr}
		}
	}
	entry := CapturedDelivery{
		CaptureEntryID:     entryID,
		BatchID:            env.BatchID,
		RouteEpoch:         env.RouteEpoch,
		ProjectionTargetID: c.desc.ProjectionTargetID,
		Transaction:        env.Kind,
		Mode:               mode,
		BytesLength:        len(env.Bytes),
		Hash:               hashStr,
		CapturedAt:         c.clock.Now(),
	}
	if c.opts.TTL > 0 {
		entry.ExpiresAt = entry.CapturedAt.Add(c.opts.TTL)
	}
	c.entries = append(c.entries, entry)
	for len(c.entries) > c.opts.MaxEntries {
		c.evaporateLocked(c.entries[0].CaptureEntryID)
		c.dropped++
	}
	res.Target = &TargetReceipt{
		SessionID:          env.SessionID,
		Sequence:           env.Sequence,
		BatchID:            env.BatchID,
		RouteEpoch:         env.RouteEpoch,
		SinkID:             c.desc.SinkID,
		TargetClass:        c.desc.Class,
		ProjectionTargetID: c.desc.ProjectionTargetID,
		InvocationID:       0,
		SinkDeliveryResult: SinkDeliveryResult{
			Status:         DeliveryCommitted,
			Certainty:      WriteCertaintyFull,
			ErrorClass:     DeliveryErrorNone,
			AttemptedBytes: len(env.Bytes),
			AcceptedBytes:  res.AcceptedBytes,
		},
		CallbackReturned: true,
		StartedAt:        entry.CapturedAt,
		FinishedAt:       entry.CapturedAt,
		OutcomeFixedAt:   entry.CapturedAt,
	}
	return res
}

func (c *CaptureSink) evaporateLocked(entryID string) {
	if p, ok := c.store[entryID]; ok {
		c.bytes -= len(p.data)
		delete(c.store, entryID)
	}
	for i, e := range c.entries {
		if e.CaptureEntryID == entryID {
			c.entries = append(c.entries[:i], c.entries[i+1:]...)
			break
		}
	}
}

// Entries 返回当前保留条目（detached）。
func (c *CaptureSink) Entries() []CapturedDelivery {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CapturedDelivery, len(c.entries))
	copy(out, c.entries)
	return out
}

// Payload 返回指定条目 payload（detached）；NotFound 返回
// CapturePayloadErrorNotFound。
func (c *CaptureSink) Payload(entryID string) ([]byte, CapturePayloadErrorClass) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.store[entryID]
	if !ok || p == nil {
		return nil, CapturePayloadErrorNotFound
	}
	if len(p.data) == 0 {
		return nil, CapturePayloadErrorNotFound // hash-only 无 payload 访问
	}
	out := make([]byte, len(p.data))
	copy(out, p.data)
	return out, CapturePayloadErrorNone
}
