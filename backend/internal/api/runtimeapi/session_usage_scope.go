package runtimeapi

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// usageScopeFromSessionContext maps the durable session identity to the same
// scope type used by request-scoped profile/runtime resolution.
func usageScopeFromSessionContext(sessionCtx runtimehost.SessionContext) UsageScope {
	return UsageScope{
		TenantID:  strings.TrimSpace(sessionCtx.TenantID),
		ProjectID: strings.TrimSpace(sessionCtx.ProjectID),
		UserID:    strings.TrimSpace(sessionCtx.UserID),
	}
}

func (h *Handler) usageScopeForSession(session *chat.Session) UsageScope {
	if session == nil {
		return UsageScope{}
	}
	return usageScopeFromSessionContext(runtimehost.ExtractSessionContext(session))
}

// stampSessionUsageScope persists the request's tenant/project identity on the
// session before a durable actor can be built. The actor is a different request
// path; without this anchor its profile/runtimeConfig resolution would fall back
// to UsageScope{} even though the direct request resolved a scoped config.
//
// The stamp is written only when the durable values actually change, and a
// write failure is logged as a degradation rather than failing the turn.
func (h *Handler) stampSessionUsageScope(ctx context.Context, session *chat.Session, scope UsageScope) {
	if h == nil || h.sessionManager == nil || session == nil {
		return
	}
	tenantID := strings.TrimSpace(scope.TenantID)
	projectID := strings.TrimSpace(scope.ProjectID)
	if tenantID == "" && projectID == "" {
		return
	}
	currentTenant := sessionmeta.String(session.Metadata.Context, sessionmeta.UsageTenantID)
	currentProject := sessionmeta.String(session.Metadata.Context, sessionmeta.UsageProjectID)
	if currentTenant == tenantID && currentProject == projectID {
		return
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = make(map[string]interface{})
	}
	sessionmeta.Set(session.Metadata.Context, sessionmeta.UsageTenantID, tenantID)
	sessionmeta.Set(session.Metadata.Context, sessionmeta.UsageProjectID, projectID)
	if err := h.sessionManager.Update(ctx, session); err != nil {
		logger.Warnf("agent chat: persist session usage scope %s: %v", session.ID, err)
	}
}
