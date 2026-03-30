package skills

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/gorilla/mux"
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

// ListSessionCheckpoints lists checkpoints for a session.
func (h *Handler) ListSessionCheckpoints(w http.ResponseWriter, r *http.Request) {
	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
		return
	}

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

	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	checkpoints, err := actor.ListCheckpoints(r.Context(), limit, offset)
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

	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	result, err := actor.PreviewCheckpoint(r.Context(), checkpointID, mode)
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

	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	files, err := actor.GetCheckpointFiles(r.Context(), checkpointID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"files": files,
		"count": len(files),
	})
}

