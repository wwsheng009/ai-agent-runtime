package schedule

import (
	"strings"
	"sync"
	"time"
)

// Clock 返回当前墙钟时间（可注入以便测试）。
type Clock func() time.Time

// SystemClock 是生产用时钟。
func SystemClock() time.Time { return time.Now() }

// CatalogEntry 是跨会话目录条目：记录 + 原会话绑定 + 生命周期 + 最近回执。
type CatalogEntry struct {
	Record
	SessionID string `json:"sessionId"`
	Status    Status `json:"status"`
	Origin    Origin `json:"origin,omitempty"`
	// J1/J3：执行上下文与创建者（前端展示 / 归属）。
	TargetKind      TargetKind `json:"targetKind,omitempty"`
	TargetWorkspace string     `json:"targetWorkspace,omitempty"`
	TargetProfile   string     `json:"targetProfile,omitempty"`
	// CreatedBySessionID lets management surfaces act on non-session targets
	// (headless/workspace) without pretending they are bound to a session.
	CreatedBySessionID string           `json:"createdBySessionId,omitempty"`
	LastDelivery       *DeliveryReceipt `json:"lastDelivery,omitempty"`
}

// DeleteResult 是删除结果（含非变更的未找到）。
type DeleteResult struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
	Code    Code   `json:"code,omitempty"`
}

// UpdateResult 是更新结果（含非变更的未命中/冲突）。
type UpdateResult struct {
	ID      string  `json:"id"`
	Updated bool    `json:"updated"`
	Record  *Record `json:"record,omitempty"`
	Status  Status  `json:"status,omitempty"`
	Code    Code    `json:"code,omitempty"`
	Message string  `json:"message,omitempty"`
}

// HistoryRequest 是一次有界的历史查询。
type HistoryRequest struct {
	SessionID string
	ID        string
	Limit     int
	Before    string // message id 游标（排他）
}

// HistoryResult 是历史查询结果：记录从新到旧，附游标与保留上限。
type HistoryResult struct {
	ID                        string           `json:"id"`
	Records                   []DeliveryRecord `json:"records"`
	NextBefore                string           `json:"nextBefore,omitempty"`
	EarlierRecordsUnavailable bool             `json:"earlierRecordsUnavailable"`
	EarlierRecordsPruned      bool             `json:"earlierRecordsPruned"`
	RetentionDays             int              `json:"retentionDays"`
	RetentionRecords          int              `json:"retentionRecords"`
}

// Service 是共享管理服务：读、删除与时间编辑都不激活会话。
//
// 所有写经同一互斥队列串行化（单写者），保证"读-改-写"与投递推进不交错。
type Service struct {
	store Store
	clock Clock
	// notify 在每次任务变更提交后调用（schedule.changed 通知；nil = no-op）。
	// 发布失败绝不影响管理写本身（通知不是事务参与者）。
	notify func()
	// hostRuntime 是宿主侧兜底消费运行时（缺口 1：ActivateSession 消费
	// daemon 落库的 pending 提醒）；nil = 宿主未注册（pending 只能由 daemon 消费）。
	hostRuntime *Runtime
	// driverWaker 在每次任务变更提交后唤醒调度驱动（S2 修复）：空闲期创建的
	// 任务不会自动进入 driver 的定时器重算，需要一次显式 RequestDrive。
	driverWakerMu sync.RWMutex
	driverWaker   func()

	// notifyMu 保护 notify 的装配期写入与运行期读取。**绝不能与 mu 混用**：
	// Create/Update/Delete 持有 mu 期间调用 emitChanged，若 emitChanged 再取
	// mu 会自死锁（曾致测试挂死）。
	notifyMu sync.RWMutex
	mu       sync.Mutex
}

// NewService 构造管理服务。
func NewService(store Store, clock Clock) *Service {
	if clock == nil {
		clock = SystemClock
	}
	return &Service{store: store, clock: clock}
}

// SetNotifier 注入 schedule.changed 发布回调（宿主在装配时接入 EventBus）。
func (s *Service) SetNotifier(notify func()) {
	if notify == nil {
		notify = nopPublisher
	}
	s.notifyMu.Lock()
	s.notify = notify
	s.notifyMu.Unlock()
}

