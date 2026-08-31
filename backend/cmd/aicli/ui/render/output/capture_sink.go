package output

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// ============================================================================
// CaptureSink（7.2）：semantic/wire capture + bounded journal
// ============================================================================

// CapturedDelivery 是 capture 中一笔已保留的 delivery（7.2 B）。
// SchemaVersion 第一版为 1。SinkID 必须等于 CaptureSnapshot.Sink.Descriptor
// 的 SinkID 且 class 必须是 TargetCapture。capture 作为 primary 时
// MirrorEntryID 为空、MirrorIndex 不参与 identity、ObservedPrimaryTargetID
// 等于自身 ProjectionTargetID，policy/mode 使用零值；作为 mirror 时
// entry/index/policy/mode 与 envelope/admission/final receipt 一致。
type CapturedDelivery struct {
	SchemaVersion           uint32
	CaptureEntryID          string // payload store 内部 handle；只用于本 sink 定位，不对外暴露
	SessionID               string
	BatchID                 string
	Sequence                uint64
	RouteEpoch              uint64
	MirrorEntryID           string // primary capture 时为空
	MirrorIndex             int    // primary capture 时为零值且不参与 identity
	SinkID                  string
	TargetClass             TargetClass
	ProjectionTargetID      string
	ObservedPrimaryTargetID string // primary capture 时等于 ProjectionTargetID
	Policy                  MirrorPolicy
	RequestedApplyMode      MirrorApplyMode
	EffectiveApplyMode      MirrorApplyMode
	NonAuthoritative        bool
	Mode                    RecordedPayloadMode // full_available/hash_only/metadata_only/truncated
	BytesLength             int
	ContentHash             string // session-keyed 诊断 hash；非 archive checksum
	TruncationReason        string
	DroppedBytes            int
	Transaction             TransactionKind
	CapturedAt              time.Time
	ExpiresAt               time.Time
}

// CaptureSnapshot 是 capture store 的可观察快照（7.2 B）。SchemaVersion
// 第一版为 1；slice 必须 detached。ActiveHandleCount 只计数，绝不暴露
// handle ID。
type CaptureSnapshot struct {
	SchemaVersion      uint32
	SessionID          string
	Sink               SinkSnapshot
	FullCaptureEnabled bool
	Deliveries         []CapturedDelivery
	PayloadItems       int
	PayloadBytes       uint64
	ActiveHandleCount  int
	DroppedBatches     uint64
	DroppedBytes       uint64
	Retained           int
	MaxEntries         int
	MaxBytes           int
	Erased             uint64
}

// CaptureOptions 配置 capture store。
type CaptureOptions struct {
	MaxEntries   int
	MaxBytes     int               // 保留 payload 的字节预算
	TTL          time.Duration     // entries 过期时长；<=0 表示不过期
	StorePayload bool              // true = 保留 bytes（full_available）；false = hash_only
	Redact       func([]byte) bool // 返回 true 表示应重写/排除 payload（安全敏感）
	// KeyedHash 生成 session-keyed 诊断 hash。nil 时用公开 SHA-256 兜底
	//（仅诊断；生产应注入 session-ephemeral keyed hash，避免字典反推）。
	// 注意：KeyedHash/Redact 回调在 sink 持锁下执行，回调内不得调用本 sink
	// 的任何方法（会重入死锁）。
	KeyedHash func([]byte) string
	// MaxSingleBatch 是单 batch 上限（bytes）；超出走 truncation（DroppedBytes
	// + TruncationReason），不复用 gateway primary safety limit。<=0 不限。
	MaxSingleBatch int
	// PayloadHandleTTL 是显式 payload handle 的默认存活时长（10.4：带
	// purpose、大小限制和 TTL 的显式 handle）。<=0 用默认 60s。
	PayloadHandleTTL time.Duration
}

// CaptureSink 是有界 capture sink：镜像是自己的 target，有独立 maxEntries/
// maxBytes/TTL；按 cap 淘汰最旧条目不阻塞 delivery。作为 mirror 超限时降为
// metadata/hash 并增加 drop 计数，绝不改变 physical primary；作为 primary
// 时使用 strict capacity（无法满足声明模式就在触达存储前 zero Rejected）。
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
	// handles 是显式 payload 读取授权（10.4）；handleID 不暴露。
	handles      map[string]*payloadHandle
	handleTTL    time.Duration
	dropped      uint64 // 淘汰条目（含 ring 超限）批次计数
	droppedBytes uint64
	erased       uint64
	// journal ring：最近 N 个 batch 的 metadata（bounded journal，7.2 C）。
	// 与 entries 分离：entries 是保活条目（TTL 内），journal 是审计环。
	journal     []RecordedBatch
	journalCap  int
	journalDrop uint64
}

