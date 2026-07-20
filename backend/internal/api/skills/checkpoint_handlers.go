package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
)

type checkpointSummary struct {
	ID                       string                       `json:"id"`
	SessionID                string                       `json:"session_id"`
	TaskID                   string                       `json:"task_id,omitempty"`
	Reason                   string                       `json:"reason,omitempty"`
	HistoryHash              string                       `json:"history_hash,omitempty"`
	MessageCount             int                          `json:"message_count"`
	ConversationExact        bool                         `json:"conversation_exact,omitempty"`
	ConversationMessageCount int                          `json:"conversation_message_count,omitempty"`
	CreatedAt                time.Time                    `json:"created_at"`
	Metadata                 map[string]interface{}       `json:"metadata,omitempty"`
	Provenance               checkpoint.ProvenanceSummary `json:"provenance,omitempty"`
}

type checkpointReadService interface {
	ListCheckpoints(ctx context.Context, limit, offset int) ([]artifact.Checkpoint, error)
	PreviewCheckpoint(ctx context.Context, checkpointID, mode string) (*checkpoint.RestoreResult, error)
	GetCheckpointFiles(ctx context.Context, checkpointID string) ([]artifact.CheckpointFile, error)
}

type storedCheckpointReadService struct {
	sessionID string
	store     *artifact.Store
}

func (s *storedCheckpointReadService) ListCheckpoints(ctx context.Context, limit, offset int) ([]artifact.Checkpoint, error) {
	return s.store.ListCheckpoints(ctx, s.sessionID, limit, offset)
}

func (s *storedCheckpointReadService) PreviewCheckpoint(ctx context.Context, checkpointID, mode string) (*checkpoint.RestoreResult, error) {
	if strings.TrimSpace(mode) == "" {
		mode = string(checkpoint.RestoreCode)
	}
	return checkpoint.NewManager(s.store, nil).Restore(ctx, checkpoint.RestoreRequest{
		SessionID:    s.sessionID,
		CheckpointID: checkpointID,
		Mode:         checkpoint.RestoreMode(mode),
		PreviewOnly:  true,
	})
}

func (s *storedCheckpointReadService) GetCheckpointFiles(ctx context.Context, checkpointID string) ([]artifact.CheckpointFile, error) {
	checkpointID = strings.TrimSpace(checkpointID)
	checkpointRecord, err := s.store.GetCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if checkpointRecord == nil {
		return nil, fmt.Errorf("checkpoint not found: %s", checkpointID)
	}
	if owner := strings.TrimSpace(checkpointRecord.SessionID); owner != "" && owner != s.sessionID {
		return nil, fmt.Errorf("checkpoint does not belong to session")
	}
	return s.store.GetCheckpointFiles(ctx, checkpointID)
}

func (h *Handler) openCheckpointReadService(sessionID string) (checkpointReadService, func(), error) {
	if h != nil {
		h.sessionRuntimeMu.RLock()
		hub := h.sessionHub
		h.sessionRuntimeMu.RUnlock()
		if hub != nil {
			if actor, ok := hub.Get(sessionID); ok {
				return actor, func() {}, nil
			}
		}
	}

	config := h.resolveRuntimeConfig(UsageScope{})
	paths := sessionruntime.ResolvePaths(sessionruntime.ResolveOptions{
		Config:     config,
		ConfigFile: h.runtimeConfigFile,
		Mode:       sessionruntime.ModeServer,
	})
	storeConfig := &artifact.StoreConfig{Path: paths.ArtifactStorePath}
	if config != nil {
		storeConfig.DSN = strings.TrimSpace(config.Artifact.StoreDSN)
	}
	store, err := artifact.NewStore(storeConfig)
	if err != nil {
		return nil, nil, err
	}
	service := &storedCheckpointReadService{
		sessionID: strings.TrimSpace(sessionID),
		store:     store,
	}
	return service, func() { _ = store.Close() }, nil
}

