package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// maxTimerDelay 是单次定时器等待的上限，超过则分段重算。
const maxTimerDelay = time.Hour

// idleSweepInterval 是空闲期的兜底扫描间隔（S2 修复）：即使变更唤醒丢失
// （跨进程写库、通知异常），新任务也会在一个周期内被发现，不永久滞留。
const idleSweepInterval = time.Minute

// embeddedTurnTimeout 是一次内嵌 turn 的上限（方案 E2：maxTurnDuration 默认 10m）。
const embeddedTurnTimeout = 10 * time.Minute

// headlessDeferInterval 是「无 headless 执行能力」时对非 session 目标的延后间隔
// （J2：不空转、不计死信，等具备能力的宿主接管）。
const headlessDeferInterval = 5 * time.Minute

// 执行模式取值（与 config.ScheduleMode* 对齐；本包不 import config，避免反向依赖）。
const (
	// ModeDelegate 只经 HTTP 委派宿主执行，永不内嵌。
	ModeDelegate = "delegate"
	// ModeEmbedded 允许 daemon 内嵌执行。
	ModeEmbedded = "embedded"
	// ModeAuto 先 delegate，不可用则内嵌。
	ModeAuto = "auto"
)

// ModeAllowsEmbedded 报告该执行模式是否允许内嵌执行。
// delegate 与未知值都返回 false（fail-closed，绝不误内嵌）；空值按允许处理
// （仅为兼容既有测试/程序化构造；生产由配置显式给值）。
func ModeAllowsEmbedded(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModeEmbedded, ModeAuto:
		return true
	default:
		return false
	}
}

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

// Deliver 提交一条 prompt 到指定会话（legacy 形态：不携带 occurrence 键）。
func (d *HTTPDeliverer) Deliver(ctx context.Context, sessionID, text string) error {
	return d.DeliverEnvelope(ctx, DeliveryEnvelope{SessionID: sessionID, Text: text})
}