type capturedPayload struct {
	data             []byte
	hash             string
	nonAuthoritative bool // NonAuthoritative=true 的 payload 拒绝显式申请（7.2）
}

// payloadHandle 是一次显式 payload 读取的授权记录（10.4）。
// handleID 仅通过 AcquirePayload 返回；snapshot 只暴露计数。
type payloadHandle struct {
	id        string
	entryID   string
	purpose   string
	expiresAt time.Time
	revoked   bool
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
	if opts.KeyedHash == nil {
		opts.KeyedHash = defaultKeyedHash
	}
	journalCap := opts.MaxEntries
	if journalCap <= 0 {
		journalCap = 256
	}
	c := &CaptureSink{
		desc: TargetDescriptor{
			SinkID:             "capture",
			Class:              TargetClassCapture,
			ProjectionTargetID: projectionTargetID,
		},
		opts:       opts,
		clock:      SystemClock{},
		state:      SinkLifecycleOpen,
		store:      make(map[string]*capturedPayload),
		handles:    make(map[string]*payloadHandle),
		handleTTL:  opts.PayloadHandleTTL,
		journalCap: journalCap,
	}
	if c.handleTTL <= 0 {
		c.handleTTL = 60 * time.Second
	}
	return c
}

// defaultKeyedHash 是公开 SHA-256 的诊断兜底（不是 archive checksum）。
func defaultKeyedHash(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:])
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
		WriteCount:     uint64(len(c.entries)),
		LastSeenAt:     c.clock.Now(),
	}
}

// CaptureSnapshot 返回 7.2 B 完整可观察快照（detached）。
func (c *CaptureSink) CaptureSnapshot() CaptureSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	deliveries := make([]CapturedDelivery, len(c.entries))
	copy(deliveries, c.entries)
	return CaptureSnapshot{
		SchemaVersion:      SchemaVersion,
		SessionID:          c.owner,
		Sink:               SinkSnapshot{Descriptor: c.desc, State: c.state, OwnerSessionID: c.owner, RetainedBytes: c.bytes},
		FullCaptureEnabled: c.opts.StorePayload,
		Deliveries:         deliveries,
		PayloadItems:       len(c.store),
		PayloadBytes:       uint64(c.bytes),
		ActiveHandleCount:  c.activeHandleCountLocked(),
		DroppedBatches:     c.dropped + c.journalDrop,
		DroppedBytes:       c.droppedBytes,
		Retained:           c.bytes,
		MaxEntries:         c.opts.MaxEntries,
		MaxBytes:           c.opts.MaxBytes,
		Erased:             c.erased,
	}
}

// Journal 返回 bounded journal 中的 sanitized batch 元数据（detached）。
func (c *CaptureSink) Journal() []RecordedBatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]RecordedBatch, len(c.journal))
	copy(out, c.journal)
	return out
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

// SubmitMirror 作为 mirror 从 MirrorEnvelope 获取 primary outcome，返回
// 不含 identity/time 的 MirrorSinkResult；最终 mirror/target receipt 由
// gateway scheduler 盖章汇总（7.2 B）。
func (c *CaptureSink) SubmitMirror(_ context.Context, env MirrorEnvelope) MirrorSinkResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != SinkLifecycleOpen {
		return MirrorSinkResult{
			Status:     MirrorFailed,
			ErrorClass: DeliveryErrorClosed,
			Err:        NewClassifiedError(DeliveryErrorClosed, "capture sink closed"),
		}
	}
	res := MirrorSinkResult{
		Status:         MirrorApplied,
		ErrorClass:     DeliveryErrorNone,
		AttemptedBytes: len(env.Bytes),
		AcceptedBytes:  len(env.Bytes),
	}
	c.captureLocked(env, false)
	res.Target = c.avatarLocked(env.SessionID, env.Sequence, env.BatchID, env.RouteEpoch, len(env.Bytes))
	return res
}

