package restapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// putBinding 是测试助手：对 fx 发起 PUT bindings 请求。
func putBinding(t *testing.T, fx *fixture, connectorID, chat, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, fx.router, http.MethodPut,
		"/connectors/"+connectorID+"/bindings/"+chat, body)
}

func decodeBinding(t *testing.T, body []byte) BindingView {
	t.Helper()
	var single BindingView
	if err := json.Unmarshal(body, &single); err != nil {
		t.Fatalf("decode binding: %v\n%s", err, body)
	}
	return single
}

func TestBindingsListRoundTrip(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	if err := fx.store.PutBinding(connectors.Binding{
		ConnectorID: "c1",
		Chat:        "chat-1",
		Session:     connectors.SessionRef{SessionID: "sess-1"},
		UpdatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	recorder := do(t, fx.router, http.MethodGet, "/connectors/bindings", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("list bindings should 200, got %d %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Items []BindingView `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(payload.Items))
	}
	item := payload.Items[0]
	if item.ConnectorID != "c1" || item.Chat != "chat-1" || item.SessionID != "sess-1" {
		t.Fatalf("item = %+v", item)
	}
	if item.Connector == nil || item.Connector.ID != "c1" || item.Connector.Provider != "fakeweb" {
		t.Fatalf("connector summary = %+v", item.Connector)
	}
}

func TestBindingsPutCreatesAndUpdates(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))

	// 创建（expected 缺省 = 必须不存在）。
	recorder := putBinding(t, fx, "c1", "chat-1", `{"session_id":"sess-a"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create binding should 200, got %d %s", recorder.Code, recorder.Body.String())
	}
	created := decodeBinding(t, recorder.Body.Bytes())
	if created.SessionID != "sess-a" || created.UpdatedAt == "" {
		t.Fatalf("created = %+v", created)
	}

	// 重复创建（无 expected）→ 409 + current。
	recorder = putBinding(t, fx, "c1", "chat-1", `{"session_id":"sess-b"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("recreate without expected should 409, got %d %s", recorder.Code, recorder.Body.String())
	}

	// 带 expected 更新 → 200。
	recorder = putBinding(t, fx, "c1", "chat-1", `{"session_id":"sess-b","expected_updated_at":"`+created.UpdatedAt+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update with expected should 200, got %d %s", recorder.Code, recorder.Body.String())
	}
	updated := decodeBinding(t, recorder.Body.Bytes())
	if updated.SessionID != "sess-b" {
		t.Fatalf("updated session = %+v", updated)
	}

	// 陈旧 expected（= 第一次创建的时间戳，已被第二次更新覆盖）→ 409。
	recorder = putBinding(t, fx, "c1", "chat-1", `{"session_id":"sess-c","expected_updated_at":"`+created.UpdatedAt+`"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale expected should 409, got %d %s", recorder.Code, recorder.Body.String())
	}

	// 视图读到最新值。
	if got, ok := fx.store.GetBinding("c1", "chat-1"); !ok || got.Session.SessionID != "sess-b" {
		t.Fatalf("store binding = %+v ok=%v", got, ok)
	}
}

func TestBindingsDeleteUnbinds(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	if err := fx.store.PutBinding(connectors.Binding{
		ConnectorID: "c1",
		Chat:        "chat-1",
		Session:     connectors.SessionRef{SessionID: "sess-1"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	recorder := do(t, fx.router, http.MethodDelete, "/connectors/c1/bindings/chat-1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete should 200, got %d %s", recorder.Code, recorder.Body.String())
	}

	// 墓碑语义：GetBinding 仍命中但 Session 为零值（/new 同款）。
	got, ok := fx.store.GetBinding("c1", "chat-1")
	if !ok || !got.Session.IsZero() {
		t.Fatalf("tombstone binding = %+v ok=%v", got, ok)
	}
}

func TestBindingsValidation(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"未知连接器", http.MethodPut, "/connectors/nope/bindings/chat-1", `{"session_id":"s"}`, http.StatusNotFound},
		{"缺 session_id", http.MethodPut, "/connectors/c1/bindings/chat-1", `{}`, http.StatusBadRequest},
		{"缺 chat", http.MethodPut, "/connectors/c1/bindings/%20", `{"session_id":"s"}`, http.StatusBadRequest},
		{"expected 非法", http.MethodPut, "/connectors/c1/bindings/chat-1", `{"session_id":"s","expected_updated_at":"yesterday"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		recorder := do(t, fx.router, tc.method, tc.path, tc.body)
		if recorder.Code != tc.want {
			t.Fatalf("%s: got %d want %d (%s)", tc.name, recorder.Code, tc.want, recorder.Body.String())
		}
	}
}

func TestBindingsAllowedUsersEnforced(t *testing.T) {
	restricted := baseConfig("c1", "fakeweb")
	restricted.Policy.AllowedUsers = []string{"owner-chat"}
	fx := newFixture(t, allowAll, restricted)

	recorder := putBinding(t, fx, "c1", "chat-stranger", `{"session_id":"s"}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-whitelisted chat should 403, got %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = putBinding(t, fx, "c1", "owner-chat", `{"session_id":"s"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("whitelisted chat should 200, got %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestBindingsRouteOrderBindingsNotTreatedAsID(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1", "fakeweb"))
	// GET /connectors/bindings 必须命中列表而不是 get("bindings")。
	recorder := do(t, fx.router, http.MethodGet, "/connectors/bindings", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("literal /bindings should 200, got %d %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Items []BindingView `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("list payload: %v", err)
	}
	if payload.Items == nil {
		t.Fatalf("expected items array, got %s", recorder.Body.String())
	}
}