// DeliverEnvelope 提交一条 prompt 并携带 occurrence 键（S2：服务端按交付账本
// 去重——同一 occurrence 已送达时返回 2xx 且不重复入会话 actor）。
func (d *HTTPDeliverer) DeliverEnvelope(ctx context.Context, env DeliveryEnvelope) error {
	base := strings.TrimRight(strings.TrimSpace(d.BaseURL), "/")
	if base == "" {
		return fmt.Errorf("schedule: runtime server URL is empty")
	}
	payload := map[string]any{"type": "submit_prompt", "prompt": env.Text}
	if keys := cleanOccurrenceKeys(env.Occurrences); len(keys) > 0 {
		payload["occurrences"] = keys
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/runtime/sessions/%s/runtime/commands", base, env.SessionID)
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
		return fmt.Errorf("schedule: deliver to session %s: %w", env.SessionID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("schedule: deliver to session %s: status %d", env.SessionID, resp.StatusCode)
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
	// Executor 与 Arbiter 支撑内嵌执行模式（E0/E1）：delegate 失败后，若两者
	// 均配置且 mode 允许，daemon 先经 Arbiter 取 session 租约——拿到才内嵌执行
	// （活跃会话绝不抢占），拿不到退回持久 mailbox。nil = 不启用内嵌。
	Executor SessionExecutor
	Arbiter  LeaseArbiter
	// Mode 选择执行模式（delegate | embedded | auto）。delegate 永不内嵌；
	// embedded/auto 才允许 delegate 失败后内嵌。空值按允许处理。
	Mode string
	// FallbackToMailbox 落持久兜底消息（宿主恢复后消费）；默认 true。
	FallbackToMailbox bool
	// MisfireGrace 是一次性任务（after/at）的补发宽限窗口（S4b，§4.4）：
	// 0 = 不限宽限（总是补发，兼容既有行为）；>0 时逾期超过窗口的任务不再
	// 投递，直接转 inactive 并计入 MisfireSkippedTotal。
	MisfireGrace time.Duration
	// InstanceID 是任务 claim 的持有者标识（S4，§4.3）：同一 store 上的不同
	// 进程/节点必须不同（Leader 传 node id）。空值回落 "runtime-<pid>"。
	InstanceID string
	Logf       func(format string, args ...any)
}

// Runtime 拥有至多一个定时器，负责启动扫描、到期投递与重试退避。
type Runtime struct {
	opts RuntimeOptions
	mtr  metrics

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
	if opts.InstanceID == "" {
		opts.InstanceID = fmt.Sprintf("runtime-%d", os.Getpid())
	}
	return &Runtime{
		opts:        opts,
		failedUntil: map[string]time.Time{},
		attempts:    map[string]int{},
	}
}

// Start 启动：立即驱动一次（含启动扫描），随后按最近目标排定定时器。
// ctx 目前未使用（保留签名以便后续接取消）。
func (r *Runtime) Start(_ context.Context) {
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

// Metrics 返回累计计数快照（P6：运维端点/HTTP API 直接序列化）。
func (r *Runtime) Metrics() MetricsSnapshot { return r.mtr.Snapshot() }

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
			// S4（§4.3 ③）：leader 易主后从持久层恢复退避状态（接管不风暴）。
			r.hydrateAttempts(member)
		}
		if !r.admitted(task.Record.ID, now) {
			continue
		}
		// S4（§4.3）：组级 claim——抢不到的成员延后重扫，空组直接跳过。
		group = r.claimOrDefer(group, now)
		if len(group) == 0 {
			continue
		}
		// S4b（§4.4）misfire：一次性任务逾期超过宽限窗口 → 不再补发（跳过即留痕）。
		if r.misfireExpired(task, now) {
			r.skipMisfired(task, now)
			continue
		}
		// J2：非 session 目标无既有会话可投递，走 headless 分支。
		if task.TargetKind != TargetSession {
			r.driveHeadless(group, task, now)
			continue
		}
		keys := occurrenceKeys(group)
		// S2（§12.1）：账本命中 = 上一任 leader 已投递成功但未 commit（U1 崩溃
		// 窗口）→ 不重复投递，直接推进（与"投递成功"同语义）。
		if r.allOccurrencesDelivered(keys) {
			r.mtr.duplicateSkippedTotal.Add(int64(len(group)))
			r.opts.Logf("schedule: reminders %v already delivered (ledger hit); advancing", groupIDs(group))
			for _, member := range group {
				r.commit(member, now)
			}
			continue
		}
		text := r.renderGroup(group)
		envelope := DeliveryEnvelope{SessionID: task.SessionID, Text: text, Occurrences: keys}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		deliverErr := DeliverEnvelopeTo(ctx, r.opts.Deliverer, envelope)
		cancel()
		if deliverErr != nil {
			// 内嵌执行（E0/E1）：delegate 失败且配置允许时，先经 Arbiter 取
			// session 租约——拿到才自跑 turn（活跃会话绝不抢占）。
			// S4：长执行前把 claim 续到覆盖执行上限（防易主后重复执行）；
			// 不可能内嵌时不续期——失败任务的让位窗口保持短租约。
			if r.embeddedPossible() {
				r.renewClaimsForLongWork(group, now)
			}
			if r.tryEmbedded(task.SessionID, text) {
				r.mtr.embeddedTotal.Add(int64(len(group)))
				r.mtr.deliveredTotal.Add(int64(len(group)))
				for _, member := range group {
					r.commit(member, now)
				}
				continue
			}
			r.mtr.deliveryFailedTotal.Add(int64(len(group)))
			for _, member := range group {
				r.markFailed(member.Record.ID, now)
			}
			// 兜底（存储文档 §4.3 / 守护进程文档 D1）：失败即落持久 pending
			// 消息，宿主恢复后消费——宿主离线时消息不丢。
			if r.opts.FallbackToMailbox {
				for _, member := range group {
					if err := r.appendPending(member, deliverErr, now); err != nil {
						r.opts.Logf("schedule: append pending for %s: %v", member.Record.ID, err)
					}
				}
			}
			r.opts.Logf("schedule: reminders %v were not delivered: %v", groupIDs(group), deliverErr)
			continue
		}
		for _, member := range group {
			r.commit(member, now)
		}
		r.mtr.deliveredTotal.Add(int64(len(group)))
	}
	r.scheduleNext(now)
}

