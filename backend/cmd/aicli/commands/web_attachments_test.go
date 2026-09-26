package commands

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func webAttachmentUploadRequest(t *testing.T, files map[string][]byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, data := range files {
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatalf("构造 multipart 失败: %v", err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatalf("写入 multipart 失败: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIAttachmentsPath, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func decodeWebAttachmentResponse(t *testing.T, rec *httptest.ResponseRecorder) (int, []chatWebAttachmentEntry) {
	t.Helper()
	var payload struct {
		Status      string                   `json:"status"`
		Accepted    int                      `json:"accepted"`
		Attachments []chatWebAttachmentEntry `json:"attachments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析上传响应失败: %v（body=%s）", err, rec.Body.String())
	}
	if payload.Status != "ok" {
		t.Fatalf("status = %q, want ok（body=%s）", payload.Status, rec.Body.String())
	}
	return payload.Accepted, payload.Attachments
}

func TestHandleChatWebAPIAttachments_UploadsAndPrepares(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	data, err := os.ReadFile(writeImageFixtureNamed(t, "web-shot.png"))
	if err != nil {
		t.Fatalf("读取测试图片失败: %v", err)
	}

	rec := httptest.NewRecorder()
	HandleChatWebAPIAttachments(rec, webAttachmentUploadRequest(t, map[string][]byte{"web-shot.png": data}))

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	accepted, entries := decodeWebAttachmentResponse(t, rec)
	if accepted != 1 || len(entries) != 1 {
		t.Fatalf("应接纳 1 张，得到 accepted=%d entries=%+v", accepted, entries)
	}
	if entries[0].Path == "" || entries[0].Skipped {
		t.Fatalf("可用图片应给出 path: %+v", entries[0])
	}
	if _, err := os.Stat(entries[0].Path); err != nil {
		t.Fatalf("返回的路径应真实存在: %v", err)
	}
	if entries[0].Width == 0 || entries[0].Height == 0 {
		t.Fatalf("应报告图片尺寸: %+v", entries[0])
	}
	// 上传只落盘，不登记到会话附件（登记发生在发送瞬间），避免"上传了没发"粘住下一轮。
	if len(session.ImagePaths) != 0 {
		t.Fatalf("上传不应改动会话附件列表: %v", session.ImagePaths)
	}
}

func TestHandleChatWebAPIAttachments_RejectsNonImage(t *testing.T) {
	withWebTestSession(t, newWebTestSession())

	rec := httptest.NewRecorder()
	HandleChatWebAPIAttachments(rec, webAttachmentUploadRequest(t, map[string][]byte{
		"note.txt": []byte("这不是图片"),
	}))

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, want 200（非图片是单条跳过，不是整请求失败）（body=%s）", rec.Code, rec.Body.String())
	}
	accepted, entries := decodeWebAttachmentResponse(t, rec)
	if accepted != 0 || len(entries) != 1 {
		t.Fatalf("非图片不应被接纳: accepted=%d entries=%+v", accepted, entries)
	}
	if !entries[0].Skipped || !strings.Contains(entries[0].Note, "不是可识别的图片") {
		t.Fatalf("跳过原因应写明: %+v", entries[0])
	}
}

func TestHandleChatWebAPIAttachments_RequiresSession(t *testing.T) {
	withWebTestSession(t, nil)

	rec := httptest.NewRecorder()
	HandleChatWebAPIAttachments(rec, webAttachmentUploadRequest(t, map[string][]byte{"a.png": {1, 2, 3}}))

	if rec.Code != http.StatusConflict {
		t.Fatalf("无会话应返回 409，得到 %d", rec.Code)
	}
}

func TestHandleChatWebAPIAttachments_MissingFileField(t *testing.T) {
	withWebTestSession(t, newWebTestSession())

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.Close(); err != nil {
		t.Fatalf("构造空 multipart 失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIAttachmentsPath, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	rec := httptest.NewRecorder()
	HandleChatWebAPIAttachments(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 file 字段应返回 400，得到 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "file") {
		t.Fatalf("拒绝原因应点明缺失字段: %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIAttachments_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleChatWebAPIAttachments(rec, httptest.NewRequest(http.MethodGet, ChatWebAPIAttachmentsPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应返回 405，得到 %d", rec.Code)
	}
}

func webInputRequest(t *testing.T, payload any) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化请求体失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIInputPath, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestHandleChatWebAPIInput_AttachesImagePaths(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)
	source := writeImageFixtureNamed(t, "attach-me.png")

	rec := httptest.NewRecorder()
	HandleChatWebAPIInput(rec, webInputRequest(t, map[string]any{
		"prompt":      "看看这张图",
		"image_paths": []string{source},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	var payload struct {
		Status         string   `json:"status"`
		AttachedImages int      `json:"attached_images"`
		ImageNotes     []string `json:"image_notes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, rec.Body.String())
	}
	if payload.Status != "queued" || payload.AttachedImages != 1 {
		t.Fatalf("应接纳 1 张并排队: %+v", payload)
	}
	if len(session.ImagePaths) != 1 {
		t.Fatalf("会话附件应有 1 项: %v", session.ImagePaths)
	}

	// 同一路径重复发送：去重，不重复入列。
	rec = httptest.NewRecorder()
	HandleChatWebAPIInput(rec, webInputRequest(t, map[string]any{
		"prompt":      "再发一次同一张",
		"image_paths": []string{source},
	}))
	if len(session.ImagePaths) != 1 {
		t.Fatalf("重复路径不应重复入列: %v", session.ImagePaths)
	}
}

func TestHandleChatWebAPIInput_SkipsUnusableImagePaths(t *testing.T) {
	session := newWebTestSession()
	withWebTestSession(t, session)

	rec := httptest.NewRecorder()
	HandleChatWebAPIInput(rec, webInputRequest(t, map[string]any{
		"prompt":      "这张其实不存在",
		"image_paths": []string{os.TempDir() + string(os.PathSeparator) + "missing-image-xyz.png"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	var payload struct {
		Status         string   `json:"status"`
		AttachedImages int      `json:"attached_images"`
		ImageNotes     []string `json:"image_notes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, rec.Body.String())
	}
	if payload.Status != "queued" || payload.AttachedImages != 0 {
		t.Fatalf("不可用图片不应被接纳: %+v", payload)
	}
	if len(payload.ImageNotes) == 0 {
		t.Fatalf("应给出跳过原因: %s", rec.Body.String())
	}
	if len(session.ImagePaths) != 0 {
		t.Fatalf("会话附件不应有内容: %v", session.ImagePaths)
	}
}

func TestHandleChatWebAPIInput_PlainPromptResponseUnchanged(t *testing.T) {
	withWebTestSession(t, newWebTestSession())

	rec := httptest.NewRecorder()
	HandleChatWebAPIInput(rec, webInputRequest(t, map[string]any{"prompt": "纯文本"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, want 200", rec.Code)
	}
	// 不带 image_paths 的调用不应出现附加字段（旧客户端契约不变）。
	if strings.Contains(rec.Body.String(), "attached_images") || strings.Contains(rec.Body.String(), "image_notes") {
		t.Fatalf("纯文本响应不应带附件字段: %s", rec.Body.String())
	}
}
