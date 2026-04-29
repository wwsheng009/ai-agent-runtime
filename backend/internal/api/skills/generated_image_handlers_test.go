package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestGetSessionGeneratedImage_ReturnsImageByRawAndSanitizedName(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	t.Cleanup(sessionManager.Stop)

	session, err := sessionManager.CreateSession(ctx, "user-1")
	require.NoError(t, err)

	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "image_1.png")
	imageBytes := []byte("generated image bytes")
	require.NoError(t, os.WriteFile(imagePath, imageBytes, 0o644))

	assistant := runtimetypes.NewAssistantMessage("Generated image")
	assistant.Metadata["generated_images"] = []map[string]interface{}{
		{
			"id":             "image:1",
			"status":         "completed",
			"revised_prompt": "a tiny robot",
			"mime_type":      "image/png",
			"saved_path":     imagePath,
			"sha256":         "deadbeef",
			"byte_count":     len(imageBytes),
		},
	}
	session.AddMessage(*assistant)
	require.NoError(t, storage.Update(ctx, session))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	for _, name := range []string{"image_1", url.PathEscape("image:1")} {
		req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/generated-images/"+name, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
		require.Contains(t, rec.Header().Get("Content-Disposition"), "image_1.png")
		require.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
		require.Equal(t, "private, max-age=3600", rec.Header().Get("Cache-Control"))
		require.Equal(t, `"deadbeef"`, rec.Header().Get("ETag"))
		require.Equal(t, imageBytes, rec.Body.Bytes())
	}
}

func TestGetSessionGeneratedImage_UsesSha256ETagAndSupportsConditionalAndRangeRequests(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	t.Cleanup(sessionManager.Stop)

	session, err := sessionManager.CreateSession(ctx, "user-1")
	require.NoError(t, err)

	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "image_2.png")
	imageBytes := []byte("generated image bytes for etag")
	require.NoError(t, os.WriteFile(imagePath, imageBytes, 0o644))

	assistant := runtimetypes.NewAssistantMessage("Generated image")
	assistant.Metadata["generated_images"] = []map[string]interface{}{
		{
			"id":             "image:2",
			"status":         "completed",
			"revised_prompt": "a tiny robot",
			"mime_type":      "image/png",
			"saved_path":     imagePath,
			"sha256":         "sha-2",
			"byte_count":     len(imageBytes),
		},
	}
	session.AddMessage(*assistant)
	require.NoError(t, storage.Update(ctx, session))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	firstReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/generated-images/image_2", nil)
	firstRec := httptest.NewRecorder()
	router.ServeHTTP(firstRec, firstReq)

	require.Equal(t, http.StatusOK, firstRec.Code)
	require.Equal(t, `"sha-2"`, firstRec.Header().Get("ETag"))
	require.Equal(t, "bytes", firstRec.Header().Get("Accept-Ranges"))

	conditionalReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/generated-images/image_2", nil)
	conditionalReq.Header.Set("If-None-Match", firstRec.Header().Get("ETag"))
	conditionalRec := httptest.NewRecorder()
	router.ServeHTTP(conditionalRec, conditionalReq)

	require.Equal(t, http.StatusNotModified, conditionalRec.Code)
	require.Empty(t, conditionalRec.Body.Bytes())

	rangeReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/generated-images/image_2", nil)
	rangeReq.Header.Set("Range", "bytes=5-10")
	rangeRec := httptest.NewRecorder()
	router.ServeHTTP(rangeRec, rangeReq)

	require.Equal(t, http.StatusPartialContent, rangeRec.Code)
	require.Equal(t, imageBytes[5:11], rangeRec.Body.Bytes())
	require.Equal(t, "bytes 5-10/"+strconv.Itoa(len(imageBytes)), rangeRec.Header().Get("Content-Range"))
}

func TestGetSessionGeneratedImage_UsesConfiguredCacheMaxAge(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	t.Cleanup(sessionManager.Stop)

	session, err := sessionManager.CreateSession(ctx, "user-1")
	require.NoError(t, err)

	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "image_3.png")
	require.NoError(t, os.WriteFile(imagePath, []byte("generated image bytes"), 0o644))

	assistant := runtimetypes.NewAssistantMessage("Generated image")
	assistant.Metadata["generated_images"] = []map[string]interface{}{
		{
			"id":             "image:3",
			"status":         "completed",
			"revised_prompt": "cache control",
			"mime_type":      "image/png",
			"saved_path":     imagePath,
		},
	}
	session.AddMessage(*assistant)
	require.NoError(t, storage.Update(ctx, session))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.runtimeConfig = &runtimecfg.RuntimeConfig{
		Images: runtimecfg.ImagesConfig{
			CacheMaxAge: 0,
		},
	}

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/generated-images/image_3", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestGetSessionGeneratedImage_ReturnsNotFoundWhenMetadataMissing(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	t.Cleanup(sessionManager.Stop)

	session, err := sessionManager.CreateSession(ctx, "user-1")
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/generated-images/missing", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGetSessionGeneratedImage_ReturnsNotFoundWhenFileMissing(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	t.Cleanup(sessionManager.Stop)

	session, err := sessionManager.CreateSession(ctx, "user-1")
	require.NoError(t, err)

	assistant := runtimetypes.NewAssistantMessage("Generated image")
	assistant.Metadata["generated_images"] = []map[string]interface{}{
		{
			"id":             "image-1",
			"status":         "completed",
			"revised_prompt": "missing file",
			"mime_type":      "image/png",
			"saved_path":     filepath.Join(t.TempDir(), "missing.png"),
		},
	}
	session.AddMessage(*assistant)
	require.NoError(t, storage.Update(ctx, session))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/generated-images/image-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGetSessionGeneratedImage_RejectsInvalidName(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	t.Cleanup(sessionManager.Stop)

	session, err := sessionManager.CreateSession(ctx, "user-1")
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/generated-images/bad..name", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetSessionGeneratedImage_RejectsEncodedTraversalName(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	t.Cleanup(sessionManager.Stop)

	session, err := sessionManager.CreateSession(ctx, "user-1")
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	router.UseEncodedPath()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/generated-images/..%2fpasswd", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetSessionGeneratedImage_RequiresSessionManager(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/session-1/generated-images/image", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