// SetDriverWaker 注册"任务变更后唤醒调度驱动"回调（nil 清除）。
//
// S2b 实测发现：driver 的定时器只在 drive() 末尾按"最近目标"重排；空闲期
// （无任何 active 任务）创建的新任务不会触发重排，提醒要等下一次 drive 才被
// 看见。控制面每次变更提交后经此回调唤醒驱动（实现为 RequestDrive 的合并
// 语义：非阻塞、可重入）。
func (s *Service) SetDriverWaker(wake func()) {
	s.driverWakerMu.Lock()
	s.driverWaker = wake
	s.driverWakerMu.Unlock()
}

// emitChanged 发布一次变更通知（同步监听器失败只记日志，不影响调用方）。
func (s *Service) emitChanged() {
	s.notifyMu.RLock()
	notify := s.notify
	s.notifyMu.RUnlock()
	s.driverWakerMu.RLock()
	wake := s.driverWaker
	s.driverWakerMu.RUnlock()
	if notify == nil && wake == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			// 同步监听器 panic 不得打断管理写；调用方（宿主）自行记日志。
			_ = r
		}
	}()
	if notify != nil {
		notify()
	}
	if wake != nil {
		// 唤醒必须非阻塞（RequestDrive 自带合并语义；panic 由上方 recover 兜底）。
		wake()
	}
}

// Store 返回底层存储（供运行时读写）。
func (s *Service) Store() Store { return s.store }

// Runtime 返回宿主侧兜底消费运行时（drain pending 用）；未注册时为 nil。
func (s *Service) Runtime() *Runtime { return s.hostRuntime }

// SetRuntime 注册宿主侧兜底消费运行时。
func (s *Service) SetRuntime(rt *Runtime) { s.hostRuntime = rt }

// Now 返回当前时钟读数。
func (s *Service) Now() time.Time { return s.clock() }

// WithLock 在单写者队列内执行 work（运行时投递提交用）。
func (s *Service) WithLock(work func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return work()
}

// Create 创建一条绑定到 sessionID 的提醒（不激活会话）。
// origin 标记创建来源：空值回落 model（模型工具路径保持兼容）。
func (s *Service) Create(sessionID string, req CreateRequest, origin Origin) (Record, error) {
	selectors := 0
	if req.AfterSeconds != nil {
		selectors++
	}
	if req.At != nil {
		selectors++
	}
	if req.EverySeconds != nil {
		selectors++
	}
	if req.Daily != nil {
		selectors++
	}
	if req.Weekly != nil {
		selectors++
	}
	if req.Cron != nil {
		selectors++
	}
	if selectors != 1 {
		return Record{}, inputError(CodeInvalidSelector,
			"exactly one of after_seconds, at, every_seconds, daily, weekly, cron is required.")
	}
	id := NewID()
	now := s.clock()
	var (
		record Record
		err    error
	)
	switch {
	case req.AfterSeconds != nil:
		record, err = CreateAfter(id, req.Prompt, req.Title, *req.AfterSeconds, now)
	case req.At != nil:
		record, err = CreateAt(id, req.Prompt, req.Title, *req.At, now)
	case req.EverySeconds != nil:
		record, err = CreateEvery(id, req.Prompt, req.Title, *req.EverySeconds, now)
	case req.Daily != nil:
		record, err = CreateDaily(id, req.Prompt, req.Title, *req.Daily, now)
	case req.Weekly != nil:
		record, err = CreateWeekly(id, req.Prompt, req.Title, *req.Weekly, now)
	case req.Cron != nil:
		record, err = CreateCron(id, req.Prompt, req.Title, *req.Cron, now)
	}
	if err != nil {
		return Record{}, err
	}
	// J0：作业通用化字段（全部可选；空则保持现状语义）。
	record.Goal = strings.TrimSpace(req.Goal)
	record.Plan = normalizePlan(req.Plan)
	// J1：执行上下文决定会话绑定——非 session 目标不绑定会话（创建者回投属 J3）。
	targetKind := NormalizeTargetKind(string(req.TargetKind))
	if targetKind == TargetWorkspace && strings.TrimSpace(req.TargetWorkspace) == "" {
		return Record{}, inputError(CodeInvalidRule, "target_workspace is required when target_kind=workspace.")
	}
	// J4：非会话作业没有既有会话承载语义，必须有目标（goal）才成立。
	if targetKind != TargetSession && strings.TrimSpace(req.Goal) == "" {
		return Record{}, inputError(CodeInvalidRule, "goal is required when target_kind is workspace or headless.")
	}
	boundSession := strings.TrimSpace(sessionID)
	if targetKind != TargetSession {
		boundSession = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task := Task{
		SessionID:       boundSession,
		Record:          record,
		Status:          StatusActive,
		Origin:          origin,
		TargetKind:      targetKind,
		TargetWorkspace: strings.TrimSpace(req.TargetWorkspace),
		TargetProfile:   strings.TrimSpace(req.TargetProfile),
		// J3：保留创建者会话（非 session 目标也记，用于结果回投）。
		CreatedBySessionID: strings.TrimSpace(sessionID),
		DeliveryHistory:    &DeliveryHistory{Records: []DeliveryRecord{}},
	}
	if err := s.store.Put(task); err != nil {
		return Record{}, err
	}
	// Return the persisted projection: SQLite rounds timestamps to the storage
	// precision, and management clients use this record as the next CAS
	// expectation. Returning the pre-store nanosecond value would make an
	// immediate edit fail with a spurious schedule_conflict.
	if stored, ok, err := s.store.Get(record.ID); err == nil && ok {
		record = stored.Record
	}
	s.emitChanged()
	return record, nil
}

// List 返回某会话的活动任务。
func (s *Service) List(sessionID string) ([]Record, error) {
	tasks, err := s.store.ListActiveBySession(sessionID)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(tasks))
	for _, task := range tasks {
		records = append(records, task.Record)
	}
	return records, nil
}