// Submit 作为 primary 使用 strict capacity（7.2）：无法满足调用方声明的
// capture mode 就在触达存储前返回 zero Rejected，由 gateway 盖章为 target
// receipt；不能一边丢内容一边声称 wire capture committed。
func (c *CaptureSink) Submit(_ context.Context, batch RenderBatch) SinkDeliveryResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != SinkLifecycleOpen {
		return SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorClosed,
			AttemptedBytes: len(batch.Bytes),
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorClosed, "capture sink closed"),
		}
	}
	// strict capacity：声明 full 但放不下 → zero rejection，不丢内容。
	if c.opts.StorePayload &&
		(c.opts.MaxSingleBatch > 0 && len(batch.Bytes) > c.opts.MaxSingleBatch) ||
		(len(batch.Bytes) > c.opts.MaxBytes) {
		return SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorOversized,
			AttemptedBytes: len(batch.Bytes),
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorOversized, "capture primary exceeds strict capacity"),
		}
	}
	env := MirrorEnvelope{
		MirrorEntryRef: MirrorEntryRef{
			EntryID:            "",
			MirrorIndex:        0,
			SinkID:             c.desc.SinkID,
			TargetClass:        c.desc.Class,
			ProjectionTargetID: c.desc.ProjectionTargetID,
		},
		RenderBatch:      batch,
		NonAuthoritative: false,
	}
	c.captureLocked(env, true)
	return SinkDeliveryResult{
		Status:         DeliveryCommitted,
		Certainty:      WriteCertaintyFull,
		ErrorClass:     DeliveryErrorNone,
		AttemptedBytes: len(batch.Bytes),
		AcceptedBytes:  len(batch.Bytes),
	}
}

