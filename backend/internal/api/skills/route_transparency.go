package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

func (h *Handler) attachSessionExecutionRoute(ctx context.Context, sessionID string, payload map[string]interface{}) map[string]interface{} {
	if h == nil || h.sessionManager == nil {
		return payload
	}
	session, err := h.sessionManager.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil || session == nil {
		return payload
	}
	route, ok := executionRouteTransparencyFromSession(session)
	if !ok {
		return payload
	}
	return attachExecutionRouteTransparency(payload, route)
}

type executionRouteTransparency struct {
	RequestedProvider        string
	EffectiveProvider        string
	RequestedModel           string
	EffectiveModel           string
	RequestedReasoningEffort string
	EffectiveReasoningEffort string
	RequestedPermissionMode  string
	EffectivePermissionMode  string
	RouteWarnings            []string
	FallbackUsed             bool
	FallbackReason           string
}

func executionRouteTransparencyFromSession(session *chat.Session) (executionRouteTransparency, bool) {
	if session == nil {
		return executionRouteTransparency{}, false
	}
	context := session.Metadata.Context
	route := executionRouteTransparency{
		RequestedProvider: firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.RequestedProvider),
			sessionmeta.String(context, sessionmeta.ProviderName),
		),
		EffectiveProvider: firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.EffectiveProvider),
			sessionmeta.String(context, sessionmeta.ProviderName),
		),
		RequestedModel: firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.RequestedModel),
			sessionmeta.String(context, sessionmeta.Model),
		),
		EffectiveModel: firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.EffectiveModel),
			sessionmeta.String(context, sessionmeta.Model),
		),
		RequestedReasoningEffort: firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.RequestedReasoningEffort),
			sessionmeta.String(context, sessionmeta.ReasoningEffort),
		),
		EffectiveReasoningEffort: firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.EffectiveReasoningEffort),
			sessionmeta.String(context, sessionmeta.ReasoningEffort),
		),
		RequestedPermissionMode: firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.RequestedPermissionMode),
			sessionmeta.String(context, sessionmeta.PermissionMode),
		),
		EffectivePermissionMode: firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.EffectivePermissionMode),
			sessionmeta.String(context, sessionmeta.PermissionMode),
		),
		FallbackReason: sessionmeta.String(context, sessionmeta.FallbackReason),
	}
	route.FallbackUsed, _ = sessionmeta.Bool(context, sessionmeta.FallbackUsed)
	if value, ok := sessionmeta.Value(context, sessionmeta.RouteWarnings); ok {
		switch warnings := value.(type) {
		case []string:
			route.RouteWarnings = append([]string(nil), warnings...)
		case []interface{}:
			for _, warning := range warnings {
				if text := strings.TrimSpace(fmt.Sprint(warning)); text != "" && text != "<nil>" {
					route.RouteWarnings = append(route.RouteWarnings, text)
				}
			}
		}
	}
	available := route.RequestedProvider != "" || route.EffectiveProvider != "" ||
		route.RequestedModel != "" || route.EffectiveModel != "" ||
		route.RequestedReasoningEffort != "" || route.EffectiveReasoningEffort != "" ||
		route.RequestedPermissionMode != "" || route.EffectivePermissionMode != ""
	return route, available
}

func resolveAgentChatRouteTransparency(runtime *llm.LLMRuntime, session *chat.Session, requestedProvider, requestedModel, requestedReasoning, provider, model, reasoning string) executionRouteTransparency {
	effectiveProvider := strings.TrimSpace(provider)
	if runtime != nil {
		if resolved := runtime.ResolveProviderName(effectiveProvider); resolved != "" {
			effectiveProvider = resolved
		} else if resolved := runtime.ResolveProviderName(model); resolved != "" {
			effectiveProvider = resolved
		}
	}
	route := executionRouteTransparency{
		RequestedProvider:        firstNonEmptyString(requestedProvider, provider, effectiveProvider),
		EffectiveProvider:        firstNonEmptyString(effectiveProvider, provider),
		RequestedModel:           firstNonEmptyString(requestedModel, model),
		EffectiveModel:           strings.TrimSpace(model),
		RequestedReasoningEffort: strings.TrimSpace(requestedReasoning),
		EffectiveReasoningEffort: strings.TrimSpace(reasoning),
	}
	if session != nil {
		context := session.Metadata.Context
		route.RequestedPermissionMode = firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.RequestedPermissionMode),
			sessionmeta.String(context, sessionmeta.PermissionMode),
		)
		route.EffectivePermissionMode = firstNonEmptyString(
			sessionmeta.String(context, sessionmeta.EffectivePermissionMode),
			sessionmeta.String(context, sessionmeta.PermissionMode),
		)
	}
	return route
}