// Catalog 返回全部任务（活动 + 未运行）及其原会话绑定。
func (s *Service) Catalog() ([]CatalogEntry, error) {
	tasks, err := s.store.List()
	if err != nil {
		return nil, err
	}
	entries := make([]CatalogEntry, 0, len(tasks))
	for _, task := range tasks {
		entries = append(entries, CatalogEntry{
			Record: task.Record, SessionID: task.SessionID, Status: task.Status, Origin: task.Origin,
			TargetKind: task.TargetKind, TargetWorkspace: task.TargetWorkspace, TargetProfile: task.TargetProfile,
			CreatedBySessionID: task.CreatedBySessionID, LastDelivery: task.LastDelivery,
		})
	}
	return entries, nil
}

// ActiveTasks 返回某会话的活动任务记录（归档准入用）。
func (s *Service) ActiveTasks(sessionID string) ([]Record, error) {
	return s.List(sessionID)
}

// Get 读取一条属于 sessionID 的记录（工具捕获 expected 用）。
func (s *Service) Get(sessionID, id string) (Record, bool, error) {
	task, ok, err := s.store.Get(id)
	if err != nil || !ok || task.SessionID != sessionID {
		return Record{}, false, err
	}
	return task.Record, true, nil
}

// History 读取已保存投递记录：limit 1..100；before 为 message id 排他游标。
func (s *Service) History(request HistoryRequest) (HistoryResult, error) {
	if request.Limit < 1 || request.Limit > 100 {
		return HistoryResult{}, inputError(CodeInvalidRule, "limit must be a safe integer from 1 through 100.")
	}
	task, ok, err := s.store.Get(request.ID)
	if err != nil {
		return HistoryResult{}, err
	}
	if !ok || task.SessionID != request.SessionID {
		return HistoryResult{}, inputError(CodeNotFound, "schedule_not_found")
	}
	var beforeSeq int64
	if strings.TrimSpace(request.Before) != "" {
		sqlStore, isSQL := s.store.(*SQLiteStore)
		if !isSQL {
			return HistoryResult{}, inputError(CodeCursorNotFound, "delivery_cursor_not_found")
		}
		seq, found, err := sqlStore.FindHistorySeqByMessage(request.ID, request.Before)
		if err != nil {
			return HistoryResult{}, err
		}
		if !found {
			return HistoryResult{}, inputError(CodeCursorNotFound, "delivery_cursor_not_found")
		}
		beforeSeq = seq
	}
	sqlStore, isSQL := s.store.(*SQLiteStore)
	if !isSQL {
		return HistoryResult{ID: request.ID, Records: []DeliveryRecord{}}, nil
	}
	records, err := sqlStore.ListHistoryPage(request.ID, beforeSeq, request.Limit)
	if err != nil {
		return HistoryResult{}, err
	}
	result := HistoryResult{
		ID:      request.ID,
		Records: records,
	}
	if task.DeliveryHistory != nil {
		result.EarlierRecordsUnavailable = task.DeliveryHistory.EarlierRecordsUnavailable
		result.EarlierRecordsPruned = task.DeliveryHistory.EarlierRecordsPruned
	}
	if len(records) == request.Limit {
		// 追加序持续递增：以本页最早记录的 message id 为下一页游标。
		oldest := records[len(records)-1]
		result.NextBefore = oldest.MessageID
	}
	return result, nil
}

