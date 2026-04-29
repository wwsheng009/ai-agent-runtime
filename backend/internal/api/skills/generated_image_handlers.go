package skills

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// GetSessionGeneratedImage returns a generated image that has already been
// registered on an assistant message inside the target session.
func (h *Handler) GetSessionGeneratedImage(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session manager not configured"))
		return
	}

	vars := mux.Vars(r)
	sessionID := strings.TrimSpace(vars["id"])
	name := strings.TrimSpace(vars["name"])
	if sessionID == "" || name == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id and image name are required"))
		return
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid image name"))
		return
	}

	session, err := h.sessionManager.GetSession(r.Context(), sessionID)
	if err != nil {
		h.writeError(w, http.StatusNotFound, err)
		return
	}

	absPath, mimeType, sha256Sum, ok := lookupGeneratedImagePath(session.GetMessages(), name)
	if !ok {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrAPINotFound, "generated image not found"))
		return
	}

	info, statErr := os.Stat(absPath)
	if statErr != nil || !info.Mode().IsRegular() {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrAPINotFound, "generated image file missing"))
		return
	}
	etag := generatedImageETag(info, sha256Sum)

	file, openErr := os.Open(absPath)
	if openErr != nil {
		h.writeError(w, http.StatusInternalServerError, openErr)
		return
	}
	defer file.Close()

	if mimeType == "" {
		if guess := mime.TypeByExtension(strings.ToLower(filepath.Ext(absPath))); guess != "" {
			mimeType = guess
		} else {
			mimeType = "image/png"
		}
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", cacheControlHeader(h.generatedImageCacheMaxAge()))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, filepath.Base(absPath)))
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")

	http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), file)
}

func lookupGeneratedImagePath(messages []types.Message, name string) (string, string, string, bool) {
	if len(messages) == 0 || strings.TrimSpace(name) == "" {
		return "", "", "", false
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			continue
		}

		raw, ok := msg.Metadata[runtimellm.MetadataKeyGeneratedImages]
		if !ok || raw == nil {
			continue
		}

		items := normalizeGeneratedImageEntries(raw)
		for _, entry := range items {
			savedPath := strings.TrimSpace(stringFromAny(entry["saved_path"]))
			if savedPath == "" {
				continue
			}

			entryID := strings.TrimSpace(stringFromAny(entry["id"]))
			sanitizedID := sanitizeGeneratedImageName(entryID)
			base := filepath.Base(savedPath)
			if name == base || name == entryID || name == sanitizedID {
				return savedPath, strings.TrimSpace(stringFromAny(entry["mime_type"])), strings.TrimSpace(stringFromAny(entry["sha256"])), true
			}
		}
	}

	return "", "", "", false
}

func generatedImageETag(info os.FileInfo, sha256 string) string {
	if strings.TrimSpace(sha256) != "" {
		return `"` + strings.TrimSpace(sha256) + `"`
	}
	if info == nil {
		return `""`
	}
	return fmt.Sprintf(`"%x-%x"`, info.ModTime().UnixNano(), info.Size())
}

func normalizeGeneratedImageEntries(raw interface{}) []map[string]interface{} {
	switch v := raw.(type) {
	case []map[string]interface{}:
		return v
	case []types.Metadata:
		out := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			if item == nil {
				continue
			}
			out = append(out, map[string]interface{}(item))
		}
		return out
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func stringFromAny(value interface{}) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func sanitizeGeneratedImageName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z':
			builder.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			builder.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch == '-' || ch == '_':
			builder.WriteRune(ch)
		default:
			builder.WriteByte('_')
		}
	}

	return builder.String()
}
