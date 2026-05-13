package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

const (
	MetadataKey         = "aicli.goal"
	MaxObjectiveRunes   = 4000
	defaultGoalIDPrefix = "goal_"
)

type Status string

const (
	StatusActive        Status = "active"
	StatusPaused        Status = "paused"
	StatusBudgetLimited Status = "budget_limited"
	StatusComplete      Status = "complete"
)

type SessionGoal struct {
	GoalID            string     `json:"goal_id" yaml:"goal_id"`
	SessionID         string     `json:"session_id" yaml:"session_id"`
	Objective         string     `json:"objective" yaml:"objective"`
	Status            Status     `json:"status" yaml:"status"`
	TokenBudget       int        `json:"token_budget,omitempty" yaml:"token_budget,omitempty"`
	TokensUsed        int        `json:"tokens_used,omitempty" yaml:"tokens_used,omitempty"`
	TimeUsedSeconds   int64      `json:"time_used_seconds,omitempty" yaml:"time_used_seconds,omitempty"`
	CompletedBy       string     `json:"completed_by,omitempty" yaml:"completed_by,omitempty"`
	CompletionSummary string     `json:"completion_summary,omitempty" yaml:"completion_summary,omitempty"`
	CreatedAt         time.Time  `json:"created_at" yaml:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at" yaml:"updated_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
}

type MetadataStore struct{}

type MutationActor string

const (
	MutationUser   MutationActor = "user"
	MutationModel  MutationActor = "model"
	MutationSystem MutationActor = "system"
)

type GoalMergePolicy struct {
	Actor MutationActor
}

func NewMetadataStore() MetadataStore {
	return MetadataStore{}
}

func NewSessionGoal(sessionID, objective string, now time.Time) (SessionGoal, error) {
	objective, err := NormalizeObjective(objective)
	if err != nil {
		return SessionGoal{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	return SessionGoal{
		GoalID:    defaultGoalIDPrefix + uuid.NewString(),
		SessionID: strings.TrimSpace(sessionID),
		Objective: objective,
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func NormalizeObjective(objective string) (string, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return "", fmt.Errorf("goal objective cannot be empty")
	}
	if len([]rune(objective)) > MaxObjectiveRunes {
		return "", fmt.Errorf("goal objective exceeds %d characters", MaxObjectiveRunes)
	}
	return objective, nil
}

func ValidateStatus(status Status) error {
	switch status {
	case StatusActive, StatusPaused, StatusBudgetLimited, StatusComplete:
		return nil
	default:
		return fmt.Errorf("invalid goal status %q", status)
	}
}

func (g SessionGoal) Validate() error {
	if strings.TrimSpace(g.Objective) == "" {
		return fmt.Errorf("goal objective cannot be empty")
	}
	if len([]rune(strings.TrimSpace(g.Objective))) > MaxObjectiveRunes {
		return fmt.Errorf("goal objective exceeds %d characters", MaxObjectiveRunes)
	}
	return ValidateStatus(g.Status)
}

func (s MetadataStore) Get(session *runtimechat.Session) (*SessionGoal, bool, error) {
	if session == nil || session.Metadata.Context == nil {
		return nil, false, nil
	}
	raw, ok := session.Metadata.Context[MetadataKey]
	if !ok || raw == nil {
		return nil, false, nil
	}
	goal, err := decode(raw)
	if err != nil {
		return nil, true, err
	}
	if err := goal.Validate(); err != nil {
		return nil, true, err
	}
	return &goal, true, nil
}

func (s MetadataStore) Put(session *runtimechat.Session, goal SessionGoal) error {
	if session == nil {
		return runtimechat.ErrInvalidSession
	}
	normalized, err := normalizeForSession(session.ID, goal)
	if err != nil {
		return err
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = make(map[string]interface{})
	}
	session.Metadata.Context[MetadataKey] = normalized
	return nil
}

func (s MetadataStore) Clear(session *runtimechat.Session) error {
	if session == nil {
		return runtimechat.ErrInvalidSession
	}
	if session.Metadata.Context != nil {
		delete(session.Metadata.Context, MetadataKey)
	}
	return nil
}

func (s MetadataStore) PutPersistent(ctx context.Context, storage runtimechat.SessionStorage, sessionID string, goal SessionGoal, actor MutationActor) (*runtimechat.Session, error) {
	normalized, err := normalizeForSession(sessionID, goal)
	if err != nil {
		return nil, err
	}
	return sessionmeta.ApplyContextPatch(ctx, storage, sessionmeta.ContextPatch{
		SessionID: strings.TrimSpace(sessionID),
		SetContext: map[string]interface{}{
			MetadataKey: normalized,
		},
		MergePolicies: map[string]sessionmeta.MergePolicy{
			MetadataKey: GoalMergePolicy{Actor: actor},
		},
	})
}

func (s MetadataStore) ClearPersistent(ctx context.Context, storage runtimechat.SessionStorage, sessionID string) (*runtimechat.Session, error) {
	return sessionmeta.ApplyContextPatch(ctx, storage, sessionmeta.ContextPatch{
		SessionID:     strings.TrimSpace(sessionID),
		DeleteContext: []string{MetadataKey},
	})
}

func (p GoalMergePolicy) MergeContextValue(key string, oldValue, newValue interface{}) (interface{}, error) {
	if strings.TrimSpace(key) != MetadataKey || oldValue == nil {
		return newValue, nil
	}
	oldGoal, err := decode(oldValue)
	if err != nil {
		return nil, err
	}
	newGoal, err := decode(newValue)
	if err != nil {
		return nil, err
	}
	sameGoal := strings.TrimSpace(oldGoal.GoalID) != "" && strings.TrimSpace(oldGoal.GoalID) == strings.TrimSpace(newGoal.GoalID)
	if !sameGoal {
		if p.Actor == MutationModel {
			return oldGoal, nil
		}
		return newGoal, nil
	}
	if oldGoal.Status == StatusComplete && newGoal.Status != StatusComplete {
		return oldGoal, nil
	}
	if oldGoal.Status == StatusBudgetLimited && newGoal.Status == StatusActive {
		return oldGoal, nil
	}
	if oldGoal.Status == StatusPaused && newGoal.Status == StatusComplete && p.Actor == MutationModel {
		return nil, fmt.Errorf("model cannot complete a paused goal")
	}
	return newGoal, nil
}

func normalizeForSession(sessionID string, goal SessionGoal) (SessionGoal, error) {
	goal.Objective = strings.TrimSpace(goal.Objective)
	if goal.SessionID == "" {
		goal.SessionID = strings.TrimSpace(sessionID)
	}
	if goal.GoalID == "" {
		goal.GoalID = defaultGoalIDPrefix + uuid.NewString()
	}
	if goal.CreatedAt.IsZero() {
		goal.CreatedAt = time.Now()
	}
	if goal.UpdatedAt.IsZero() {
		goal.UpdatedAt = time.Now()
	}
	if err := goal.Validate(); err != nil {
		return SessionGoal{}, err
	}
	return goal, nil
}

func decode(raw interface{}) (SessionGoal, error) {
	switch typed := raw.(type) {
	case SessionGoal:
		return typed, nil
	case *SessionGoal:
		if typed == nil {
			return SessionGoal{}, fmt.Errorf("goal metadata is nil")
		}
		return *typed, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return SessionGoal{}, fmt.Errorf("encode goal metadata: %w", err)
		}
		var goal SessionGoal
		if err := json.Unmarshal(data, &goal); err != nil {
			return SessionGoal{}, fmt.Errorf("decode goal metadata: %w", err)
		}
		return goal, nil
	}
}
