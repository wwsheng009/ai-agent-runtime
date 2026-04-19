package skills

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type fakeConfigDocumentService struct {
	document *ConfigDocument
	preview  ConfigDocumentSaveRequest
	saved    ConfigDocumentSaveRequest
	saveErr  error
	loadErr  error
}

func (s *fakeConfigDocumentService) LoadDocument() (*ConfigDocument, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.document, nil
}

func (s *fakeConfigDocumentService) PreviewDocument(req ConfigDocumentSaveRequest) (*ConfigDocument, error) {
	s.preview = req
	return s.document, nil
}

func (s *fakeConfigDocumentService) SaveDocument(req ConfigDocumentSaveRequest) (*ConfigDocument, error) {
	if s.saveErr != nil {
		return nil, s.saveErr
	}
	s.saved = req
	return s.document, nil
}

func TestGetConfigDocument(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetConfigDocumentService(&fakeConfigDocumentService{
		document: &ConfigDocument{
			Path:   "E:/projects/ai/ai-agent-runtime/backend/configs/config.yaml",
			Format: "yaml",
			Raw:    "server:\n  host: 127.0.0.1\n",
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/config/document", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]ConfigDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "yaml", payload["document"].Format)
}

func TestUpdateConfigDocument(t *testing.T) {
	service := &fakeConfigDocumentService{
		document: &ConfigDocument{
			Path:   "E:/projects/ai/ai-agent-runtime/backend/configs/config.yaml",
			Format: "yaml",
			Raw:    "server:\n  host: 0.0.0.0\n",
		},
	}
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetConfigDocumentService(service)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"mode":"structured","parsed":{"providers":{"default_provider":"codex_fox"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/runtime/config/document", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "structured", service.saved.Mode)

	root, ok := service.saved.Parsed.(map[string]interface{})
	require.True(t, ok)
	providers, ok := root["providers"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "codex_fox", providers["default_provider"])
}

func TestPreviewConfigDocument(t *testing.T) {
	service := &fakeConfigDocumentService{
		document: &ConfigDocument{
			Path:   "E:/projects/ai/ai-agent-runtime/backend/configs/config.yaml",
			Format: "yaml",
			Raw:    "providers:\n  default_provider: codex_fox\n",
		},
	}
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetConfigDocumentService(service)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"mode":"raw","raw":"providers:\n  default_provider: codex_fox\n"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/config/document/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "raw", service.preview.Mode)
	require.NotNil(t, service.preview.Raw)
}

func TestConfigDocumentPayloadTopSections(t *testing.T) {
	req := &ConfigDocumentSaveRequest{
		Parsed: map[string]interface{}{
			"providers": map[string]interface{}{},
			"server":    map[string]interface{}{},
			"auth":      map[string]interface{}{},
		},
	}

	require.Equal(t, []string{"auth", "providers", "server"}, configDocumentPayloadTopSections(req))
}

func TestConfigDocumentSparseStructuredMerged(t *testing.T) {
	document := &ConfigDocument{
		Warnings: []string{
			"检测到本次 structured 保存只包含局部节点，已自动与当前有效配置合并后写入，避免覆盖整份运行时配置。",
		},
	}

	require.True(t, configDocumentSparseStructuredMerged(document))
	require.False(t, configDocumentSparseStructuredMerged(&ConfigDocument{}))
}