func (r executionRouteTransparency) payload() map[string]interface{} {
	payload := map[string]interface{}{
		"requested_provider":         strings.TrimSpace(r.RequestedProvider),
		"effective_provider":         strings.TrimSpace(r.EffectiveProvider),
		"requested_model":            strings.TrimSpace(r.RequestedModel),
		"effective_model":            strings.TrimSpace(r.EffectiveModel),
		"requested_reasoning_effort": strings.TrimSpace(r.RequestedReasoningEffort),
		"effective_reasoning_effort": strings.TrimSpace(r.EffectiveReasoningEffort),
		"requested_permission_mode":  strings.TrimSpace(r.RequestedPermissionMode),
		"effective_permission_mode":  strings.TrimSpace(r.EffectivePermissionMode),
		"route_warnings":             append([]string(nil), r.RouteWarnings...),
		"fallback_used":              r.FallbackUsed,
		"fallback_reason":            strings.TrimSpace(r.FallbackReason),
	}
	for key, value := range payload {
		if text, ok := value.(string); ok && text == "" {
			delete(payload, key)
		}
	}
	return payload
}

func attachExecutionRouteTransparency(payload map[string]interface{}, route executionRouteTransparency) map[string]interface{} {
	if payload == nil {
		payload = make(map[string]interface{})
	}
	for key, value := range route.payload() {
		payload[key] = value
	}
	return payload
}

func persistExecutionRouteTransparency(session *chat.Session, route executionRouteTransparency) {
	if session == nil {
		return
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = make(map[string]interface{})
	}
	context := session.Metadata.Context
	setString := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			sessionmeta.Delete(context, key)
			return
		}
		sessionmeta.Set(context, key, value)
	}
	setString(sessionmeta.RequestedProvider, route.RequestedProvider)
	setString(sessionmeta.EffectiveProvider, route.EffectiveProvider)
	setString(sessionmeta.ProviderName, route.EffectiveProvider)
	setString(sessionmeta.RequestedModel, route.RequestedModel)
	setString(sessionmeta.EffectiveModel, route.EffectiveModel)
	setString(sessionmeta.Model, route.EffectiveModel)
	setString(sessionmeta.RequestedReasoningEffort, route.RequestedReasoningEffort)
	setString(sessionmeta.EffectiveReasoningEffort, route.EffectiveReasoningEffort)
	setString(sessionmeta.ReasoningEffort, route.EffectiveReasoningEffort)
	setString(sessionmeta.RequestedPermissionMode, route.RequestedPermissionMode)
	setString(sessionmeta.EffectivePermissionMode, route.EffectivePermissionMode)
	if len(route.RouteWarnings) > 0 {
		sessionmeta.Set(context, sessionmeta.RouteWarnings, append([]string(nil), route.RouteWarnings...))
	} else {
		sessionmeta.Delete(context, sessionmeta.RouteWarnings)
	}
	sessionmeta.Set(context, sessionmeta.FallbackUsed, route.FallbackUsed)
	if strings.TrimSpace(route.FallbackReason) == "" {
		sessionmeta.Delete(context, sessionmeta.FallbackReason)
	} else {
		sessionmeta.Set(context, sessionmeta.FallbackReason, strings.TrimSpace(route.FallbackReason))
	}
}
