package toolbroker

import (
	"context"
	"strings"
)

func (b *Broker) loadSessionHandleAliases(ctx context.Context, sessionID string) (*handleAliasRegistry, error) {
	if b == nil || b.SessionContextStore == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	value, err := b.SessionContextStore.LoadContextValue(ctx, sessionID, sessionHandleAliasesContextKey)
	if err != nil {
		return nil, err
	}
	return loadHandleAliasRegistry(value), nil
}

func (b *Broker) saveSessionHandleAliases(ctx context.Context, sessionID string, aliases *handleAliasRegistry) error {
	if b == nil || b.SessionContextStore == nil || aliases == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	return b.SessionContextStore.SaveContextValue(ctx, sessionID, sessionHandleAliasesContextKey, aliases.contextValue())
}
