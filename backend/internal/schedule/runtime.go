package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxTimerDelay 是单次定时器等待的上限，超过则分段重算。
const maxTimerDelay = time.Hour

// Deliverer 把到期提醒文本投递到原会话。
type Deliverer interface {
	Deliver(ctx context.Context, sessionID, text string) error
}

// HTTPDeliverer 经 runtime-server 的会话命令端点投递（delegate 模式）。
//
// 载荷为 {"type":"submit_prompt","prompt": text}；source 由提醒固定文本
// （[SCHEDULE REMINDER] / [SCHEDULE REMINDER BATCH]）标识，因为 RunMeta 没有
// 通用 source 字段。
type HTTPDeliverer struct {
	BaseURL string
	Client  *http.Client
}

// Deliver 提交一条 prompt 到指定会话。
func (d *HTTPDeliverer) Deliver(ctx context.Context, sessionID, text string) error {
	base := strings.TrimRight(strings.TrimSpace(d.BaseURL), "/")
	if base == "" {
		return fmt.Errorf("schedule: runtime server URL is empty")
	}
	body, err := json.Marshal(map[string]any{"type": "submit_prompt", "prompt": text})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/runtime/sessions/%s/runtime/commands", base, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("schedule: deliver to session %s: %w", sessionID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("schedule: deliver to session %s: status %d", sessionID, resp.StatusCode)
	}
	return nil
}

// RuntimeOptions 配置调度运行时。
type RuntimeOptions struct {
	Store       Store
	Service     *Service
	Clock       Clock
	Deliverer   Deliverer
	MaxAttempts int
	Logf        func(format string, args ...any)
}

// Runtime 拥有至多一个定时器，负责启动扫描、到期投递与重试退避。
type Runtime struct {
	opts RuntimeOptions

	mu          sync.Mutex
	timer       *time.Timer
	running     bool
	requested   bool
	stopping    bool
	failedUntil map[string]time.Time
	attempts    map[string]int
}