// driveHeadless 执行不绑会话的作业（J2：workspace/headless 目标）。
//
// 无 headless 执行能力时**延后**（不空转、不计死信），等待具备能力的宿主接管。
func (r *Runtime) driveHeadless(group []Task, task Task, now time.Time) {
	exec, ok := r.opts.Executor.(HeadlessExecutor)
	if !ok || exec == nil {
		r.deferTask(task.Record.ID, now.Add(headlessDeferInterval))
		r.opts.Logf("schedule: job %s (%s) deferred: no headless executor configured", task.Record.ID, task.TargetKind)
		return
	}
	// S4：长执行前续期 claim（与内嵌路径同口径）。
	r.renewClaimsForLongWork(group, now)
	ctx, cancel := context.WithTimeout(context.Background(), embeddedTurnTimeout)
	defer cancel()
	result, err := exec.ExecuteHeadlessTurn(ctx, HeadlessTurnRequest{
		JobID:      task.Record.ID,
		Goal:       task.Record.Goal,
		Plan:       task.Record.Plan,
		Prompt:     task.Record.Prompt,
		Profile:    task.TargetProfile,
		Workspace:  task.TargetWorkspace,
		TargetKind: task.TargetKind,
	})
	// J3：无论成败都落一条运行历史。
	r.recordJobRun(task, now, result, err, now)
	if err != nil {
		r.mtr.deliveryFailedTotal.Add(int64(len(group)))
		for _, member := range group {
			r.markFailed(member.Record.ID, now)
		}
		r.opts.Logf("schedule: headless job %s failed: %v", task.Record.ID, err)
		return
	}
	r.mtr.embeddedTotal.Add(int64(len(group)))
	r.mtr.deliveredTotal.Add(int64(len(group)))
	for _, member := range group {
		r.commit(member, now)
	}
	// J3：结果回投必须在 commit **之后**——commit 会清理该作业的 pending（正常投递
	// 成功的语义），若先回投则失败兜底的 pending 会被立刻抹掉。
	r.deliverJobResult(task, result)
}

// recordJobRun 落一条作业运行历史（J3；best-effort，失败只记日志）。
func (r *Runtime) recordJobRun(task Task, started time.Time, result HeadlessTurnResult, runErr error, now time.Time) {
	sqlStore, ok := r.opts.Store.(*SQLiteStore)
	if !ok {
		return
	}
	run := JobRun{
		ScheduleID: task.Record.ID,
		SessionID:  result.SessionID,
		StartedAt:  started.UTC(),
		FinishedAt: now.UTC(),
	}
	if runErr != nil {
		run.Status = JobRunFailed
		run.Error = runErr.Error()
	} else {
		run.Status = JobRunDone
		run.Output = result.Output
	}
	if err := sqlStore.AppendJobRun(run); err != nil {
		r.opts.Logf("schedule: append job run for %s: %v", task.Record.ID, err)
	}
}