// captureLocked 记录一笔 delivery（mirror 或 primary）；调用方持 c.mu。
// primary 时 entry 字段归零、ObservedPrimaryTargetID==ProjectionTargetID。
func (c *CaptureSink) captureLocked(env MirrorEnvelope, primary bool) {
	// 淘汰过期：先收集过期 id，再逐条蒸发（避免 kept/evaporateLocked
	// 共享底层数组导致重复或残留）。
	if c.opts.TTL > 0 {
		now := c.clock.Now()
		var expired []string
		for _, e := range c.entries {
			if !e.ExpiresAt.IsZero() && e.ExpiresAt.Before(now) {
				expired = append(expired, e.CaptureEntryID)
			}
		}
		for _, id := range expired {
			c.evaporateLocked(id)
			c.erased++
		}
	}
	// 容量淘汰（最旧）。
	entryID := randomID("ce")
	mode := RecordedMetadataOnly
	if c.opts.StorePayload {
		mode = RecordedFullAvailable
	}
	hashStr := c.opts.KeyedHash(env.Bytes) // 基于原文计算（含截断场景），与
	// BytesLength=len(env.Bytes) 一致：记录"收到了什么"（诊断 hash）。
	// 单 batch 上限：超限截断（记录 metadata + 前缀，标 truncation）。
	truncated := false
	keptLen := len(env.Bytes)
	if c.opts.MaxSingleBatch > 0 && keptLen > c.opts.MaxSingleBatch {
		truncated = true
		keptLen = c.opts.MaxSingleBatch
	}
	if c.opts.Redact != nil && c.opts.Redact(env.Bytes) {
		// 敏感内容：保留 hash + 元数据，不保留字节原文；仍算整批接受。
		mode = RecordedHashOnly
	} else if c.opts.StorePayload {
		// 预算控制：先淘汰最旧直到放得下。
		allow := c.opts.MaxBytes - c.bytes
		if keptLen > allow {
			for keptLen > c.opts.MaxBytes-c.bytes && len(c.entries) > 0 {
				// 先取字节数再删，避免 off-by-one/空 entries panic。
				evicted := c.entries[0].BytesLength
				c.evaporateLocked(c.entries[0].CaptureEntryID)
				c.dropped++
				c.droppedBytes += uint64(evicted)
			}
		}
		if keptLen <= c.opts.MaxBytes-c.bytes {
			if truncated {
				payload := env.Bytes[:keptLen]
				c.store[entryID] = &capturedPayload{data: payload, hash: hashStr, nonAuthoritative: env.NonAuthoritative}
				c.bytes += keptLen
				mode = RecordedTruncated
			} else {
				c.store[entryID] = &capturedPayload{data: env.Bytes, hash: hashStr, nonAuthoritative: env.NonAuthoritative}
				c.bytes += len(env.Bytes)
				mode = RecordedFullAvailable
			}
		} else {
			// 预算不足：不保留 payload，降级为 hash-only（记录元数据 + hash，
			// 不声明 full proof）。
			mode = RecordedHashOnly
			c.store[entryID] = &capturedPayload{hash: hashStr, nonAuthoritative: env.NonAuthoritative}
		}
	}
	capAt := c.clock.Now()
	entry := CapturedDelivery{
		SchemaVersion:      SchemaVersion,
		CaptureEntryID:     entryID,
		SessionID:          env.SessionID,
		BatchID:            env.BatchID,
		Sequence:           env.Sequence,
		RouteEpoch:         env.RouteEpoch,
		SinkID:             c.desc.SinkID,
		TargetClass:        c.desc.Class,
		ProjectionTargetID: c.desc.ProjectionTargetID,
		Policy:             env.Policy,
		RequestedApplyMode: env.RequestedApplyMode,
		EffectiveApplyMode: env.EffectiveApplyMode,
		NonAuthoritative:   env.NonAuthoritative,
		Mode:               mode,
		BytesLength:        len(env.Bytes),
		ContentHash:        hashStr,
		Transaction:        env.Kind,
		CapturedAt:         capAt,
	}
	if primary {
		entry.MirrorEntryID = ""
		entry.MirrorIndex = 0
		entry.ObservedPrimaryTargetID = c.desc.ProjectionTargetID
		entry.Policy = ""
		entry.RequestedApplyMode = ""
		entry.EffectiveApplyMode = ""
		entry.NonAuthoritative = false
	} else {
		entry.MirrorEntryID = env.EntryID
		entry.MirrorIndex = env.MirrorIndex
		entry.ObservedPrimaryTargetID = env.MirrorEntryRef.ProjectionTargetID
	}
	if truncated {
		entry.TruncationReason = "single_batch_limit"
		entry.DroppedBytes = len(env.Bytes) - keptLen
	}
	if c.opts.TTL > 0 {
		entry.ExpiresAt = capAt.Add(c.opts.TTL)
	}
	c.entries = append(c.entries, entry)
	for len(c.entries) > c.opts.MaxEntries {
		// 先取字节数再删（MaxEntries=1 时也正确）。
		evicted := c.entries[0].BytesLength
		c.evaporateLocked(c.entries[0].CaptureEntryID)
		c.dropped++
		c.droppedBytes += uint64(evicted)
	}
	// bounded journal（7.2 C）：session-local ring，超限丢旧观察记录。
	c.journal = append(c.journal, SanitizedBatch(env.RenderBatch, RecordedMetadataOnly, nil))
	for len(c.journal) > c.journalCap {
		c.journal = c.journal[1:]
		c.journalDrop++
	}
}