// NewRuntime 构造调度运行时。
func NewRuntime(opts RuntimeOptions) *Runtime {
	if opts.Clock == nil {
		opts.Clock = SystemClock
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 5
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &Runtime{
		opts:        opts,
		failedUntil: map[string]time.Time{},
		attempts:    map[string]int{},
	}
}

// Start 启动：立即驱动一次（含启动扫描），随后按最近目标排定定时器。
func (r *Runtime) Start(ctx context.Context) {
	_ = ctx
	r.RequestDrive()
}

// Stop 停止定时器并等待进行中的 drive 结束。
func (r *Runtime) Stop() {
	r.mu.Lock()
	r.stopping = true
	r.clearTimerLocked()
	r.mu.Unlock()
	for {
		r.mu.Lock()
		running := r.running
		r.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// RequestDrive 请求一次驱动；若已有 drive 在跑则合并。
func (r *Runtime) RequestDrive() {
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return
	}
	r.requested = true
	r.clearTimerLocked()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	go r.loop()
}

func (r *Runtime) loop() {
	for {
		r.mu.Lock()
		if r.stopping {
			r.requested = false
			r.running = false
			r.mu.Unlock()
			return
		}
		if !r.requested {
			r.running = false
			r.mu.Unlock()
			return
		}
		r.requested = false
		r.mu.Unlock()

		r.drive()

		r.mu.Lock()
		if !r.requested {
			r.running = false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
	}
}

func (r *Runtime) clearTimerLocked() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

func (r *Runtime) drive() {
	r.mu.Lock()
	r.clearTimerLocked()
	stopping := r.stopping
	r.mu.Unlock()
	if stopping {
		return
	}
	now := r.opts.Clock()
	due, err := r.opts.Store.ListActiveDue(now)
	if err != nil {
		r.opts.Logf("schedule: list due tasks: %v", err)
		return
	}
	handled := map[string]bool{}
	for _, task := range due {
		if handled[task.Record.ID] {
			continue
		}
		group := r.groupFor(task, due)
		for _, member := range group {
			handled[member.Record.ID] = true
		}
		if !r.admitted(task.Record.ID, now) {
			continue
		}
		text := r.renderGroup(group)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		deliverErr := r.opts.Deliverer.Deliver(ctx, task.SessionID, text)
		cancel()
		if deliverErr != nil {
			for _, member := range group {
				r.markFailed(member.Record.ID, now)
			}
			r.opts.Logf("schedule: reminders %v were not delivered: %v", groupIDs(group), deliverErr)
			continue
		}
		for _, member := range group {
			r.commit(member, now)
		}
	}
	r.scheduleNext(now)
}

// groupFor 返回同一会话、同一批到期的周期任务（P0：every）；一次性任务各自成组。
func (r *Runtime) groupFor(task Task, due []Task) []Task {
	if task.Record.Kind != KindEvery {
		return []Task{task}
	}
	group := make([]Task, 0, 1)
	for _, candidate := range due {
		if candidate.SessionID == task.SessionID && candidate.Record.Kind == KindEvery {
			group = append(group, candidate)
		}
	}
	if len(group) == 0 {
		return []Task{task}
	}
	return group
}

func (r *Runtime) admitted(id string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.failedUntil[id]
	return !ok || !now.Before(until)
}

func (r *Runtime) markFailed(id string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts[id]++
	attempt := r.attempts[id]
	if attempt >= r.opts.MaxAttempts {
		r.opts.Logf("schedule: reminder %s reached max delivery attempts (%d)", id, attempt)
	}
	backoff := time.Duration(attempt) * 30 * time.Second
	if backoff > 10*time.Minute {
		backoff = 10 * time.Minute
	}
	r.failedUntil[id] = now.Add(backoff)
}

func (r *Runtime) commit(task Task, now time.Time) {
	occurrence, err := ResolveOccurrence(task.Record, now)
	if err != nil {
		r.opts.Logf("schedule: resolve occurrence for %s: %v", task.Record.ID, err)
		return
	}
	next := task
	receipt := &DeliveryReceipt{
		ScheduledAt: occurrence.OccurrenceAt,
		DeliveredAt: now.UTC(),
		MessageID:   syntheticMessageID(task.Record.ID, occurrence.OccurrenceAt),
	}
	next.LastDelivery = receipt
	next.DeliveryHistory = appendHistory(task, receipt)
	if occurrence.NextScheduledAt == nil {
		next.Status = StatusInactive
	} else {
		next.Record.ScheduledAt = occurrence.NextScheduledAt.UTC()
	}
	applied, err := r.opts.Store.UpdateCAS(next, task.StateVersion)
	if err != nil {
		r.opts.Logf("schedule: commit %s: %v", task.Record.ID, err)
		return
	}
	if !applied {
		r.opts.Logf("schedule: commit %s skipped (stale state version)", task.Record.ID)
		return
	}
	r.mu.Lock()
	delete(r.attempts, task.Record.ID)
	delete(r.failedUntil, task.Record.ID)
	r.mu.Unlock()
}

func (r *Runtime) renderGroup(group []Task) string {
	if len(group) == 1 {
		return RenderReminderFraming(group[0].Record)
	}
	return RenderRecurringBatchFraming(group)
}

func (r *Runtime) scheduleNext(now time.Time) {
	tasks, err := r.opts.Store.List()
	if err != nil {
		r.opts.Logf("schedule: list tasks for timer: %v", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping {
		return
	}
	var next *time.Time
	for _, task := range tasks {
		if task.Status != StatusActive {
			continue
		}
		at := task.Record.ScheduledAt
		if until, ok := r.failedUntil[task.Record.ID]; ok && until.After(at) {
			at = until
		}
		if next == nil || at.Before(*next) {
			candidate := at
			next = &candidate
		}
	}
	if next == nil {
		return
	}
	delay := next.Sub(now)
	if delay < 0 {
		delay = 0
	}
	if delay > maxTimerDelay {
		delay = maxTimerDelay
	}
	r.timer = time.AfterFunc(delay, func() {
		r.mu.Lock()
		r.timer = nil
		r.mu.Unlock()
		r.RequestDrive()
	})
}

// RenderRecurringBatchFraming 渲染同一会话的周期提醒批次。
func RenderRecurringBatchFraming(group []Task) string {
	payload := make([]map[string]string, 0, len(group))
	for _, task := range group {
		payload = append(payload, map[string]string{
			"schedule_id":     task.Record.ID,
			"occurrence_at":   task.Record.ScheduledAt.UTC().Format(time.RFC3339Nano),
			"reminder_prompt": task.Record.Prompt,
		})
	}
	encoded, _ := json.Marshal(payload)
	return strings.Join([]string{
		"[SCHEDULE REMINDER BATCH]",
		"This is a scheduled message from the user",
		"reminders_json: " + string(encoded),
	}, "\n")
}

func appendHistory(task Task, receipt *DeliveryReceipt) *DeliveryHistory {
	history := task.DeliveryHistory
	if history == nil {
		history = &DeliveryHistory{Records: []DeliveryRecord{}, EarlierRecordsUnavailable: task.LastDelivery == nil}
	}
	records := append(append([]DeliveryRecord{}, history.Records...), DeliveryRecord{
		DeliveryReceipt: *receipt,
		Prompt:          task.Record.Prompt,
	})
	return &DeliveryHistory{
		Records:                   records,
		EarlierRecordsUnavailable: history.EarlierRecordsUnavailable,
		EarlierRecordsPruned:      history.EarlierRecordsPruned,
	}
}

func syntheticMessageID(scheduleID string, occurrence time.Time) string {
	return fmt.Sprintf("schedule:%s:%d", scheduleID, occurrence.UTC().UnixMilli())
}

func groupIDs(group []Task) []string {
	ids := make([]string, 0, len(group))
	for _, task := range group {
		ids = append(ids, task.Record.ID)
	}
	return ids
}