// deliverJobResult 把 headless 作业结果回投给创建者会话（J3；best-effort）。
func (r *Runtime) deliverJobResult(task Task, result HeadlessTurnResult) {
	creator := strings.TrimSpace(task.CreatedBySessionID)
	if creator == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.opts.Deliverer.Deliver(ctx, creator, RenderJobResultFraming(task, result)); err != nil {
		r.opts.Logf("schedule: deliver job result for %s to creator %s failed: %v", task.Record.ID, creator, err)
		// J3：回投失败落持久 pending，宿主恢复后消费（与提醒投递同一兜底通道）。
		if r.opts.FallbackToMailbox {
			if sqlStore, ok := r.opts.Store.(*SQLiteStore); ok {
				if perr := sqlStore.AppendPendingMessage(task.Record.ID, creator,
					RenderJobResultFraming(task, result),
					"job result delivery failed: "+err.Error(), r.opts.Clock()); perr != nil {
					r.opts.Logf("schedule: append job result pending for %s: %v", task.Record.ID, perr)
				}
			}
		}
	}
}

// RenderJobResultFraming 渲染一次 headless 作业的结果回投文本（J3）。
func RenderJobResultFraming(task Task, result HeadlessTurnResult) string {
	payload := map[string]string{
		"schedule_id": task.Record.ID,
		"status":      string(JobRunDone),
		"output":      result.Output,
	}
	if goal := strings.TrimSpace(task.Record.Goal); goal != "" {
		payload["goal"] = goal
	}
	encoded, _ := json.Marshal(payload)
	return strings.Join([]string{
		"[SCHEDULE JOB RESULT]",
		"This is the result of a scheduled job you created",
		"result_json: " + string(encoded),
	}, "\n")
}

