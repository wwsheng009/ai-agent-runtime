package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// newSkillsWebTestSession 构造带一个「字段齐全」skill 的测试会话：
// 描述符里的触发词 / 依赖 / 来源 / 元数据都有值，用于校验详情面板的数据来源。
func newSkillsWebTestSession() *ChatSession {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__imagegen",
		sourcePath:   "/skills/imagegen",
		skill: &runtimeskill.Skill{
			Name:         "imagegen",
			Description:  "Generate images from a prompt",
			Version:      "1.2.0",
			Category:     "media",
			Tags:         []string{"image", "generation"},
			Capabilities: []string{"image.generate"},
			Triggers: []runtimeskill.Trigger{
				{Type: "keyword", Values: []string{"画图"}, Weight: 0.8},
			},
			Tools:  []string{"http_request"},
			Source: &runtimeskill.SkillSource{Path: "/skills/imagegen/SKILL.md", Dir: "/skills/imagegen", Layer: "project"},
		},
	})
	return &ChatSession{FunctionCatalog: catalog, FunctionRegistry: registry}
}

// decodeSkillsBody 解析 skills 端点的 JSON 响应体。
func decodeSkillsBody(t *testing.T, rec *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
}

// TestHandleChatWebAPISkills_List 锁定列表契约：与 /skills 同源、按当前会话目录返回。
func TestHandleChatWebAPISkills_List(t *testing.T) {
	withWebTestSession(t, newSkillsWebTestSession())

	req := httptest.NewRequest(http.MethodGet, ChatWebAPISkillsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISkills(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Count  int                   `json:"count"`
		Skills []chatWebSkillSummary `json:"skills"`
	}
	decodeSkillsBody(t, rec, &payload)
	if payload.Count != 1 || len(payload.Skills) != 1 {
		t.Fatalf("count = %d, skills = %d; want 1/1", payload.Count, len(payload.Skills))
	}
	item := payload.Skills[0]
	if item.Name != "imagegen" || item.FunctionName != "skill__imagegen" {
		t.Fatalf("unexpected item identity: %#v", item)
	}
	if item.Description == "" || item.Version != "1.2.0" || item.Category != "media" {
		t.Fatalf("list item must carry descriptor fields, got %#v", item)
	}
	if len(item.Labels) != 3 || len(item.Capabilities) != 1 {
		t.Fatalf("labels/capabilities missing: %#v", item)
	}
}

// TestHandleChatWebAPISkills_Detail 详情可按目录名或可调用名取，且带触发词/依赖/来源。
func TestHandleChatWebAPISkills_Detail(t *testing.T) {
	withWebTestSession(t, newSkillsWebTestSession())

	for _, name := range []string{"imagegen", "skill__imagegen"} {
		req := httptest.NewRequest(http.MethodGet, ChatWebAPISkillsPath+"/"+name, nil)
		rec := httptest.NewRecorder()
		HandleChatWebAPISkills(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200; body=%s", name, rec.Code, rec.Body.String())
		}
		var detail chatWebSkillDetail
		decodeSkillsBody(t, rec, &detail)
		if detail.Name != "imagegen" {
			t.Fatalf("GET %s: name = %q, want imagegen", name, detail.Name)
		}
		if len(detail.Triggers) != 1 || detail.Triggers[0].Type != "keyword" {
			t.Fatalf("GET %s: triggers missing: %#v", name, detail.Triggers)
		}
		if len(detail.Dependencies) != 1 || detail.Dependencies[0].Name != "http_request" {
			t.Fatalf("GET %s: dependencies missing: %#v", name, detail.Dependencies)
		}
		if detail.Source == nil || detail.Source.Layer != "project" {
			t.Fatalf("GET %s: source missing: %#v", name, detail.Source)
		}
		if detail.Metadata == nil || detail.Metadata["skill_name"] != "imagegen" {
			t.Fatalf("GET %s: metadata missing: %#v", name, detail.Metadata)
		}
	}
}

// TestHandleChatWebAPISkills_Unknown 未知 skill 返回 404 skill_not_found，不回退到列表。
func TestHandleChatWebAPISkills_Unknown(t *testing.T) {
	withWebTestSession(t, newSkillsWebTestSession())

	req := httptest.NewRequest(http.MethodGet, ChatWebAPISkillsPath+"/no-such-skill", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISkills(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "skill_not_found") {
		t.Fatalf("body must carry skill_not_found, got %s", rec.Body.String())
	}
}

// TestHandleChatWebAPISkills_DeepPath 更深子路径不当作 skill 名，返回 404。
func TestHandleChatWebAPISkills_DeepPath(t *testing.T) {
	withWebTestSession(t, newSkillsWebTestSession())

	req := httptest.NewRequest(http.MethodGet, ChatWebAPISkillsPath+"/imagegen/extra", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISkills(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "skill_not_found") {
		t.Fatalf("deep path must not degrade to the list response, got %s", rec.Body.String())
	}
}

// TestHandleChatWebAPISkills_NoSession 无活动会话时 503 skills_unavailable。
func TestHandleChatWebAPISkills_NoSession(t *testing.T) {
	withWebTestSession(t, nil)

	req := httptest.NewRequest(http.MethodGet, ChatWebAPISkillsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISkills(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "skills_unavailable") {
		t.Fatalf("body must carry skills_unavailable, got %s", rec.Body.String())
	}
}

// TestHandleChatWebAPISkills_MethodNotAllowed 仅支持 GET。
func TestHandleChatWebAPISkills_MethodNotAllowed(t *testing.T) {
	withWebTestSession(t, newSkillsWebTestSession())

	req := httptest.NewRequest(http.MethodPost, ChatWebAPISkillsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISkills(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", rec.Code, rec.Body.String())
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}

// TestHandleChatWebAPISkills_EmptyCatalog 无 skill 时返回 count=0 的空数组（不是 null）。
func TestHandleChatWebAPISkills_EmptyCatalog(t *testing.T) {
	session := &ChatSession{
		FunctionCatalog:  newAICLIFunctionCatalog("openai", functions.NewFunctionRegistry()),
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	withWebTestSession(t, session)

	req := httptest.NewRequest(http.MethodGet, ChatWebAPISkillsPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPISkills(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"skills":[]`) {
		t.Fatalf("empty catalog must serialize an empty array, got %s", rec.Body.String())
	}
}
