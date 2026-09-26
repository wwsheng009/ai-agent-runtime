package runtimeapi

import (
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

// 行级评论（§4.4）的 HTTP 面：存储契约见 internal/planstore/comments.go，
// 锚点重放见 internal/planmode/comments.go。读侧总是附带「按目标修订重放后的
// 当前位置」，因此 CLI / Web 不需要各自实现一份重放。

// planCommentResponse is one stored comment plus its replay result against the
// requested revision: revision/start_line/end_line are the *stored* anchor,
// current_* the location after replay (identical for orphaned comments).
type planCommentResponse struct {
	ID        string `json:"id"`
	Revision  int    `json:"revision"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Excerpt   string `json:"excerpt,omitempty"`
	Body      string `json:"body"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`

	Status           string `json:"status"`
	CurrentRevision  int    `json:"current_revision"`
	CurrentStartLine int    `json:"current_start_line"`
	CurrentEndLine   int    `json:"current_end_line"`
}

type planCommentListResponse struct {
	PlanID         string                `json:"plan_id"`
	Revision       int                   `json:"revision"`
	LatestRevision int                   `json:"latest_revision"`
	Comments       []planCommentResponse `json:"comments"`
	Count          int                   `json:"count"`
}

type planCommentCreateRequest struct {
	// Revision anchors the comment; 0 means the latest archived round.
	Revision  int    `json:"revision"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Body      string `json:"body"`
	Author    string `json:"author"`
}

// ListStoredPlanComments returns every comment of one archived plan, replayed
// against ?revision=N (0/absent = latest archived round).
func (h *Handler) ListStoredPlanComments(w http.ResponseWriter, r *http.Request) {
	store, record, ok := h.storedPlanForComments(w, r)
	if !ok {
		return
	}
	revision, ok := h.commentRevisionTarget(w, r, record)
	if !ok {
		return
	}
	content, ok := h.readCommentRevisionContent(w, store, record, revision)
	if !ok {
		return
	}
	comments, err := store.Comments(record.ID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid, err.Error()))
		return
	}
	resolved := planmode.ResolvePlanComments(comments, content)
	h.writeJSON(w, http.StatusOK, planCommentListResponse{
		PlanID:         record.ID,
		Revision:       revision,
		LatestRevision: record.Version,
		Comments:       planCommentResponsesFromResolved(resolved, revision),
		Count:          len(resolved),
	})
}

// CreateStoredPlanComment anchors one comment to a line range of an archived
// round. The excerpt is captured from that round's body, so a later revision
// can replay the anchor instead of pointing at whatever moved into those lines.
func (h *Handler) CreateStoredPlanComment(w http.ResponseWriter, r *http.Request) {
	store, record, ok := h.storedPlanForComments(w, r)
	if !ok {
		return
	}
	req, err := decodePlanCommentCreateRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	revision := req.Revision
	if revision == 0 {
		revision = record.Version
	}
	if revision < 1 || revision > record.Version {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed,
			"plan revision not archived: "+strconv.Itoa(revision)))
		return
	}
	content, err := store.ReadVersion(record.ID, revision)
	if err != nil {
		h.writeStoredPlanCommentError(w, err)
		return
	}

	comment, err := planmode.NewLineComment(planmode.LineCommentOptions{
		RecordID:  record.ID,
		Revision:  revision,
		StartLine: req.StartLine,
		EndLine:   req.EndLine,
		Content:   content,
		Body:      req.Body,
		Author:    req.Author,
	})
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	stored, err := store.AppendComment(record.ID, comment)
	if err != nil {
		if stderrors.Is(err, planstore.ErrInvalidComment) {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
			return
		}
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid, err.Error()))
		return
	}

	// 刚创建的评论必然锚定在原位，但仍走同一条重放路径，保证响应形状与读侧一致。
	resolved := planmode.ResolvePlanComments([]planstore.ReviewComment{stored}, content)
	h.writeJSON(w, http.StatusCreated, planCommentListResponse{
		PlanID:         record.ID,
		Revision:       revision,
		LatestRevision: record.Version,
		Comments:       planCommentResponsesFromResolved(resolved, revision),
		Count:          len(resolved),
	})
}