// groupFor 返回同一会话、同一批到期的周期任务（P0：every）；一次性任务各自成组。
// 非会话目标（J2）不参与分组：它们没有共享会话，必须各自执行。
func (r *Runtime) groupFor(task Task, due []Task) []Task {
	if task.Record.Kind != KindEvery || strings.TrimSpace(task.SessionID) == "" {
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
	r.attempts[id]++
	attempt := r.attempts[id]
	deadLettered := attempt >= r.opts.MaxAttempts
	if deadLettered {
		r.opts.Logf("schedule: reminder %s reached max delivery attempts (%d)", id, attempt)
		r.mtr.deadLetterTotal.Add(1)
	}
	r.mtr.retriesScheduled.Add(1)
	r.failedUntil[id] = now.Add(deliveryBackoff(attempt))
	r.mu.Unlock()
	// S4（§4.3 ③）：attempts/死信持久化——leader 易主后退避不归零、状态可查。
	if attempts, ok := r.opts.Store.(TaskAttemptStore); ok {
		if _, err := attempts.RecordTaskAttempt(id, now, deadLettered); err != nil {
			r.opts.Logf("schedule: persist attempt for %s: %v", id, err)
		}
	}
}

// deliveryBackoff 是第 attempt 次失败后的退避（30s × n，封顶 10m）。
func deliveryBackoff(attempt int) time.Duration {
	backoff := time.Duration(attempt) * 30 * time.Second
	if backoff > 10*time.Minute {
		backoff = 10 * time.Minute
	}
	return backoff
}

// deferTask 延后某任务的下次尝试（不改 attempts，不产生死信）。
func (r *Runtime) deferTask(id string, until time.Time) {
	r.mu.Lock()
	r.failedUntil[id] = until
	r.mu.Unlock()
}

// hydrateAttempts 在本地无记录时从持久层恢复退避状态（S4：leader 易主后
// 不立即重试；与 markFailed 的退避公式一致）。
func (r *Runtime) hydrateAttempts(task Task) {
	if task.AttemptCount <= 0 {
		return
	}
	id := task.Record.ID
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.attempts[id]; ok {
		return
	}
	r.attempts[id] = task.AttemptCount
	if task.LastAttemptAt != nil {
		r.failedUntil[id] = task.LastAttemptAt.Add(deliveryBackoff(task.AttemptCount))
	}
}

// claimOrDefer 对一批任务做组级 claim（§4.3）：未抢到的成员本地延后重扫，
// 返回抢到的子集（可能为空或小于原组）。store 未实现 TaskClaimer 时原样
// 返回（fail-open：claim 是防双投递的冗余层，不是投递前提）。
func (r *Runtime) claimOrDefer(group []Task, now time.Time) []Task {
	claimer, ok := r.opts.Store.(TaskClaimer)
	if !ok {
		return group
	}
	ids := groupIDs(group)
	claimed, err := claimer.ClaimTasks(ids, r.opts.InstanceID, now.Add(claimLease), now)
	if err != nil {
		r.opts.Logf("schedule: claim tasks %v: %v", ids, err)
		return group
	}
	if len(claimed) == len(ids) {
		return group
	}
	claimedSet := make(map[string]bool, len(claimed))
	for _, id := range claimed {
		claimedSet[id] = true
	}
	out := make([]Task, 0, len(claimed))
	for _, member := range group {
		if claimedSet[member.Record.ID] {
			out = append(out, member)
			continue
		}
		// 抢不到：另一节点正在处理（或刚崩溃、租约未过期）——延后重扫。
		r.deferTask(member.Record.ID, now.Add(claimRetryDelay))
	}
	r.mtr.claimConflictsTotal.Add(int64(len(ids) - len(claimed)))
	return out
}

// renewClaimsForLongWork 把 claim 租期延长到覆盖一次长执行（内嵌/headless
// 上限 embeddedTurnTimeout + 余量）：执行期间即使 leader 易主，新 leader 也
// 不会把同一任务当作"无主"重复执行。
func (r *Runtime) renewClaimsForLongWork(group []Task, now time.Time) {
	claimer, ok := r.opts.Store.(TaskClaimer)
	if !ok {
		return
	}
	until := now.Add(embeddedTurnTimeout + claimLease)
	if _, err := claimer.RenewTaskClaims(groupIDs(group), r.opts.InstanceID, until); err != nil {
		r.opts.Logf("schedule: renew claims for long work: %v", err)
	}
}

// misfireExpired 报告一次性任务（after/at）是否已超出补发宽限窗口（§4.4）：
// 未配置宽限（<=0）时恒 false（总是补发，兼容既有行为）；周期任务不适用。
func (r *Runtime) misfireExpired(task Task, now time.Time) bool {
	if r.opts.MisfireGrace <= 0 {
		return false
	}
	switch task.Record.Kind {
	case KindAfter, KindAt:
	default:
		return false
	}
	return now.Sub(task.Record.ScheduledAt) > r.opts.MisfireGrace
}

// skipMisfired 把超窗的一次性任务直接转 inactive（不投递）：留痕计数 + 日志，
// 清 attempts/死信并释放 claim（与投递成功同口径的簿记收尾）。
func (r *Runtime) skipMisfired(task Task, now time.Time) {
	r.mtr.misfireSkippedTotal.Add(1)
	r.opts.Logf("schedule: reminder %s misfired beyond grace (%s late > %s); marking inactive without delivery",
		task.Record.ID, now.Sub(task.Record.ScheduledAt).Round(time.Second), r.opts.MisfireGrace)
	next := task
	next.Status = StatusInactive
	applied, err := r.opts.Store.UpdateCAS(next, task.StateVersion)
	if err != nil {
		r.opts.Logf("schedule: mark misfired %s: %v", task.Record.ID, err)
		return
	}
	if !applied {
		r.opts.Logf("schedule: mark misfired %s skipped (stale state version)", task.Record.ID)
		return
	}
	if attempts, ok := r.opts.Store.(TaskAttemptStore); ok {
		if err := attempts.ClearTaskAttempts(task.Record.ID); err != nil {
			r.opts.Logf("schedule: clear attempts for misfired %s: %v", task.Record.ID, err)
		}
	}
	if claimer, ok := r.opts.Store.(TaskClaimer); ok {
		if err := claimer.ReleaseTaskClaims([]string{task.Record.ID}, r.opts.InstanceID); err != nil {
			r.opts.Logf("schedule: release claim for misfired %s: %v", task.Record.ID, err)
		}
	}
	r.mu.Lock()
	delete(r.attempts, task.Record.ID)
	delete(r.failedUntil, task.Record.ID)
	r.mu.Unlock()
	r.notifyChanged()
}

// appendPending 把一次失败的投递落成持久兜底消息（`schedule_pending_messages`）。
func (r *Runtime) appendPending(task Task, deliverErr error, now time.Time) error {
	sqlStore, ok := r.opts.Store.(*SQLiteStore)
	if !ok {
		return nil
	}
	return sqlStore.AppendPendingMessage(task.Record.ID, task.SessionID, task.Record.Prompt,
		fmt.Sprintf("delivery failed: %v", deliverErr), now)
}

// tryEmbedded 是内嵌执行入口（E0/E1）：Executor 与 Arbiter 都配置才尝试。
// 先取 session 租约（拿不到 = 会话被交互宿主持有，绝不抢占，返回 false），
// 拿到后提交一次 turn 并释放租约。返回是否已内嵌执行成功。
func (r *Runtime) tryEmbedded(sessionID, text string) bool {
	// mode 门控：delegate（或未知值）永不内嵌，即使 Executor/Arbiter 已注入。
	if !r.embeddedPossible() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), embeddedTurnTimeout)
	defer cancel()
	release, ok := r.opts.Arbiter.AcquireSession(ctx, sessionID)
	if !ok {
		r.opts.Logf("schedule: session %s is held by another host; skip embedded execution", sessionID)
		r.mtr.embeddedSkippedTotal.Add(1)
		return false
	}
	defer release()
	if err := r.opts.Executor.ExecuteTurn(ctx, sessionID, text); err != nil {
		r.opts.Logf("schedule: embedded turn for %s failed: %v", sessionID, err)
		return false
	}
	return true
}