// ListSessionCheckpoints lists checkpoints for a session.
func (h *Handler) ListSessionCheckpoints(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	offset, err := parseOptionalOffset(r.URL.Query().Get("offset"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	reader, cleanup, err := h.openCheckpointReadService(sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer cleanup()
	checkpoints, err := reader.ListCheckpoints(r.Context(), limit, offset)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	summaries := make([]checkpointSummary, 0, len(checkpoints))
	for _, chk := range checkpoints {
		summaries = append(summaries, checkpointSummary{
			ID:                       chk.ID,
			SessionID:                chk.SessionID,
			TaskID:                   chk.TaskID,
			Reason:                   chk.Reason,
			HistoryHash:              chk.HistoryHash,
			MessageCount:             chk.MessageCount,
			ConversationExact:        checkpointHasConversationSnapshot(chk.Metadata),
			ConversationMessageCount: checkpointConversationMessageCount(chk.Metadata, chk.MessageCount),
			CreatedAt:                chk.CreatedAt,
			Metadata:                 chk.Metadata,
			Provenance:               checkpointSummaryProvenance(chk),
		})
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"checkpoints": summaries,
		"count":       len(summaries),
	})
}

func checkpointHasConversationSnapshot(metadata map[string]interface{}) bool {
	if len(metadata) == 0 {
		return false
	}
	raw, ok := metadata["conversation_blob_id"]
	if !ok {
		return false
	}
	text, _ := raw.(string)
	return strings.TrimSpace(text) != ""
}

func checkpointConversationMessageCount(metadata map[string]interface{}, fallback int) int {
	if fallback > 0 {
		if count := checkpointMetadataConversationMessageCount(metadata); count > 0 {
			return count
		}
		return fallback
	}
	return checkpointMetadataConversationMessageCount(metadata)
}

func checkpointMetadataConversationMessageCount(metadata map[string]interface{}) int {
	if len(metadata) == 0 {
		return 0
	}
	raw, ok := metadata["conversation_message_count"]
	if !ok {
		return 0
	}
	switch value := raw.(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

// PreviewSessionCheckpoint previews a checkpoint restore.
func (h *Handler) PreviewSessionCheckpoint(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := strings.TrimSpace(vars["id"])
	checkpointID := strings.TrimSpace(vars["checkpoint_id"])
	if sessionID == "" || checkpointID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id and checkpoint id are required"))
		return
	}

	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	if mode == "" {
		var body struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			mode = strings.TrimSpace(body.Mode)
		}
	}

	reader, cleanup, err := h.openCheckpointReadService(sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer cleanup()

	result, err := reader.PreviewCheckpoint(r.Context(), checkpointID, mode)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "checkpoint not found"))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
	})
}

func checkpointSummaryProvenance(chk artifact.Checkpoint) checkpoint.ProvenanceSummary {
	return checkpointSummaryProvenancePtr(&chk)
}

func checkpointSummaryProvenancePtr(chk *artifact.Checkpoint) checkpoint.ProvenanceSummary {
	if chk == nil {
		return checkpoint.ProvenanceSummary{}
	}
	return checkpoint.SummarizeCheckpointProvenance(chk)
}

// RestoreSessionCheckpoint restores a checkpoint.
func (h *Handler) RestoreSessionCheckpoint(w http.ResponseWriter, r *http.Request) {
	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
		return
	}

	vars := mux.Vars(r)
	sessionID := strings.TrimSpace(vars["id"])
	checkpointID := strings.TrimSpace(vars["checkpoint_id"])
	if sessionID == "" || checkpointID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id and checkpoint id are required"))
		return
	}

	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	if mode == "" {
		var body struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			mode = strings.TrimSpace(body.Mode)
		}
	}
	if mode == "" {
		mode = string(checkpoint.RestoreCode)
	}

	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		if h.writeSessionLeaseConflict(w, err) {
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	result, err := actor.Rewind(r.Context(), checkpointID, mode)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "checkpoint not found"))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"result": result,
	})
}

// GetCheckpointFiles returns files for a checkpoint.
func (h *Handler) GetCheckpointFiles(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := strings.TrimSpace(vars["id"])
	checkpointID := strings.TrimSpace(vars["checkpoint_id"])
	if sessionID == "" || checkpointID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id and checkpoint id are required"))
		return
	}

	reader, cleanup, err := h.openCheckpointReadService(sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer cleanup()
	files, err := reader.GetCheckpointFiles(r.Context(), checkpointID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"files": files,
		"count": len(files),
	})
}
