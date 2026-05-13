package sessionmeta

import (
	"context"
	"fmt"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

type MergePolicy interface {
	MergeContextValue(key string, oldValue, newValue interface{}) (interface{}, error)
}

type ContextPatch struct {
	SessionID         string
	SetContext        map[string]interface{}
	DeleteContext     []string
	ExpectedUpdatedAt string
	MergePolicies     map[string]MergePolicy
}

func ApplyContextPatch(ctx context.Context, storage runtimechat.SessionStorage, patch ContextPatch) (*runtimechat.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if storage == nil {
		return nil, fmt.Errorf("session storage is required")
	}
	sessionID := strings.TrimSpace(patch.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	session, err := storage.Load(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, runtimechat.ErrSessionNotFound
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = make(map[string]interface{})
	}
	for _, key := range patch.DeleteContext {
		Delete(session.Metadata.Context, key)
	}
	for key, value := range patch.SetContext {
		key = CanonicalKey(key)
		if key == "" {
			continue
		}
		if policy := mergePolicyForKey(patch.MergePolicies, key); policy != nil {
			oldValue, _ := Value(session.Metadata.Context, key)
			merged, mergeErr := policy.MergeContextValue(key, oldValue, value)
			if mergeErr != nil {
				return nil, mergeErr
			}
			value = merged
		}
		Set(session.Metadata.Context, key, value)
	}
	if err := storage.Save(ctx, session); err != nil {
		return nil, err
	}
	return session.Clone(), nil
}

func mergePolicyForKey(policies map[string]MergePolicy, key string) MergePolicy {
	if len(policies) == 0 {
		return nil
	}
	if policy := policies[key]; policy != nil {
		return policy
	}
	return policies[CanonicalKey(key)]
}