// embeddedPossible 报告内嵌执行是否可能（与 tryEmbedded 的入口门一致）。
func (r *Runtime) embeddedPossible() bool {
	return ModeAllowsEmbedded(r.opts.Mode) && r.opts.Executor != nil && r.opts.Arbiter != nil
}

// DrainPendingMessages 消费某会话的兜底消息：逐条尝试投递，成功即标记消费。
// 宿主启动/会话激活时调用；返回消费条数。
func (r *Runtime) DrainPendingMessages(ctx context.Context, sessionID string, limit int) (int, error) {
	sqlStore, ok := r.opts.Store.(*SQLiteStore)
	if !ok {
		return 0, nil
	}
	messages, err := sqlStore.ListPendingMessages(sessionID, limit)
	if err != nil {
		return 0, err
	}
	consumed := 0
	now := r.opts.Clock()
	for _, message := range messages {
		if err := ctx.Err(); err != nil {
			return consumed, err
		}
		deliverCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		deliverErr := r.opts.Deliverer.Deliver(deliverCtx, message.SessionID, message.Prompt)
		cancel()
		if deliverErr != nil {
			r.opts.Logf("schedule: pending %d redelivery failed: %v", message.ID, deliverErr)
			continue
		}
		if err := sqlStore.MarkPendingMessageConsumed(message.ID, now); err != nil {
			r.opts.Logf("schedule: mark pending %d consumed: %v", message.ID, err)
			continue
		}
		// 补投成功即推进其任务：否则 daemon 稍后仍会按原目标重投，同一提醒双投。
		r.advanceTaskAfterDrain(message.ScheduleID, now)
		consumed++
	}
	return consumed, nil
}

