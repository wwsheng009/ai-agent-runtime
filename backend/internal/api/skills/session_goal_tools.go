package skills

import (
	"context"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclitools"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

const runtimeServerGoalMCPName = "aicli_goal"

type runtimeServerGoalToolSessionContext struct {
	handler   *Handler
	sessionID string
	mu        sync.Mutex
	session   *chat.Session
}

func (h *Handler) runtimeServerToolSurfaceForSession(ctx context.Context, sessionID string, next skill.MCPManager, enabled bool) skill.MCPManager {
	sessionID = strings.TrimSpace(sessionID)
	if !enabled || !h.runtimeServerGoalToolsAvailable(ctx, sessionID) {
		return next
	}
	return &aiclitools.CapabilityMCPManager{
		Registry: runtimegoal.CapabilityRegistry(),
		Next:     next,
		Path:     aiclitools.ExposureRuntimeServer,
		MCPName:  runtimeServerGoalMCPName,
		ContextFactory: func(ctx context.Context) aiclitools.ToolSessionContext {
			return &runtimeServerGoalToolSessionContext{
				handler:   h,
				sessionID: sessionID,
			}
		},
	}
}

func (h *Handler) runtimeServerGoalToolsAvailable(ctx context.Context, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || h.sessionManager == nil || h.sessionManager.GetStorage() == nil || sessionID == "" {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := h.sessionManager.Get(ctx, sessionID)
	return err == nil && session != nil
}

func (c *runtimeServerGoalToolSessionContext) SessionID() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.sessionID)
}

func (c *runtimeServerGoalToolSessionContext) RuntimeSession() *chat.Session {
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		return c.session.Clone()
	}
	session, err := c.handler.sessionManager.Get(context.Background(), c.SessionID())
	if err != nil || session == nil {
		return nil
	}
	c.session = session.Clone()
	return c.session.Clone()
}

func (c *runtimeServerGoalToolSessionContext) SessionStorage() chat.SessionStorage {
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil
	}
	return c.handler.sessionManager.GetStorage()
}

func (c *runtimeServerGoalToolSessionContext) RefreshRuntimeSession(ctx context.Context, updated *chat.Session) error {
	if c == nil || updated == nil {
		return nil
	}
	c.mu.Lock()
	c.session = updated.Clone()
	c.mu.Unlock()
	return nil
}

func (c *runtimeServerGoalToolSessionContext) ExecutorPath() aiclitools.ExposurePath {
	return aiclitools.ExposureRuntimeServer
}

var _ aiclitools.ToolSessionContext = (*runtimeServerGoalToolSessionContext)(nil)

func (h *Handler) runtimeServerGoalPersistHook(ctx context.Context, session *chat.Session) (*chat.Session, error) {
	if session == nil || h == nil || h.sessionManager == nil {
		return session, nil
	}
	sessionID := strings.TrimSpace(session.ID)
	if sessionID == "" {
		return session, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	latest, err := h.sessionManager.Get(ctx, sessionID)
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
