package skills

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	apperrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

const (
	defaultSessionUsersLimit = 100
	maxSessionUsersLimit     = 500
	sessionUsersScanLimit    = 1000000
)

type sessionUserSummary struct {
	UserID           string     `json:"user_id"`
	DisplayName      string     `json:"display_name"`
	Source           string     `json:"source"`
	SessionCount     int        `json:"session_count"`
	ActiveCount      int        `json:"active_count"`
	IdleCount        int        `json:"idle_count"`
	ClosedCount      int        `json:"closed_count"`
	ArchivedCount    int        `json:"archived_count"`
	RecoverableCount int        `json:"recoverable_count"`
	LatestUpdatedAt  *time.Time `json:"latest_updated_at,omitempty"`
}

// ListSessionUsers returns user ids discovered from persisted sessions.
func (h *Handler) ListSessionUsers(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, apperrors.New(apperrors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	limit, err := parseSessionUsersLimit(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	defaultUserID := h.resolveServerSessionUserID("")
	users, totalCount, err := h.listSessionUsers(r.Context(), defaultUserID, limit)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"users":           users,
		"count":           len(users),
		"total_count":     totalCount,
		"default_user_id": defaultUserID,
		"limit":           limit,
	})
}

func (h *Handler) listSessionUsers(ctx context.Context, defaultUserID string, limit int) ([]sessionUserSummary, int, error) {
	storage := h.sessionManager.GetStorage()
	allLister, ok := storage.(chat.SessionStorageAllLister)
	source := "session_store"
	var sessions []*chat.Session
	var err error
	if ok {
		sessions, err = allLister.ListAll(ctx, sessionUsersScanLimit, 0)
	} else {
		source = "default_user_sessions"
		sessions, err = h.sessionManager.List(ctx, defaultUserID)
	}
	if err != nil {
		return nil, 0, err
	}

	users := buildSessionUserSummaries(sessions, defaultUserID, source)
	totalCount := len(users)
	if limit > 0 && limit < len(users) {
		users = users[:limit]
	}
	return users, totalCount, nil
}

func buildSessionUserSummaries(sessions []*chat.Session, defaultUserID, source string) []sessionUserSummary {
	defaultUserID = strings.TrimSpace(defaultUserID)
	if defaultUserID == "" {
		defaultUserID = "anonymous"
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "session_store"
	}

	byUser := make(map[string]*sessionUserSummary)
	for _, session := range sessions {
		if session == nil {
			continue
		}
		userID := strings.TrimSpace(session.UserID)
		if userID == "" {
			userID = defaultUserID
		}
		summary := byUser[userID]
		if summary == nil {
			summary = &sessionUserSummary{
				UserID:      userID,
				DisplayName: userID,
				Source:      source,
			}
			byUser[userID] = summary
		}
		summary.SessionCount++
		switch session.State {
		case chat.StateActive:
			summary.ActiveCount++
			summary.RecoverableCount++
		case chat.StateIdle:
			summary.IdleCount++
			summary.RecoverableCount++
		case chat.StateClosed:
			summary.ClosedCount++
		case chat.StateArchived:
			summary.ArchivedCount++
		}
		if updatedAt, ok := sessionUserLatestUpdatedAt(session); ok {
			if summary.LatestUpdatedAt == nil || updatedAt.After(*summary.LatestUpdatedAt) {
				latest := updatedAt
				summary.LatestUpdatedAt = &latest
			}
		}
	}

	users := make([]sessionUserSummary, 0, len(byUser))
	for _, summary := range byUser {
		users = append(users, *summary)
	}
	sort.Slice(users, func(i, j int) bool {
		left := users[i]
		right := users[j]
		if left.LatestUpdatedAt != nil && right.LatestUpdatedAt != nil && !left.LatestUpdatedAt.Equal(*right.LatestUpdatedAt) {
			return left.LatestUpdatedAt.After(*right.LatestUpdatedAt)
		}
		if left.LatestUpdatedAt != nil && right.LatestUpdatedAt == nil {
			return true
		}
		if left.LatestUpdatedAt == nil && right.LatestUpdatedAt != nil {
			return false
		}
		if left.SessionCount != right.SessionCount {
			return left.SessionCount > right.SessionCount
		}
		return left.UserID < right.UserID
	})
	return users
}

func sessionUserLatestUpdatedAt(session *chat.Session) (time.Time, bool) {
	if session == nil {
		return time.Time{}, false
	}
	if !session.UpdatedAt.IsZero() {
		return session.UpdatedAt, true
	}
	if !session.CreatedAt.IsZero() {
		return session.CreatedAt, true
	}
	return time.Time{}, false
}

func parseSessionUsersLimit(r *http.Request) (int, error) {
	limit := defaultSessionUsersLimit
	if r == nil || r.URL == nil {
		return limit, nil
	}
	rawLimit := strings.TrimSpace(r.URL.Query().Get("limit"))
	if rawLimit == "" {
		return limit, nil
	}
	parsed, err := strconv.Atoi(rawLimit)
	if err != nil || parsed <= 0 {
		return 0, apperrors.New(apperrors.ErrValidationFailed, "invalid limit value")
	}
	if parsed > maxSessionUsersLimit {
		return maxSessionUsersLimit, nil
	}
	return parsed, nil
}
