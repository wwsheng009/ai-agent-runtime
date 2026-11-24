package runtimeapi

import (
	"net/http"
	"strings"
)

// teamProductIdentity is the trusted principal used for product Definition
// and Run ownership. It intentionally excludes request body, query, and
// configurable header values: those are caller-controlled unless separately
// authenticated, and must never choose a resource owner or workspace.
type teamProductIdentity struct {
	OwnerID     string
	WorkspaceID string
}

func (h *Handler) resolveTeamProductIdentity(r *http.Request) (teamProductIdentity, bool) {
	if h == nil || r == nil {
		return teamProductIdentity{}, false
	}
	config := h.getScopeResolverConfig()
	if !config.Enabled {
		return teamProductIdentity{}, false
	}

	jwtScope := h.resolveScopeFromJWTClaims(r)
	apiKeyScope := h.resolveScopeFromAPIKey(r)
	jwtIdentity, jwtOK := teamProductIdentityFromScope(jwtScope)
	apiKeyIdentity, apiKeyOK := teamProductIdentityFromScope(apiKeyScope)

	// A request carrying two independently trusted credentials with different
	// principals is ambiguous. Fail closed rather than selecting whichever
	// credential happens to win resolver precedence.
	if jwtOK && apiKeyOK && jwtIdentity != apiKeyIdentity {
		return teamProductIdentity{}, false
	}
	if jwtOK {
		return jwtIdentity, true
	}
	if apiKeyOK {
		return apiKeyIdentity, true
	}
	return teamProductIdentity{}, false
}

func teamProductIdentityFromScope(scope UsageScope) (teamProductIdentity, bool) {
	identity := teamProductIdentity{
		OwnerID:     strings.TrimSpace(scope.UserID),
		WorkspaceID: strings.TrimSpace(scope.WorkspaceID),
	}
	if identity.OwnerID == "" || identity.WorkspaceID == "" ||
		strings.EqualFold(identity.OwnerID, "anonymous") {
		return teamProductIdentity{}, false
	}
	return identity, true
}
