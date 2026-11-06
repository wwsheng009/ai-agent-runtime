package commands

import (
	"context"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// loadSessionMetadataBestEffort reads only the sessions row when the storage
// implementation exposes SessionStorageMetadataReader.
//
// Metadata consumers must not call Load by default: Load also deserializes the
// prompt projection (and may scan canonical history for legacy self-heal). The
// fallback preserves behavior for older/custom stores that only implement the
// base SessionStorage interface.
func loadSessionMetadataBestEffort(ctx context.Context, store runtimechat.SessionStorage, sessionID string) (*runtimechat.Session, error) {
	sessionID = strings.TrimSpace(sessionID)
	if store == nil || sessionID == "" {
		return nil, runtimechat.ErrInvalidSession
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if reader, ok := store.(runtimechat.SessionStorageMetadataReader); ok {
		return reader.LoadMetadata(ctx, sessionID)
	}
	return store.Load(ctx, sessionID)
}