// JobRuns 返回某作业的运行历史（J3；新到旧，limit 有界）。
//
// 归属校验：绑定会话或创建者会话匹配即可（headless 作业无绑定会话，但有创建者）。
func (s *Service) JobRuns(sessionID, id string, limit int) ([]JobRun, error) {
	task, ok, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, inputError(CodeNotFound, "schedule_not_found")
	}
	owner := strings.TrimSpace(sessionID)
	if owner != "" && task.SessionID != owner && task.CreatedBySessionID != owner {
		return nil, inputError(CodeNotFound, "schedule_not_found")
	}
	sqlStore, isSQL := s.store.(*SQLiteStore)
	if !isSQL {
		return []JobRun{}, nil
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return sqlStore.ListJobRuns(id, limit)
}

// Delete 删除一条属于 sessionID 的任务（硬删除，含历史）。
func (s *Service) Delete(sessionID, id string) (DeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok, err := s.store.Get(id)
	if err != nil {
		return DeleteResult{}, err
	}
	if !ok || !taskOwnedBy(task, sessionID) {
		return DeleteResult{ID: id, Deleted: false, Code: CodeNotFound}, nil
	}
	deleted, err := s.store.Delete(id)
	if err != nil {
		return DeleteResult{}, err
	}
	if !deleted {
		return DeleteResult{ID: id, Deleted: false, Code: CodeNotFound}, nil
	}
	s.emitChanged()
	return DeleteResult{ID: id, Deleted: true}, nil
}

// Update 以完整 expected 记录比较并原地修改名称、指令与时间。
func (s *Service) Update(sessionID, id string, expected Record, content UpdateContent, change *TimingChange) (UpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok, err := s.store.Get(id)
	if err != nil {
		return UpdateResult{}, err
	}
	if !ok || !taskOwnedBy(current, sessionID) {
		return UpdateResult{ID: id, Updated: false, Code: CodeNotFound}, nil
	}
	if current.Status != StatusActive && current.Status != StatusPaused {
		return UpdateResult{ID: id, Updated: false, Code: CodeEnded}, nil
	}
	if !recordsEqual(current.Record, expected) {
		return UpdateResult{ID: id, Updated: false, Code: CodeConflict}, nil
	}
	title := current.Record.Title
	if content.Title != nil {
		validated, err := ScheduleTitle(*content.Title)
		if err != nil {
			return updateInputFailure(id, err)
		}
		title = validated
	}
	prompt := current.Record.Prompt
	if content.Prompt != nil {
		validated, err := SchedulePrompt(*content.Prompt)
		if err != nil {
			return updateInputFailure(id, err)
		}
		prompt = validated
	}
	updated := current.Record
	if change != nil {
		replaced, err := applyTimingChange(current.Record, prompt, title, change, s.clock())
		if err != nil {
			return updateInputFailure(id, err)
		}
		updated = replaced
	} else {
		updated.Title = title
		updated.Prompt = prompt
	}
	if recordsEqual(updated, current.Record) {
		record := current.Record
		return UpdateResult{ID: id, Updated: false, Record: &record, Status: current.Status}, nil
	}
	next := current
	next.Record = updated
	applied, err := s.store.UpdateCAS(next, current.StateVersion)
	if err != nil {
		return UpdateResult{}, err
	}
	if !applied {
		return UpdateResult{ID: id, Updated: false, Code: CodeConflict}, nil
	}
	s.emitChanged()
	return UpdateResult{ID: id, Updated: true, Record: &updated, Status: current.Status}, nil
}

