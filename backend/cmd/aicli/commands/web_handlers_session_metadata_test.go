package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// countingWebSessionStorage 包装 InMemoryStorage，统计完整 Load / 元数据单行读取，
// 并把 ListMetadataPage 实现成真正的元数据行（History 不装载）。
// 用于证明 Web 侧栏列表与 resume 校验不再逐个反序列化会话历史。
type countingWebSessionStorage struct {
	*runtimechat.InMemoryStorage
	loadCalls     int32
	metadataCalls int32
	// stripSummaryID 模拟 metadata.summary 缺失的少数历史会话，
	// 让列表路径必须回退一次完整加载才能渲染摘要。
	stripSummaryID string
	// stripTitleID 模拟 metadata.title 缺失的少数历史会话。
	stripTitleID string
	// missingPreview 模拟「元数据行确实缺标题/摘要」的会话：UpdatePreviewMetadata
	// 落库后从集合移除（懒修复后该行不再缺字段，与真实存储语义一致）。
	missingPreview map[string]bool
}

func (s *countingWebSessionStorage) Load(ctx context.Context, sessionID string) (*runtimechat.Session, error) {
	atomic.AddInt32(&s.loadCalls, 1)
	return s.InMemoryStorage.Load(ctx, sessionID)
}

func (s *countingWebSessionStorage) LoadMetadata(ctx context.Context, sessionID string) (*runtimechat.Session, error) {
	atomic.AddInt32(&s.metadataCalls, 1)
	return s.InMemoryStorage.Load(ctx, sessionID)
}

// UpdatePreviewMetadata 落库成功即认为该行的预览已被修复。
func (s *countingWebSessionStorage) UpdatePreviewMetadata(ctx context.Context, sessionID, title, titleSource, summary string) error {
	if err := s.InMemoryStorage.UpdatePreviewMetadata(ctx, sessionID, title, titleSource, summary); err != nil {
		return err
	}
	delete(s.missingPreview, sessionID)
	return nil
}

func (s *countingWebSessionStorage) ListMetadataPage(ctx context.Context, userID string, limit, offset int) ([]*runtimechat.Session, error) {
	sessions, err := s.InMemoryStorage.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	if offset >= len(sessions) {
		return []*runtimechat.Session{}, nil
	}
	if offset > 0 {
		sessions = sessions[offset:]
	}
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	rows := make([]*runtimechat.Session, 0, len(sessions))
	for _, session := range sessions {
		if session == nil {
			continue
		}
		meta := session.CloneWithoutHistory()
		if meta.ID == s.stripSummaryID {
			meta.Metadata.Summary = ""
		}
		if meta.ID == s.stripTitleID {
			meta.Metadata.Title = ""
			meta.Metadata.TitleSource = ""
		}
		if s.missingPreview[meta.ID] {
			meta.Metadata.Title = ""
			meta.Metadata.TitleSource = ""
			meta.Metadata.Summary = ""
		}
		rows = append(rows, meta)
	}
	return rows, nil
}

func (s *countingWebSessionStorage) fullLoads() int {
	return int(atomic.LoadInt32(&s.loadCalls))
}

func newCountingWebSessionFixture(t *testing.T) (*countingWebSessionStorage, *runtimechat.SessionManager, *ChatSession) {
	t.Helper()
	storage := &countingWebSessionStorage{InMemoryStorage: runtimechat.NewInMemoryStorage()}
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	current, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create(current): %v", err)
	}
	current.Metadata.Title = "当前会话"
	current.Metadata.Summary = "当前摘要"
	current.AddMessage(*runtimetypes.NewUserMessage("当前会话的第一条消息"))
	if err := storage.Save(ctx, current); err != nil {
		t.Fatalf("save current: %v", err)
	}

	session := newWebTestSession()
	session.SessionManager = manager
	session.SessionUserID = "test-user"
	session.RuntimeSession = current
	// 空闲协调器：IsReady()=true，让 /resume 通过队列的命令门（真实进程空闲时
	// 正是这个状态；Interaction 为 nil 会被门判为「忙」而拒绝）。
	session.Interaction = &chatInteractionCoordinator{}
	return storage, manager, session
}

