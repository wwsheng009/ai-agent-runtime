package schedule

import (
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
	SessionID    string           `json:"sessionId"`
	Status       Status           `json:"status"`
	LastDelivery *DeliveryReceipt `json:"lastDelivery,omitempty"`
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
	Code    Code    `json:"code,omitempty"`
	Message string  `json:"message,omitempty"`
}

// Service 是共享管理服务：读、删除与时间编辑都不激活会话。
//
// 所有写经同一互斥队列串行化（单写者），保证"读-改-写"与投递推进不交错。
type Service struct {
	store Store
	clock Clock

	mu sync.Mutex
}

// NewService 构造管理服务。
func NewService(store Store, clock Clock) *Service {
	if clock == nil {
		clock = SystemClock
	}
	return &Service{store: store, clock: clock}
}

// Store 返回底层存储（供运行时读写）。
func (s *Service) Store() Store { return s.store }

// Now 返回当前时钟读数。
func (s *Service) Now() time.Time { return s.clock() }

// WithLock 在单写者队列内执行 work（运行时投递提交用）。
func (s *Service) WithLock(work func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return work()
}

// Create 创建一条绑定到 sessionID 的提醒（不激活会话）。
func (s *Service) Create(sessionID string, req CreateRequest) (Record, error) {
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
	if selectors != 1 {
		return Record{}, inputError(CodeInvalidSelector,
			"exactly one of after_seconds, at, every_seconds is required.")
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
	}
	if err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task := Task{
		SessionID:       sessionID,
		Record:          record,
		Status:          StatusActive,
		DeliveryHistory: &DeliveryHistory{Records: []DeliveryRecord{}},
	}
	if err := s.store.Put(task); err != nil {
		return Record{}, err
	}
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
			Record: task.Record, SessionID: task.SessionID, Status: task.Status, LastDelivery: task.LastDelivery,
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

// Delete 删除一条属于 sessionID 的任务（硬删除，含历史）。
func (s *Service) Delete(sessionID, id string) (DeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok, err := s.store.Get(id)
	if err != nil {
		return DeleteResult{}, err
	}
	if !ok || task.SessionID != sessionID {
		return DeleteResult{ID: id, Deleted: false, Code: CodeNotFound}, nil
	}
	deleted, err := s.store.Delete(id)
	if err != nil {
		return DeleteResult{}, err
	}
	if !deleted {
		return DeleteResult{ID: id, Deleted: false, Code: CodeNotFound}, nil
	}
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
	if !ok || current.SessionID != sessionID {
		return UpdateResult{ID: id, Updated: false, Code: CodeNotFound}, nil
	}
	if current.Status != StatusActive {
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
		return UpdateResult{ID: id, Updated: false, Record: &record}, nil
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
	return UpdateResult{ID: id, Updated: true, Record: &updated}, nil
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
	default:
		return Record{}, inputError(CodeInvalidRule, "unsupported timing change kind %q", change.Kind)
	}
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

func recordsEqual(a, b Record) bool {
	if a.ID != b.ID || a.Kind != b.Kind || a.Title != b.Title || a.Prompt != b.Prompt {
		return false
	}
	if !a.ScheduledAt.UTC().Equal(b.ScheduledAt.UTC()) {
		return false
	}
	if a.AfterSeconds != b.AfterSeconds || a.EverySeconds != b.EverySeconds {
		return false
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