// avatarLocked 构造 mirror snake（capture 自身 target receipt 简明形态）。
func (c *CaptureSink) avatarLocked(sessionID string, seq uint64, batchID string, epoch uint64, bytesLen int) *TargetReceipt {
	now := c.clock.Now()
	return &TargetReceipt{
		SessionID:          sessionID,
		Sequence:           seq,
		BatchID:            batchID,
		RouteEpoch:         epoch,
		SinkID:             c.desc.SinkID,
		TargetClass:        c.desc.Class,
		ProjectionTargetID: c.desc.ProjectionTargetID,
		InvocationID:       0,
		SinkDeliveryResult: SinkDeliveryResult{
			Status:         DeliveryCommitted,
			Certainty:      WriteCertaintyFull,
			ErrorClass:     DeliveryErrorNone,
			AttemptedBytes: bytesLen,
			AcceptedBytes:  bytesLen,
		},
		CallbackReturned: true,
		StartedAt:        now,
		FinishedAt:       now,
		OutcomeFixedAt:   now,
	}
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

// AcquirePayload 按 CapturePayloadAccess 显式申请 payload 读取（10.4/7.2）：
// 带 purpose（authorizer 校验）、LimitBytes 大小限制与 TTL 的 handle。
// 语义：
//   - authorizer 非 nil 时先 Authorize；拒绝返回 CapturePayloadErrorUnauthorized；
//   - entry 不存在或 hash-only → CapturePayloadErrorNotFound；
//   - NonAuthoritative=true 的 payload 即使完整存在也拒绝（7.2，不提供
//     attempted bytes 的权威读取）；
//   - LimitBytes>0 且超出时截断，Mode=RecordedTruncated；
//   - 返回 detached handle（含 ExpiresAt）；读取经 PayloadHandle(handleID)。
func (c *CaptureSink) AcquirePayload(ctx context.Context, request CapturePayloadRequest, authorizer CapturePayloadAuthorizer) CapturePayloadHandle {
	if ctx == nil {
		ctx = context.Background()
	}
	if authorizer != nil {
		if err := authorizer.Authorize(ctx, request); err != nil {
			return CapturePayloadHandle{
				EntryID:    request.CaptureEntryID,
				ErrorClass: CapturePayloadErrorUnauthorized,
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.store[request.CaptureEntryID]
	if !ok || p == nil || len(p.data) == 0 {
		return CapturePayloadHandle{
			EntryID:    request.CaptureEntryID,
			ErrorClass: CapturePayloadErrorNotFound,
		}
	}
	if p.nonAuthoritative {
		// 7.2：NonAuthoritative 的 attempted bytes 不提供权威读取。
		return CapturePayloadHandle{
			EntryID:    request.CaptureEntryID,
			ErrorClass: CapturePayloadErrorUnauthorized,
		}
	}
	mode := RecordedFullAvailable
	payload := p.data
	if request.LimitBytes > 0 && len(payload) > request.LimitBytes {
		payload = payload[:request.LimitBytes]
		mode = RecordedTruncated
	}
	c.evictExpiredHandlesLocked()
	handleID := randomID("ph")
	h := &payloadHandle{
		id:        handleID,
		entryID:   request.CaptureEntryID,
		purpose:   string(request.Access),
		expiresAt: c.clock.Now().Add(c.handleTTL),
	}
	c.handles[handleID] = h
	out := CapturePayloadHandle{
		HandleID:    handleID,
		EntryID:     request.CaptureEntryID,
		Mode:        mode,
		BytesLength: len(payload),
		Hash:        p.hash,
		Payload:     make([]byte, len(payload)),
		ExpiresAt:   h.expiresAt,
		ErrorClass:  CapturePayloadErrorNone,
	}
	copy(out.Payload, payload)
	return out
}

// PayloadHandle 按 handleID 读取已授权 payload；handle 过期/吊销返回稳定
// 错误（不退回 journal，10.4）。读取不延长 TTL。
func (c *CaptureSink) PayloadHandle(handleID string) ([]byte, CapturePayloadErrorClass) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredHandlesLocked()
	h, ok := c.handles[handleID]
	if !ok {
		return nil, CapturePayloadErrorNotFound
	}
	if h.revoked {
		return nil, CapturePayloadErrorRevoked
	}
	if !h.expiresAt.After(c.clock.Now()) {
		delete(c.handles, handleID)
		return nil, CapturePayloadErrorNotFound
	}
	if c.store[h.entryID] == nil {
		// entry 已被淘汰/蒸发：handle 失效，不退回 journal。
		delete(c.handles, handleID)
		return nil, CapturePayloadErrorNotFound
	}
	out := make([]byte, len(c.store[h.entryID].data))
	copy(out, c.store[h.entryID].data)
	return out, CapturePayloadErrorNone
}

// RevokePayload 吊销 handle；此后 PayloadHandle 返回 Revoked（10.4）。
func (c *CaptureSink) RevokePayload(handleID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.handles[handleID]; ok {
		h.revoked = true
	}
}

// evictExpiredHandlesLocked 清理过期 handle；计数随 ActiveHandleCount
// （snapshot）线性缩减。调用者必须已持 c.mu。
func (c *CaptureSink) evictExpiredHandlesLocked() {
	now := c.clock.Now()
	for id, h := range c.handles {
		if !h.expiresAt.After(now) {
			delete(c.handles, id)
		}
	}
}

// activeHandleCount 当前未过期、未吊销的 handle 数。
func (c *CaptureSink) activeHandleCountLocked() int {
	c.evictExpiredHandlesLocked()
	n := 0
	for _, h := range c.handles {
		if !h.revoked {
			n++
		}
	}
	return n
}
