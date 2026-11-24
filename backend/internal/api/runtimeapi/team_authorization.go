package runtimeapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
)

type legacyTeamIdentityContextKey struct{}
type legacyTeamLocalModeContextKey struct{}

// legacyTeamAuthorizationMiddleware applies the workspace boundary that
// legacy Team records themselves do not carry an owner field for. Every
// request requires an independently verified Team product identity;
// loopback transport and the Runtime access token alone are not identities.
func (h *Handler) legacyTeamAuthorizationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if next == nil {
			http.NotFound(w, r)
			return
		}
		if !h.getScopeResolverConfig().Enabled && isDirectLoopbackRequest(r) {
			// Explicitly unconfigured local development mode retains the
			// single-user loopback experience. Any non-loopback request still
			// fails closed below; disabling scope resolution is not a remote
			// authorization bypass.
			ctx := context.WithValue(r.Context(), legacyTeamLocalModeContextKey{}, true)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		identity, ok := h.requireLegacyTeamIdentity(w, r)
		if !ok {
			return
		}

		if teamID := strings.TrimSpace(mux.Vars(r)["id"]); teamID != "" {
			store := h.getTeamStore()
			if store == nil {
				writeTeamProductError(w, r, http.StatusServiceUnavailable, "TEAM_STORE_UNAVAILABLE",
					"Team resources are unavailable", "unavailable", true)
				return
			}
			record, err := store.GetTeam(r.Context(), teamID)
			if err != nil {
				writeTeamProductError(w, r, http.StatusInternalServerError, "TEAM_PRODUCT_INTERNAL",
					"Team resource authorization failed", "internal", false)
				return
			}
			if record == nil || !strings.EqualFold(strings.TrimSpace(record.WorkspaceID), identity.WorkspaceID) {
				// Keep missing and foreign-workspace records indistinguishable.
				writeTeamProductError(w, r, http.StatusNotFound, "TEAM_NOT_FOUND",
					"Team not found", "not_found", false)
				return
			}
		}

		ctx := context.WithValue(r.Context(), legacyTeamIdentityContextKey{}, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func legacyTeamLocalModeFromRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	local, _ := r.Context().Value(legacyTeamLocalModeContextKey{}).(bool)
	return local
}

func (h *Handler) requireLegacyTeamIdentity(w http.ResponseWriter, r *http.Request) (teamProductIdentity, bool) {
	if identity, ok := legacyTeamIdentityFromRequest(r); ok {
		return identity, true
	}
	identity, ok := h.resolveTeamProductIdentity(r)
	if !ok {
		writeTeamProductError(w, r, http.StatusUnauthorized, "TEAM_PRODUCT_UNAUTHENTICATED",
			"verified user and workspace identity are required", "authorization", false)
		return teamProductIdentity{}, false
	}
	return identity, true
}

func legacyTeamIdentityFromRequest(r *http.Request) (teamProductIdentity, bool) {
	if r == nil {
		return teamProductIdentity{}, false
	}
	identity, ok := r.Context().Value(legacyTeamIdentityContextKey{}).(teamProductIdentity)
	if !ok || strings.TrimSpace(identity.OwnerID) == "" || strings.TrimSpace(identity.WorkspaceID) == "" {
		return teamProductIdentity{}, false
	}
	return identity, true
}

func rejectLegacyTeamWorkspaceMismatch(w http.ResponseWriter, r *http.Request) {
	writeTeamProductError(w, r, http.StatusForbidden, "TEAM_WORKSPACE_FORBIDDEN",
		"Team workspace does not match the verified caller identity", "authorization", false)
}
