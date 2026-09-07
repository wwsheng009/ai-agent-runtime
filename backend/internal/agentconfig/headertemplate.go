package agentconfig

import (
	"strings"
)

// Header template placeholders referenced in providers.headers and
// providers.items.<name>.headers values. They mirror the session-scoped
// headers sent by opencode clients: the opencode gateway provider carries
// x-opencode-session / x-opencode-project / x-opencode-request /
// x-opencode-client, while other providers use x-session-affinity /
// X-Session-Id. With templates the header name stays fully configurable per
// provider while the value is filled at request-build time.
const (
	// HeaderTemplateSessionID resolves to the current runtime session ID
	// (x-opencode-session / x-session-affinity value in opencode).
	HeaderTemplateSessionID = "{session_id}"
	// HeaderTemplateParentSessionID resolves to the parent session ID when the
	// session runs as a supervised agent child (x-parent-session-id in opencode).
	HeaderTemplateParentSessionID = "{parent_session_id}"
	// HeaderTemplateUserID resolves to the session user ID
	// (x-opencode-request value in opencode).
	HeaderTemplateUserID = "{user_id}"
	// HeaderTemplateProjectID resolves to a stable hash of the working
	// directory (x-opencode-project value in opencode).
	HeaderTemplateProjectID = "{project_id}"
	// HeaderTemplateProvider resolves to the current provider name.
	HeaderTemplateProvider = "{provider}"
	// HeaderTemplateModel resolves to the current effective model name.
	HeaderTemplateModel = "{model}"
	// HeaderTemplateClient resolves to the client identity token
	// (x-opencode-client value in opencode).
	HeaderTemplateClient = "{client}"
)

// HeaderTemplateContext carries the runtime values used to resolve header
// value templates. Empty fields resolve to the empty string.
type HeaderTemplateContext struct {
	SessionID       string
	ParentSessionID string
	UserID          string
	ProjectID       string
	Provider        string
	Model           string
	Client          string
}

// HeaderTemplatePlaceholderNames lists every supported placeholder for
// documentation and validation purposes.
func HeaderTemplatePlaceholderNames() []string {
	return []string{
		HeaderTemplateSessionID,
		HeaderTemplateParentSessionID,
		HeaderTemplateUserID,
		HeaderTemplateProjectID,
		HeaderTemplateProvider,
		HeaderTemplateModel,
		HeaderTemplateClient,
	}
}

// ResolveHeaderValueTemplates replaces known placeholders in value with the
// context values. Unknown placeholders are preserved verbatim so literal
// braces in header values are never damaged. Values containing no '{' are
// returned unchanged.
func ResolveHeaderValueTemplates(value string, ctx HeaderTemplateContext) string {
	if value == "" || !strings.Contains(value, "{") {
		return value
	}
	value = strings.ReplaceAll(value, HeaderTemplateSessionID, ctx.SessionID)
	value = strings.ReplaceAll(value, HeaderTemplateParentSessionID, ctx.ParentSessionID)
	value = strings.ReplaceAll(value, HeaderTemplateUserID, ctx.UserID)
	value = strings.ReplaceAll(value, HeaderTemplateProjectID, ctx.ProjectID)
	value = strings.ReplaceAll(value, HeaderTemplateProvider, ctx.Provider)
	value = strings.ReplaceAll(value, HeaderTemplateModel, ctx.Model)
	value = strings.ReplaceAll(value, HeaderTemplateClient, ctx.Client)
	return value
}

// ResolveHeaderTemplates resolves templates in every header value. The input
// map is not mutated; an empty/nil input is returned as-is.
func ResolveHeaderTemplates(headers map[string]string, ctx HeaderTemplateContext) map[string]string {
	if len(headers) == 0 {
		return headers
	}
	resolved := make(map[string]string, len(headers))
	for key, value := range headers {
		resolved[key] = ResolveHeaderValueTemplates(value, ctx)
	}
	return resolved
}
