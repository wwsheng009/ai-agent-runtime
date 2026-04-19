package skills

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

type ConfigDocument struct {
	Path                   string                       `json:"path"`
	Format                 string                       `json:"format"`
	Raw                    string                       `json:"raw"`
	Parsed                 interface{}                  `json:"parsed"`
	Sections               []ConfigDocumentSection      `json:"sections,omitempty"`
	SizeBytes              int                          `json:"size_bytes"`
	UpdatedAt              string                       `json:"updated_at,omitempty"`
	Warnings               []string                     `json:"warnings,omitempty"`
	RestartRequired        bool                         `json:"restart_required"`
	SupportsStructuredSave bool                         `json:"supports_structured_save"`
	RuntimeImpact          *ConfigDocumentRuntimeImpact `json:"runtime_impact,omitempty"`
}

type ConfigDocumentRuntimeImpact struct {
	ChangedPaths         []string `json:"changed_paths,omitempty"`
	HotReloadPaths       []string `json:"hot_reload_paths,omitempty"`
	RestartRequiredPaths []string `json:"restart_required_paths,omitempty"`
	InactivePaths        []string `json:"inactive_paths,omitempty"`
	AppliedPaths         []string `json:"applied_paths,omitempty"`
}

type ConfigDocumentSection struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	ItemCount int    `json:"item_count,omitempty"`
}

type ConfigDocumentSaveRequest struct {
	Raw       *string     `json:"raw,omitempty"`
	Parsed    interface{} `json:"parsed,omitempty"`
	Mode      string      `json:"mode,omitempty"`
	ChangedBy string      `json:"changed_by,omitempty"`
}

type ConfigDocumentService interface {
	LoadDocument() (*ConfigDocument, error)
	PreviewDocument(req ConfigDocumentSaveRequest) (*ConfigDocument, error)
	SaveDocument(req ConfigDocumentSaveRequest) (*ConfigDocument, error)
}

func (h *Handler) SetConfigDocumentService(service ConfigDocumentService) {
	h.configDocumentService = service
}

func (h *Handler) GetConfigDocument(w http.ResponseWriter, r *http.Request) {
	if h.configDocumentService == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "config document service not configured"))
		return
	}

	document, err := h.configDocumentService.LoadDocument()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to load config document", err))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"document": document,
	})
}

func (h *Handler) UpdateConfigDocument(w http.ResponseWriter, r *http.Request) {
	if h.configDocumentService == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "config document service not configured"))
		return
	}

	req := &ConfigDocumentSaveRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		h.auditConfigDocumentAction(r, "save", "invalid_request", nil, nil, logger.String("reason", "invalid_json"))
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid config document payload"))
		return
	}
	if req.Raw == nil && req.Parsed == nil {
		h.auditConfigDocumentAction(r, "save", "invalid_request", req, nil, logger.String("reason", "missing_content"))
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "raw or parsed config document content is required"))
		return
	}
	req.Mode = strings.TrimSpace(req.Mode)

	document, err := h.configDocumentService.SaveDocument(*req)
	if err != nil {
		h.auditConfigDocumentAction(r, "save", "failed", req, nil, logger.Err(err))
		h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrConfigInvalid, "failed to save config document", err))
		return
	}
	h.auditConfigDocumentAction(r, "save", "success", req, document)

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"saved":    true,
		"document": document,
	})
}

func (h *Handler) PreviewConfigDocument(w http.ResponseWriter, r *http.Request) {
	if h.configDocumentService == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "config document service not configured"))
		return
	}

	req := &ConfigDocumentSaveRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		h.auditConfigDocumentAction(r, "preview", "invalid_request", nil, nil, logger.String("reason", "invalid_json"))
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid config document payload"))
		return
	}
	if req.Raw == nil && req.Parsed == nil {
		h.auditConfigDocumentAction(r, "preview", "invalid_request", req, nil, logger.String("reason", "missing_content"))
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "raw or parsed config document content is required"))
		return
	}
	req.Mode = strings.TrimSpace(req.Mode)

	document, err := h.configDocumentService.PreviewDocument(*req)
	if err != nil {
		h.auditConfigDocumentAction(r, "preview", "failed", req, nil, logger.Err(err))
		h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrConfigInvalid, "failed to preview config document", err))
		return
	}
	h.auditConfigDocumentAction(r, "preview", "success", req, document)

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"document": document,
	})
}

func (h *Handler) auditConfigDocumentAction(
	r *http.Request,
	action string,
	outcome string,
	req *ConfigDocumentSaveRequest,
	document *ConfigDocument,
	extraFields ...interface{},
) {
	fields := []interface{}{
		logger.String("action", action),
		logger.String("outcome", outcome),
		logger.String("remote_ip", requestRemoteIP(r)),
		logger.RequestID(logger.GetRequestID(r.Context())),
	}

	if req != nil {
		fields = append(fields,
			logger.String("changed_by", strings.TrimSpace(req.ChangedBy)),
			logger.String("mode", configDocumentRequestMode(req)),
			logger.Any("payload_top_sections", configDocumentPayloadTopSections(req)),
			logger.Int("payload_top_section_count", len(configDocumentPayloadTopSections(req))),
		)
	}

	if document != nil {
		fields = append(fields,
			logger.String("document_path", strings.TrimSpace(document.Path)),
			logger.String("document_format", strings.TrimSpace(document.Format)),
			logger.Any("document_top_sections", configDocumentSectionKeys(document)),
			logger.Int("document_top_section_count", len(configDocumentSectionKeys(document))),
			logger.Bool("sparse_structured_merged", configDocumentSparseStructuredMerged(document)),
		)
	}

	fields = append(fields, extraFields...)

	adminLogger := logger.Admin().Named("config_document")
	switch outcome {
	case "invalid_request":
		adminLogger.Warn("runtime config document action", fieldsToZap(fields)...)
	case "failed":
		adminLogger.Error("runtime config document action", fieldsToZap(fields)...)
	default:
		adminLogger.Info("runtime config document action", fieldsToZap(fields)...)
	}
}

func configDocumentRequestMode(req *ConfigDocumentSaveRequest) string {
	if req == nil {
		return ""
	}
	mode := strings.TrimSpace(req.Mode)
	if mode != "" {
		return mode
	}
	if req.Raw != nil {
		return "raw"
	}
	if req.Parsed != nil {
		return "structured"
	}
	return ""
}

func configDocumentPayloadTopSections(req *ConfigDocumentSaveRequest) []string {
	if req == nil {
		return nil
	}
	root, ok := req.Parsed.(map[string]interface{})
	if !ok || len(root) == 0 {
		return nil
	}

	keys := make([]string, 0, len(root))
	for key := range root {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func configDocumentSectionKeys(document *ConfigDocument) []string {
	if document == nil || len(document.Sections) == 0 {
		return nil
	}
	keys := make([]string, 0, len(document.Sections))
	for _, section := range document.Sections {
		key := strings.TrimSpace(section.Key)
		if key == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func configDocumentSparseStructuredMerged(document *ConfigDocument) bool {
	if document == nil {
		return false
	}
	for _, warning := range document.Warnings {
		if strings.Contains(warning, "局部节点") && strings.Contains(warning, "自动") {
			return true
		}
	}
	return false
}
