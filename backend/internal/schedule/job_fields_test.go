package schedule

import (
	"testing"
	"time"
)

// TestJobFieldsRoundTrip 验证 J0 新字段（goal / plan / target）持久化往返，
// 且 UpdateCAS 能更新它们。
func TestJobFieldsRoundTrip(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	task := Task{
		SessionID: "session-1",
		Record: Record{
			ID:          "job-1",
			Kind:        KindAfter,
			Title:       "t",
			Prompt:      "p",
			Goal:        "做一次系统检查",
			Plan:        []string{"检查磁盘", "检查内存"},
			ScheduledAt: now.Add(time.Hour),
		},
		Status:          StatusActive,
		TargetKind:      TargetWorkspace,
		TargetWorkspace: "/ws",
		TargetProfile:   "scheduled",
	}
	if err := store.Put(task); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := store.Get("job-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Record.Goal != "做一次系统检查" {
		t.Fatalf("goal round-trip failed: %q", got.Record.Goal)
	}
	if len(got.Record.Plan) != 2 || got.Record.Plan[0] != "检查磁盘" || got.Record.Plan[1] != "检查内存" {
		t.Fatalf("plan round-trip failed: %v", got.Record.Plan)
	}
	if got.TargetKind != TargetWorkspace || got.TargetWorkspace != "/ws" || got.TargetProfile != "scheduled" {
		t.Fatalf("target round-trip failed: kind=%q ws=%q profile=%q", got.TargetKind, got.TargetWorkspace, got.TargetProfile)
	}

	// UpdateCAS 也必须持久化这些字段（否则编辑目标/上下文会静默丢失）。
	got.Record.Goal = "改过的目标"
	got.Record.Plan = []string{"只检查磁盘"}
	got.TargetProfile = "scheduled-v2"
	applied, err := store.UpdateCAS(got, got.StateVersion)
	if err != nil || !applied {
		t.Fatalf("UpdateCAS: applied=%v err=%v", applied, err)
	}
	after, ok, err := store.Get("job-1")
	if err != nil || !ok {
		t.Fatalf("Get after update: ok=%v err=%v", ok, err)
	}
	if after.Record.Goal != "改过的目标" || after.TargetProfile != "scheduled-v2" {
		t.Fatalf("update did not persist job fields: %+v", after)
	}
	if len(after.Record.Plan) != 1 || after.Record.Plan[0] != "只检查磁盘" {
		t.Fatalf("update did not persist plan: %v", after.Record.Plan)
	}
}

// TestCreateRequestPersistsJobFields 验证 CreateRequest 的新字段经 Service.Create
// 落库（含 trim/清洗）——即「模型/人工可创建带目标与计划的作业」。
func TestCreateRequestPersistsJobFields(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	delay := int64(600)
	rec, err := svc.Create("session-1", CreateRequest{
		Prompt:          "p",
		Title:           "t",
		AfterSeconds:    &delay,
		Goal:            "  做一次系统检查  ",
		Plan:            []string{" 检查磁盘 ", "", "检查内存"},
		TargetKind:      TargetHeadless,
		TargetWorkspace: " /ws ",
		TargetProfile:   " scheduled ",
	}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 返回值也应带上清洗后的字段。
	if rec.Goal != "做一次系统检查" {
		t.Fatalf("returned goal not trimmed: %q", rec.Goal)
	}
	if len(rec.Plan) != 2 || rec.Plan[0] != "检查磁盘" || rec.Plan[1] != "检查内存" {
		t.Fatalf("returned plan not normalized: %v", rec.Plan)
	}

	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.Record.Goal != "做一次系统检查" {
		t.Fatalf("stored goal mismatch: %q", task.Record.Goal)
	}
	if len(task.Record.Plan) != 2 {
		t.Fatalf("stored plan mismatch: %v", task.Record.Plan)
	}
	if task.TargetKind != TargetHeadless || task.TargetWorkspace != "/ws" || task.TargetProfile != "scheduled" {
		t.Fatalf("stored target mismatch: kind=%q ws=%q profile=%q", task.TargetKind, task.TargetWorkspace, task.TargetProfile)
	}
}

// TestJobFieldsLegacyRowDefaults 验证旧行（无新列值）回落 session / 空计划——
// 保证「旧作业零迁移」。
func TestJobFieldsLegacyRowDefaults(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := formatInstant(time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	// 只写旧列：新列走 DEFAULT（target_kind='session'、其余空串）。
	if err := store.ExecRaw(`INSERT INTO schedule_tasks
        (id, session_id, kind, title, prompt, scheduled_at, status, state_version, created_at, updated_at)
        VALUES ('legacy-1', 's', 'after', 't', 'p', ?, 'active', 0, ?, ?)`,
		now, now, now); err != nil {
		t.Fatalf("ExecRaw legacy insert: %v", err)
	}

	got, ok, err := store.Get("legacy-1")
	if err != nil || !ok {
		t.Fatalf("Get legacy: ok=%v err=%v", ok, err)
	}
	if got.TargetKind != TargetSession {
		t.Fatalf("legacy row must default to target=session, got %q", got.TargetKind)
	}
	if got.Record.Goal != "" || len(got.Record.Plan) != 0 {
		t.Fatalf("legacy row must have empty goal/plan, got goal=%q plan=%v", got.Record.Goal, got.Record.Plan)
	}
}

// TestHeadlessTargetUnbindsSession 验证 J1/J2：非 session 目标不绑定会话、参与到期
// 选择（由 Runtime 走 headless 分支），且仍在目录中可见。
func TestHeadlessTargetUnbindsSession(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	delay := int64(60)
	rec, err := svc.Create("session-creator", CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay, Goal: "做一次系统检查", TargetKind: TargetHeadless,
	}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.SessionID != "" {
		t.Fatalf("headless job must not bind a session, got %q", task.SessionID)
	}
	if task.TargetKind != TargetHeadless {
		t.Fatalf("target kind mismatch: %q", task.TargetKind)
	}

	// J2：非 session 目标参与到期选择；无 headless 能力时由 Runtime 延后（不空转）。
	due, err := store.ListActiveDue(now.Add(2 * time.Minute))
	if err != nil {
		t.Fatalf("ListActiveDue: %v", err)
	}
	dueFound := false
	for _, d := range due {
		if d.Record.ID == rec.ID {
			dueFound = true
		}
	}
	if !dueFound {
		t.Fatal("headless job must be selectable as due (J2)")
	}

	entries, err := svc.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.ID == rec.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("headless job must remain visible in the catalog")
	}
}

// TestWorkspaceTargetRequiresWorkspace 验证 workspace 目标必须提供工作区。
func TestWorkspaceTargetRequiresWorkspace(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	svc := NewService(store, SystemClock)
	delay := int64(60)
	if _, err := svc.Create("s", CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay, TargetKind: TargetWorkspace,
	}, OriginModel); err == nil {
		t.Fatal("workspace target without target_workspace must be rejected")
	}
}

// TestNonSessionTargetRequiresGoal 验证 J4：非会话作业必须有目标（goal）；
// 会话目标不要求（保持向后兼容）。
func TestNonSessionTargetRequiresGoal(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	svc := NewService(store, SystemClock)
	delay := int64(60)

	if _, err := svc.Create("", CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay, TargetKind: TargetHeadless,
	}, OriginModel); err == nil {
		t.Fatal("headless job without goal must be rejected")
	}
	if _, err := svc.Create("s", CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay,
	}, OriginModel); err != nil {
		t.Fatalf("session reminder must not require goal: %v", err)
	}
}