// DeleteStoredPlanComment removes one comment. Like DELETE /plans/{id} it is
// idempotent: an unknown record or comment returns 200 with deleted=false.
func (h *Handler) DeleteStoredPlanComment(w http.ResponseWriter, r *http.Request) {
	store := h.planArtifactStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "plan store not configured"))
		return
	}
	id := ""
	commentID := ""
	if r != nil {
		id = strings.Trim(strings.TrimSpace(mux.Vars(r)["id"]), "/")
		commentID = strings.TrimSpace(mux.Vars(r)["comment_id"])
	}
	if id == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "plan id is required"))
		return
	}
	if commentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "comment id is required"))
		return
	}
	deleted, err := store.DeleteComment(id, commentID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"plan_id":    id,
		"comment_id": commentID,
		"deleted":    deleted,
	})
}

// storedPlanForComments resolves the routing id and the archived record shared
// by all three comment handlers.
func (h *Handler) storedPlanForComments(w http.ResponseWriter, r *http.Request) (*planstore.Store, planstore.Record, bool) {
	store := h.planArtifactStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "plan store not configured"))
		return nil, planstore.Record{}, false
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		id = strings.Trim(strings.TrimSpace(mux.Vars(r)["id"]), "/")
	}
	if id == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "plan id is required"))
		return nil, planstore.Record{}, false
	}
	record, ok, err := store.Get(id)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid, err.Error()))
		return nil, planstore.Record{}, false
	}
	if !ok {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "plan not found: "+id))
		return nil, planstore.Record{}, false
	}
	return store, record, true
}

// commentRevisionTarget decodes ?revision=N; 0/absent means the latest archived
// round, and a round that was never archived is a 404 (matching /diff).
func (h *Handler) commentRevisionTarget(w http.ResponseWriter, r *http.Request, record planstore.Record) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("revision"))
	if raw == "" {
		return record.Version, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"revision must be a non-negative integer: "+raw))
		return 0, false
	}
	if value == 0 {
		return record.Version, true
	}
	if value > record.Version {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed,
			"plan revision not archived: "+strconv.Itoa(value)))
		return 0, false
	}
	return value, true
}

// readCommentRevisionContent loads the replay target; a record that never had a
// round archived answers with empty content instead of an error, so listing an
// untouched record stays a 200 with an empty comment list.
func (h *Handler) readCommentRevisionContent(w http.ResponseWriter, store *planstore.Store, record planstore.Record, revision int) ([]byte, bool) {
	if revision < 1 {
		return nil, true
	}
	content, err := store.ReadVersion(record.ID, revision)
	if err != nil {
		h.writeStoredPlanCommentError(w, err)
		return nil, false
	}
	return content, true
}

// writeStoredPlanCommentError maps store errors the way the detail and diff
// endpoints do: a missing record or round is a client-visible 404, everything
// else is a 500 (a corrupt or unreadable archive is not the caller's fault).
func (h *Handler) writeStoredPlanCommentError(w http.ResponseWriter, err error) {
	if stderrors.Is(err, planstore.ErrNotFound) {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid, err.Error()))
}

func planCommentResponsesFromResolved(resolved []planmode.ResolvedComment, revision int) []planCommentResponse {
	out := make([]planCommentResponse, 0, len(resolved))
	for _, comment := range resolved {
		out = append(out, planCommentResponse{
			ID:               comment.Comment.ID,
			Revision:         comment.Comment.Revision,
			StartLine:        comment.Comment.StartLine,
			EndLine:          comment.Comment.EndLine,
			Excerpt:          comment.Comment.Excerpt,
			Body:             comment.Comment.Body,
			Author:           comment.Comment.Author,
			CreatedAt:        comment.Comment.CreatedAt,
			Status:           string(comment.Status),
			CurrentRevision:  revision,
			CurrentStartLine: comment.StartLine,
			CurrentEndLine:   comment.EndLine,
		})
	}
	return out
}

func decodePlanCommentCreateRequest(r *http.Request) (planCommentCreateRequest, error) {
	req := planCommentCreateRequest{}
	if r == nil || r.Body == nil {
		return req, nil
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	if err := decoder.Decode(&req); err != nil {
		if stderrors.Is(err, io.EOF) {
			return planCommentCreateRequest{}, nil
		}
		return planCommentCreateRequest{}, err
	}
	req.Body = strings.TrimSpace(req.Body)
	req.Author = strings.TrimSpace(req.Author)
	return req, nil
}