// Pause moves an active task to the non-terminal paused state. Paused tasks
// are ignored by the due scanner but keep their committed target; resuming
// before that target preserves the original schedule.
func (s *Service) Pause(sessionID, id string) (UpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok, err := s.store.Get(id)
	if err != nil {
		return UpdateResult{}, err
	}
	if !ok || !taskOwnedBy(current, sessionID) {
		return UpdateResult{ID: id, Updated: false, Code: CodeNotFound}, nil
	}
	switch current.Status {
	case StatusPaused:
		record := current.Record
		return UpdateResult{ID: id, Updated: false, Record: &record, Status: StatusPaused}, nil
	case StatusActive:
		// Continue below.
	default:
		return UpdateResult{ID: id, Updated: false, Status: current.Status, Code: CodeEnded,
			Message: "ended tasks cannot be paused"}, nil
	}
	next := current
	next.Status = StatusPaused
	applied, err := s.store.UpdateCAS(next, current.StateVersion)
	if err != nil {
		return UpdateResult{}, err
	}
	if !applied {
		return UpdateResult{ID: id, Updated: false, Status: current.Status, Code: CodeConflict}, nil
	}
	s.emitChanged()
	record := current.Record
	return UpdateResult{ID: id, Updated: true, Record: &record, Status: StatusPaused}, nil
}

// Resume makes a paused task schedulable again. If its committed target is
// still in the future it is preserved. Once the target has passed, recurring
// rules advance to a fresh future occurrence; one-shot "after" gets a fresh
// delay, while a passed "at" target must be edited explicitly (never silently
// move a user-specified wall-clock time).
func (s *Service) Resume(sessionID, id string) (UpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok, err := s.store.Get(id)
	if err != nil {
		return UpdateResult{}, err
	}
	if !ok || !taskOwnedBy(current, sessionID) {
		return UpdateResult{ID: id, Updated: false, Code: CodeNotFound}, nil
	}
	switch current.Status {
	case StatusActive:
		record := current.Record
		return UpdateResult{ID: id, Updated: false, Record: &record, Status: StatusActive}, nil
	case StatusPaused:
		// Continue below.
	default:
		return UpdateResult{ID: id, Updated: false, Status: current.Status, Code: CodeEnded,
			Message: "ended tasks cannot be resumed; create a new task instead"}, nil
	}
	now := s.clock().UTC()
	record, err := resumeScheduleRecord(current.Record, now)
	if err != nil {
		return updateInputFailure(id, err)
	}
	next := current
	next.Record = record
	next.Status = StatusActive
	// A manual resume is an explicit recovery action; old delivery failures
	// must not immediately dead-letter the task again.
	next.AttemptCount = 0
	next.LastAttemptAt = nil
	next.DeadLetteredAt = nil
	applied, err := s.store.UpdateCAS(next, current.StateVersion)
	if err != nil {
		return UpdateResult{}, err
	}
	if !applied {
		return UpdateResult{ID: id, Updated: false, Status: StatusPaused, Code: CodeConflict}, nil
	}
	s.emitChanged()
	return UpdateResult{ID: id, Updated: true, Record: &record, Status: StatusActive}, nil
}

func taskOwnedBy(task Task, sessionID string) bool {
	owner := strings.TrimSpace(sessionID)
	if owner == "" {
		return false
	}
	return task.SessionID == owner || task.CreatedBySessionID == owner
}

// resumeScheduleRecord advances a paused record to a safe future target.
func resumeScheduleRecord(rec Record, now time.Time) (Record, error) {
	now = now.UTC()
	if rec.ScheduledAt.After(now) {
		return rec, nil
	}
	switch rec.Kind {
	case KindAfter:
		if rec.AfterSeconds <= 0 {
			return Record{}, inputError(CodeInvalidRule, "after_seconds must be positive")
		}
		rec.ScheduledAt = now.Add(time.Duration(rec.AfterSeconds) * time.Second).UTC()
		return rec, nil
	case KindAt:
		return Record{}, inputError(CodeNotFuture,
			"the original at target has passed; edit the schedule time before resuming")
	case KindEvery:
		if rec.EverySeconds < MinEveryIntervalSeconds {
			return Record{}, inputError(CodeInvalidRule, "every_seconds is below the minimum interval")
		}
		rec.ScheduledAt = now.Add(time.Duration(rec.EverySeconds) * time.Second).UTC()
		return rec, nil
	case KindDaily, KindWeekly, KindCron:
		occurrence, err := ResolveOccurrence(rec, now)
		if err != nil {
			return Record{}, err
		}
		if occurrence.NextScheduledAt == nil {
			return Record{}, inputError(CodeTimeOutOfRange, "no representable future occurrence")
		}
		rec.ScheduledAt = occurrence.NextScheduledAt.UTC()
		return rec, nil
	default:
		return Record{}, inputError(CodeInvalidRule, "unsupported schedule kind")
	}
}

