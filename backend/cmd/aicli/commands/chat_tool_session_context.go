package commands

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclitools"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

type chatToolSessionContext struct {
	session *ChatSession
	path    aiclitools.ExposurePath
}

func newChatToolSessionContext(session *ChatSession, path aiclitools.ExposurePath) aiclitools.ToolSessionContext {
	return &chatToolSessionContext{session: session, path: path}
}

func (c *chatToolSessionContext) SessionID() string {
	if c == nil || c.session == nil || c.session.RuntimeSession == nil {
		return ""
	}
	return c.session.RuntimeSession.ID
}

func (c *chatToolSessionContext) RuntimeSession() *runtimechat.Session {
	if c == nil || c.session == nil {
		return nil
	}
	return c.session.RuntimeSession
}

func (c *chatToolSessionContext) SessionStorage() runtimechat.SessionStorage {
	if c == nil || c.session == nil || c.session.SessionManager == nil {
		return nil
	}
	return c.session.SessionManager.GetStorage()
}

func (c *chatToolSessionContext) RefreshRuntimeSession(ctx context.Context, updated *runtimechat.Session) error {
	if c == nil || c.session == nil || updated == nil {
		return nil
	}
	if err := restoreChatStateFromRuntimeSession(c.session, updated); err != nil {
		return err
	}
	ensureChatSystemPromptMessage(c.session)
	return syncRuntimeSessionFromChat(c.session)
}

func (c *chatToolSessionContext) ExecutorPath() aiclitools.ExposurePath {
	if c == nil || c.path == "" {
		return aiclitools.ExposureShared
	}
	return c.path
}

var _ aiclitools.ToolSessionContext = (*chatToolSessionContext)(nil)
