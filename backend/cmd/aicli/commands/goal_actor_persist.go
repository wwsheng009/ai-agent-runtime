package commands

import (
	"context"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

func localGoalPersistHook(storage runtimechat.SessionStorage) func(context.Context, *runtimechat.Session) (*runtimechat.Session, error) {
	if storage == nil {
		return nil
	}
	return func(ctx context.Context, session *runtimechat.Session) (*runtimechat.Session, error) {
		return preserveLatestGoalMetadata(ctx, storage, session)
	}
}

func preserveLatestGoalMetadata(ctx context.Context, storage runtimechat.SessionStorage, session *runtimechat.Session) (*runtimechat.Session, error) {
	if storage == nil || session == nil || strings.TrimSpace(session.ID) == "" {
		return session, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	latest, err := storage.Load(ctx, strings.TrimSpace(session.ID))
	if err != nil || latest == nil {
		return session, nil
	}
	prepared := session.Clone()
	store := runtimegoal.NewMetadataStore()
	latestGoal, latestOK, err := store.Get(latest)
	if err != nil {
		return nil, err
	}
	if latestOK && latestGoal != nil {
		if err := store.Put(prepared, *latestGoal); err != nil {
			return nil, err
		}
		return prepared, nil
	}
	if err := store.Clear(prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}