func updateInputFailure(id string, err error) (UpdateResult, error) {
	if input, ok := IsInputError(err); ok {
		return UpdateResult{ID: id, Updated: false, Code: input.Code, Message: input.Message}, nil
	}
	return UpdateResult{}, err
}

// applyTimingChange 计算新的记录（P0 支持 at/every；after 仅创建时可用）。
func applyTimingChange(current Record, prompt, title string, change *TimingChange, now time.Time) (Record, error) {
	switch change.Kind {
	case KindAt:
		if change.At == nil {
			return Record{}, inputError(CodeInvalidRule, "at timing change requires at.")
		}
		if current.Kind == KindAt && current.ScheduledAt.Equal(mustParseAtOrZero(*change.At)) {
			return withContent(current, title, prompt), nil
		}
		return CreateAt(current.ID, prompt, title, *change.At, now)
	case KindEvery:
		if change.EverySeconds == nil {
			return Record{}, inputError(CodeInvalidRule, "every timing change requires every_seconds.")
		}
		if current.Kind == KindEvery && *change.EverySeconds == current.EverySeconds {
			return withContent(current, title, prompt), nil
		}
		return CreateEvery(current.ID, prompt, title, *change.EverySeconds, now)
	case KindDaily:
		if change.Daily == nil {
			return Record{}, inputError(CodeInvalidRule, "daily timing change requires daily.")
		}
		created, err := CreateDaily(current.ID, prompt, title, *change.Daily, now)
		if err != nil {
			return Record{}, err
		}
		if current.Kind == KindDaily && created.Time == current.Time && created.TimeZone == current.TimeZone {
			return withContent(current, title, prompt), nil
		}
		return created, nil
	case KindWeekly:
		if change.Weekly == nil {
			return Record{}, inputError(CodeInvalidRule, "weekly timing change requires weekly.")
		}
		created, err := CreateWeekly(current.ID, prompt, title, *change.Weekly, now)
		if err != nil {
			return Record{}, err
		}
		if current.Kind == KindWeekly && created.Time == current.Time && created.TimeZone == current.TimeZone && intSlicesEqual(created.Weekdays, current.Weekdays) {
			return withContent(current, title, prompt), nil
		}
		return created, nil
	case KindCron:
		if change.Cron == nil {
			return Record{}, inputError(CodeInvalidRule, "cron timing change requires cron.")
		}
		created, err := CreateCron(current.ID, prompt, title, *change.Cron, now)
		if err != nil {
			return Record{}, err
		}
		if current.Kind == KindCron && created.Expression == current.Expression && created.TimeZone == current.TimeZone {
			return withContent(current, title, prompt), nil
		}
		return created, nil
	default:
		return Record{}, inputError(CodeInvalidRule, "unsupported timing change kind %q", change.Kind)
	}
}

func intSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func withContent(current Record, title, prompt string) Record {
	next := current
	next.Title = title
	next.Prompt = prompt
	return next
}

func mustParseAtOrZero(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

// normalizePlan 清洗执行步骤：去首尾空白、丢弃空项；全空返回 nil。
func normalizePlan(plan []string) []string {
	out := make([]string, 0, len(plan))
	for _, step := range plan {
		if trimmed := strings.TrimSpace(step); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func recordsEqual(a, b Record) bool {
	if a.ID != b.ID || a.Kind != b.Kind || a.Title != b.Title || a.Prompt != b.Prompt || a.Goal != b.Goal {
		return false
	}
	if !a.ScheduledAt.UTC().Equal(b.ScheduledAt.UTC()) {
		return false
	}
	if a.AfterSeconds != b.AfterSeconds || a.EverySeconds != b.EverySeconds {
		return false
	}
	if len(a.Plan) != len(b.Plan) {
		return false
	}
	for i := range a.Plan {
		if a.Plan[i] != b.Plan[i] {
			return false
		}
	}
	if a.Time != b.Time || a.TimeZone != b.TimeZone || a.Expression != b.Expression {
		return false
	}
	if len(a.Weekdays) != len(b.Weekdays) {
		return false
	}
	for i := range a.Weekdays {
		if a.Weekdays[i] != b.Weekdays[i] {
			return false
		}
	}
	return true
}