// advanceTaskAfterDrain 在兜底消息成功补投后推进其任务。
//
// 仅当任务仍 active 且目标已到点（尚未被任何路径提交）时推进：正常路径
// 提交成功后 ScheduledAt 已移到未来（或转 inactive），此处会自然跳过，
// 因此 drain 与 daemon 重试两条路径不会对同一提醒重复推进/重复投递。
func (r *Runtime) advanceTaskAfterDrain(scheduleID string, now time.Time) {
	if strings.TrimSpace(scheduleID) == "" {
		return
	}
	task, ok, err := r.opts.Store.Get(scheduleID)
	if err != nil || !ok {
		return
	}
	if task.Status != StatusActive || now.Before(task.Record.ScheduledAt) {
		return
	}
	r.commit(task, now)
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
	// 同一提交写入历史表（"追加回执 + 推进任务"同批；存储文档 §4.3）。
	if sqlStore, ok := r.opts.Store.(*SQLiteStore); ok && receipt != nil {
		if err := sqlStore.AppendDeliveryRecord(task.Record.ID, DeliveryRecord{
			DeliveryReceipt: *receipt,
			Prompt:          task.Record.Prompt,
		}); err != nil {
			r.opts.Logf("schedule: append delivery history for %s: %v", task.Record.ID, err)
		}
	}
	// 提醒已经送达：清理该任务历史失败留下的 pending 行，避免宿主 drain 重复投递。
	if sqlStore, ok := r.opts.Store.(*SQLiteStore); ok {
		if err := sqlStore.MarkPendingConsumedBySchedule(task.Record.ID, now); err != nil {
			r.opts.Logf("schedule: clear pending for %s: %v", task.Record.ID, err)
		}
	}
	// S4（§4.3 ③）：投递成功——清空 attempts/死信留痕并释放 claim（簿记写，
	// 不影响 state_version；失败只记日志，不阻断投递推进）。
	if attempts, ok := r.opts.Store.(TaskAttemptStore); ok {
		if err := attempts.ClearTaskAttempts(task.Record.ID); err != nil {
			r.opts.Logf("schedule: clear attempts for %s: %v", task.Record.ID, err)
		}
	}
	if claimer, ok := r.opts.Store.(TaskClaimer); ok {
		if err := claimer.ReleaseTaskClaims([]string{task.Record.ID}, r.opts.InstanceID); err != nil {
			r.opts.Logf("schedule: release claim for %s: %v", task.Record.ID, err)
		}
	}
	r.mu.Lock()
	delete(r.attempts, task.Record.ID)
	delete(r.failedUntil, task.Record.ID)
	r.mu.Unlock()
	// 投递推进也是一次任务变更：发布 schedule.changed（通知失败不影响投递）。
	r.notifyChanged()
}

// notifyChanged 通过 Service 的通知接缝发布 schedule.changed（nil 安全；
// emitChanged 内部自持锁并 recover，投递路径不受监听器异常影响）。
func (r *Runtime) notifyChanged() {
	if r.opts.Service == nil {
		return
	}
	r.opts.Service.emitChanged()
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
		// 空闲兜底：没有 active 任务时也保留一个低频重扫，避免"创建后无人
		// 唤醒"导致任务滞留到下一次外部事件（见 Service.SetDriverWaker）。
		r.timer = time.AfterFunc(idleSweepInterval, func() {
			r.mu.Lock()
			r.timer = nil
			r.mu.Unlock()
			r.RequestDrive()
		})
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

// occurrenceKeys 计算一组到期任务的交付键（与投递载荷的 occurrence_at 同源：
// 使用该 occurrence 的 scheduled_at；commit 前保持稳定）。
func occurrenceKeys(group []Task) []string {
	keys := make([]string, 0, len(group))
	for _, task := range group {
		keys = append(keys, OccurrenceKey(task.Record.ID, task.Record.ScheduledAt))
	}
	return keys
}

// allOccurrencesDelivered 查询交付账本（store 未实现 OccurrenceLedger 时恒 false）。
func (r *Runtime) allOccurrencesDelivered(keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	ledger, ok := r.opts.Store.(OccurrenceLedger)
	if !ok {
		return false
	}
	delivered, err := ledger.AllOccurrencesDelivered(keys)
	if err != nil {
		r.opts.Logf("schedule: delivery ledger lookup failed: %v", err)
		return false
	}
	return delivered
}