func webSessionListTitles(t *testing.T, rec *httptest.ResponseRecorder) []chatWebSessionListItem {
	t.Helper()
	var body struct {
		Sessions []chatWebSessionListItem `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal sessions: %v", err)
	}
	return body.Sessions
}

// 列表路径必须是元数据零 Load：元数据已带标题/摘要的候选不能再触发完整 Get。
func TestHandleChatWebAPISessions_MetadataOnlyListDoesNotLoadHistory(t *testing.T) {
	storage, manager, session := newCountingWebSessionFixture(t)
	ctx := context.Background()

	for _, spec := range []struct{ title, summary, message string }{
		// 摘要由存储层按既有规则从最后一条消息派生，这里按派生值断言。
		{"会话-A", "消息-A", "消息-A"},
		{"会话-B", "消息-B", "消息-B"},
	} {
		candidate, err := manager.Create(ctx, "test-user")
		if err != nil {
			t.Fatalf("manager.Create: %v", err)
		}
		candidate.Metadata.Title = spec.title
		candidate.Metadata.Summary = spec.summary
		candidate.AddMessage(*runtimetypes.NewUserMessage(spec.message))
		if err := storage.Save(ctx, candidate); err != nil {
			t.Fatalf("save candidate: %v", err)
		}
	}
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath+"?sort=created_at&scope=all", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	items := webSessionListTitles(t, rec)
	if len(items) != 3 {
		t.Fatalf("sessions len = %d, want 3 (%#v)", len(items), items)
	}
	if got := storage.fullLoads(); got != 0 {
		t.Fatalf("session list performed %d full history loads, want 0", got)
	}

	found := map[string]chatWebSessionListItem{}
	for _, item := range items {
		found[item.Title] = item
	}
	// 摘要在存储层按既有规则从最后一条消息派生（见上方 spec.summary）。
	for _, want := range []struct{ title, summary string }{{"会话-A", "消息-A"}, {"会话-B", "消息-B"}} {
		item, ok := found[want.title]
		if !ok {
			t.Fatalf("title %q missing from list %#v", want.title, items)
		}
		if item.Summary != want.summary {
			t.Fatalf("summary for %q = %q, want %q", want.title, item.Summary, want.summary)
		}
		if item.MessageCount != 1 {
			t.Fatalf("message_count for %q = %d, want 1", want.title, item.MessageCount)
		}
	}
}

// 元数据渲染不出预览（标题/摘要缺失）的少数行必须回退一次完整加载，
// 保持侧栏显示与旧的逐候选加载逐字一致。
func TestHandleChatWebAPISessions_MetadataIncompleteRowFallsBackToHistory(t *testing.T) {
	storage, manager, session := newCountingWebSessionFixture(t)
	ctx := context.Background()

	candidate, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	candidate.AddMessage(*runtimetypes.NewUserMessage("用户消息"))
	candidate.AddMessage(*runtimetypes.NewAssistantMessage("最后一条助手回复"))
	if err := storage.Save(ctx, candidate); err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	// 存储层会把标题/摘要派生并持久化；这里从元数据行里剥掉它们，
	// 模拟历史上确实缺少这些字段的少数会话。
	storage.stripTitleID = candidate.ID
	storage.stripSummaryID = candidate.ID
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	items := webSessionListTitles(t, rec)
	if got := storage.fullLoads(); got != 1 {
		t.Fatalf("fallback full loads = %d, want 1", got)
	}
	var target *chatWebSessionListItem
	for i := range items {
		if items[i].ID == candidate.ID {
			target = &items[i]
		}
	}
	if target == nil {
		t.Fatalf("candidate %q missing from list %#v", candidate.ID, items)
	}
	if target.Title != "用户消息" {
		t.Fatalf("fallback title = %q, want derived %q", target.Title, "用户消息")
	}
	if target.Summary != "最后一条助手回复" {
		t.Fatalf("fallback summary = %q, want %q", target.Summary, "最后一条助手回复")
	}
}

// resume 的目标校验只允许元数据单行读取，不得为此反序列化整段历史。
func TestHandleChatWebAPISessionsResume_UsesMetadataOnlyLookup(t *testing.T) {
	storage, manager, session := newCountingWebSessionFixture(t)
	ctx := context.Background()

	target, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create(target): %v", err)
	}
	target.Metadata.Title = "目标会话"
	target.Metadata.Summary = "目标摘要"
	target.AddMessage(*runtimetypes.NewUserMessage("目标会话消息"))
	if err := storage.Save(ctx, target); err != nil {
		t.Fatalf("save target: %v", err)
	}
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath,
		strings.NewReader(`{"session_id":"`+target.ID+`"}`))
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal resume response: %v", err)
	}
	if body.Status != "queued" {
		t.Fatalf("status = %q, want queued; body: %s", body.Status, rec.Body.String())
	}
	if got := storage.fullLoads(); got != 0 {
		t.Fatalf("resume performed %d full history loads, want 0", got)
	}
	if got := atomic.LoadInt32(&storage.metadataCalls); got != 1 {
		t.Fatalf("resume metadata loads = %d, want 1", got)
	}
}

// listResumeCandidateChatSessionMetadata 不返回空会话，且不触发完整 Load。
func TestListResumeCandidateChatSessionMetadataSkipsEmptySessions(t *testing.T) {
	storage, manager, _ := newCountingWebSessionFixture(t)
	ctx := context.Background()

	empty, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create(empty): %v", err)
	}
	if err := storage.Save(ctx, empty); err != nil {
		t.Fatalf("save empty: %v", err)
	}

	candidates, err := listResumeCandidateChatSessionMetadata(manager, "test-user", ChatSessionListFilter{}, "")
	if err != nil {
		t.Fatalf("listResumeCandidateChatSessionMetadata: %v", err)
	}
	if got := storage.fullLoads(); got != 0 {
		t.Fatalf("metadata candidate scan performed %d full loads, want 0", got)
	}
	for _, candidate := range candidates {
		if candidate.ID == empty.ID {
			t.Fatalf("empty session %q must not be a resume candidate", empty.ID)
		}
	}
}

// 缺标题/摘要的行只在首次列表回退一次完整加载：懒修复把派生预览写回元数据行后，
// 后续列表必须回归零 Load（否则侧栏在长尾数据上仍然每次都在读历史）。
func TestHandleChatWebAPISessions_RepairedPreviewMakesSubsequentListsZeroLoad(t *testing.T) {
	resetWebHTTPCaches()
	t.Cleanup(resetWebHTTPCaches)
	storage, manager, session := newCountingWebSessionFixture(t)
	ctx := context.Background()

	candidate, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	candidate.AddMessage(*runtimetypes.NewUserMessage("用户消息"))
	candidate.AddMessage(*runtimetypes.NewAssistantMessage("最后一条助手回复"))
	if err := storage.Save(ctx, candidate); err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	// 该行的元数据确实缺标题与摘要：只有完整加载才能渲染预览。
	storage.missingPreview = map[string]bool{candidate.ID: true}
	withWebTestSession(t, session)

	first := httptest.NewRecorder()
	HandleChatWebAPISessions(first, httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d; body: %s", first.Code, first.Body.String())
	}
	if got := storage.fullLoads(); got != 1 {
		t.Fatalf("first list full loads = %d, want 1", got)
	}
	firstItems := webSessionListTitles(t, first)
	var firstTarget *chatWebSessionListItem
	for i := range firstItems {
		if firstItems[i].ID == candidate.ID {
			firstTarget = &firstItems[i]
		}
	}
	if firstTarget == nil {
		t.Fatalf("candidate %q missing from first list %#v", candidate.ID, firstItems)
	}

	second := httptest.NewRecorder()
	HandleChatWebAPISessions(second, httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil))
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d; body: %s", second.Code, second.Body.String())
	}
	if got := storage.fullLoads(); got != 1 {
		t.Fatalf("second list performed %d full loads, want 1 (repair must be persisted)", got)
	}
	secondItems := webSessionListTitles(t, second)
	var secondTarget *chatWebSessionListItem
	for i := range secondItems {
		if secondItems[i].ID == candidate.ID {
			secondTarget = &secondItems[i]
		}
	}
	if secondTarget == nil {
		t.Fatalf("candidate %q missing from second list %#v", candidate.ID, secondItems)
	}
	if secondTarget.Title != firstTarget.Title || secondTarget.Summary != firstTarget.Summary {
		t.Fatalf("preview changed after repair: %q/%q → %q/%q",
			firstTarget.Title, firstTarget.Summary, secondTarget.Title, secondTarget.Summary)
	}
	if secondTarget.Title != "用户消息" || secondTarget.Summary != "最后一条助手回复" {
		t.Fatalf("repaired preview = %q/%q", secondTarget.Title, secondTarget.Summary)
	}
}
