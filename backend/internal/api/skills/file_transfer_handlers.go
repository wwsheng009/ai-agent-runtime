package skills

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/filetransport"
)

type FileTransferService interface {
	ReadFile(ctx context.Context, path string) ([]byte, string, error)
	WriteFile(ctx context.Context, path string, data []byte) (*filetransport.WriteResult, error)
	AppendFile(ctx context.Context, path string, data []byte) (*filetransport.WriteResult, error)
}

type fileReadRequest struct {
	Path string `json:"path"`
}

type fileWriteRequest struct {
	Path       string `json:"path"`
	DataBase64 string `json:"data_base64"`
}

func (h *Handler) SetFileTransferService(service FileTransferService) {
	if h == nil {
		return
	}
	h.fileTransferService = service
}

func (h *Handler) ReadRuntimeFile(w http.ResponseWriter, r *http.Request) {
	if h.fileTransferService == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "file transfer service not configured"))
		return
	}

	var req fileReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid file read payload"))
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "path is required"))
		return
	}

	data, absPath, err := h.fileTransferService.ReadFile(r.Context(), req.Path)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"file": map[string]interface{}{
			"path":        absPath,
			"data_base64": base64.StdEncoding.EncodeToString(data),
			"byte_count":  len(data),
		},
	})
}

func (h *Handler) WriteRuntimeFile(w http.ResponseWriter, r *http.Request) {
	h.writeRuntimeFileMutation(w, r, false)
}

func (h *Handler) AppendRuntimeFile(w http.ResponseWriter, r *http.Request) {
	h.writeRuntimeFileMutation(w, r, true)
}

func (h *Handler) writeRuntimeFileMutation(w http.ResponseWriter, r *http.Request, appendMode bool) {
	if h.fileTransferService == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "file transfer service not configured"))
		return
	}

	var req fileWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid file write payload"))
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "path is required"))
		return
	}
	if strings.TrimSpace(req.DataBase64) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "data_base64 is required"))
		return
	}

	data, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "data_base64 must be valid base64"))
		return
	}

	var result *filetransport.WriteResult
	if appendMode {
		result, err = h.fileTransferService.AppendFile(r.Context(), req.Path, data)
	} else {
		result, err = h.fileTransferService.WriteFile(r.Context(), req.Path, data)
	}
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	statusCode := http.StatusOK
	if appendMode {
		statusCode = http.StatusAccepted
	}
	h.writeJSON(w, statusCode, map[string]interface{}{
		"file": result,
	})
}
