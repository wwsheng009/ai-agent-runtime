package runtimeapi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/imageattach"
)

func runtimeUploadFixturePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 12), G: uint8(y * 12), B: 200, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("编码测试图片失败: %v", err)
	}
	return buffer.Bytes()
}

func runtimeUploadRequest(t *testing.T, files map[string][]byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, data := range files {
		part, err := writer.CreateFormFile(runtimeUploadFieldName, name)
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
	req := httptest.NewRequest(http.MethodPost, "/uploads", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func newRuntimeUploadRouter(t *testing.T, opts RuntimeUploadOptions) *mux.Router {
	t.Helper()
	router := mux.NewRouter()
	RegisterRuntimeUploadRoutes(router, opts)
	return router
}

func decodeRuntimeUploadResponse(t *testing.T, rec *httptest.ResponseRecorder) (int, []runtimeUploadEntry) {
	t.Helper()
	var payload struct {
		OK          bool                 `json:"ok"`
		Accepted    int                  `json:"accepted"`
		Attachments []runtimeUploadEntry `json:"attachments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析上传响应失败: %v（body=%s）", err, rec.Body.String())
	}
	if !payload.OK {
		t.Fatalf("ok 应为 true（body=%s）", rec.Body.String())
	}
	return payload.Accepted, payload.Attachments
}

func TestRuntimeUploadStoresPreparedArtifact(t *testing.T) {
	root := t.TempDir()
	router := newRuntimeUploadRouter(t, RuntimeUploadOptions{RootDir: root, Limits: imageattach.Limits{MaxDimension: 4}})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, runtimeUploadRequest(t, map[string][]byte{"shot.png": runtimeUploadFixturePNG(t, 8, 8)}))

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	accepted, entries := decodeRuntimeUploadResponse(t, rec)
	if accepted != 1 || len(entries) != 1 {
		t.Fatalf("应接纳 1 张: accepted=%d entries=%+v", accepted, entries)
	}
	if entries[0].Path == "" || entries[0].Skipped {
		t.Fatalf("应给出可用路径: %+v", entries[0])
	}
	if _, err := os.Stat(entries[0].Path); err != nil {
		t.Fatalf("落盘文件应存在: %v", err)
	}
	if entries[0].Width > 4 || entries[0].Height > 4 {
		t.Fatalf("应按上限缩放: %+v", entries[0])
	}

	// 边界校验：上传产出的路径必须被 submit_prompt.images 接受。
	allowed, notes := resolveRuntimeUploadImages([]string{entries[0].Path})
	if len(allowed) != 1 || len(notes) != 0 {
		t.Fatalf("上传路径应可发送: allowed=%v notes=%v", allowed, notes)
	}
}

func TestRuntimeUploadSkipsOversizeAndNonImage(t *testing.T) {
	root := t.TempDir()
	router := newRuntimeUploadRouter(t, RuntimeUploadOptions{RootDir: root, Limits: imageattach.Limits{MaxBytes: 16}})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, runtimeUploadRequest(t, map[string][]byte{
		"shot.png": runtimeUploadFixturePNG(t, 8, 8),
		"note.txt": []byte("这不是图片"),
	}))

	accepted, entries := decodeRuntimeUploadResponse(t, rec)
	if accepted != 0 {
		t.Fatalf("超限与非图片都不应被接纳，得到 accepted=%d (%+v)", accepted, entries)
	}
	if len(entries) != 2 {
		t.Fatalf("应逐条回报结果，得到 %d 条", len(entries))
	}
	for _, entry := range entries {
		if !entry.Skipped || entry.Note == "" {
			t.Fatalf("每条都应带原因: %+v", entry)
		}
	}
}

func TestRuntimeUploadUnavailableWithoutRoot(t *testing.T) {
	router := newRuntimeUploadRouter(t, RuntimeUploadOptions{})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, runtimeUploadRequest(t, map[string][]byte{"shot.png": runtimeUploadFixturePNG(t, 4, 4)}))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置目录应 503 降级，得到 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "uploads_unavailable") {
		t.Fatalf("降级原因应是机读的: %s", rec.Body.String())
	}
}

func TestRuntimeUploadMissingFileField(t *testing.T) {
	root := t.TempDir()
	router := newRuntimeUploadRouter(t, RuntimeUploadOptions{RootDir: root})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.Close(); err != nil {
		t.Fatalf("构造空 multipart 失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/uploads", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺字段应 400，得到 %d", rec.Code)
	}
}

func TestRuntimeUploadRejectsGet(t *testing.T) {
	root := t.TempDir()
	router := newRuntimeUploadRouter(t, RuntimeUploadOptions{RootDir: root})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405，得到 %d", rec.Code)
	}
}

func TestResolveRuntimeUploadImagesRejectsOutsidePaths(t *testing.T) {
	root := t.TempDir()
	newRuntimeUploadRouter(t, RuntimeUploadOptions{RootDir: root})
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, runtimeUploadFixturePNG(t, 4, 4), 0o644); err != nil {
		t.Fatalf("写外部文件失败: %v", err)
	}

	allowed, notes := resolveRuntimeUploadImages([]string{outside})
	if len(allowed) != 0 {
		t.Fatalf("附件目录外的路径必须被拒绝: %v", allowed)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "不在附件目录内") {
		t.Fatalf("应给出明确的拒绝原因: %v", notes)
	}

	// 前缀相近的兄弟目录同样不算在内（防止 /data/attachments-evil 绕过）。
	sibling := root + "-evil"
	if runtimeUploadPathWithin(root, filepath.Join(sibling, "x.png")) {
		t.Fatalf("兄弟目录不应被当成附件目录内")
	}
	if !runtimeUploadPathWithin(root, filepath.Join(root, "sub", "x.png")) {
		t.Fatalf("子目录应被接受")
	}
}

func TestResolveRuntimeUploadImagesWithoutRoot(t *testing.T) {
	// 未注册（或服务未配置目录）时：任何 images 都不放行，并给出可解释的说明。
	runtimeUploadRoot.Store("")
	allowed, notes := resolveRuntimeUploadImages([]string{filepath.Join(t.TempDir(), "a.png")})
	if len(allowed) != 0 || len(notes) != 1 {
		t.Fatalf("未配置目录应全部拒绝: allowed=%v notes=%v", allowed, notes)
	}
}

func TestResolveRuntimeUploadImagesRejectsUnusableImageInsideRoot(t *testing.T) {
	root := t.TempDir()
	newRuntimeUploadRouter(t, RuntimeUploadOptions{RootDir: root})
	broken := filepath.Join(root, "broken.png")
	if err := os.WriteFile(broken, []byte("不是图片"), 0o644); err != nil {
		t.Fatalf("写坏图失败: %v", err)
	}

	allowed, notes := resolveRuntimeUploadImages([]string{broken})
	if len(allowed) != 0 {
		t.Fatalf("坏图不应被放行: %v", allowed)
	}
	if len(notes) == 0 {
		t.Fatalf("坏图应给出说明")
	}
}

func TestAttachRuntimeImageNotesKeepsPayloadAdditive(t *testing.T) {
	payload := map[string]interface{}{"result": "ok"}
	attachRuntimeImageNotes(payload, nil, nil)
	if _, exists := payload["attached_images"]; exists {
		t.Fatalf("无图片时不应出现 attached_images")
	}
	if _, exists := payload["image_notes"]; exists {
		t.Fatalf("无说明时不应出现 image_notes")
	}
	attachRuntimeImageNotes(payload, []string{"a.png"}, []string{"已压缩"})
	if payload["attached_images"] != 1 {
		t.Fatalf("应报告挂上的图片数: %+v", payload)
	}
	if notes, ok := payload["image_notes"].([]string); !ok || len(notes) != 1 {
		t.Fatalf("应报告说明: %+v", payload)
	}
}
