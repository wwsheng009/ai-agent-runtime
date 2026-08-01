package siteaccount

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// EndpointProbe describes a probe owned by one site adapter: an endpoint path
// plus the response characteristics that identify the site family. A probe
// only scores when its response satisfies BodyMatches or, for protected
// probes, returns an authentication challenge. This prevents generic gateway
// JSON responses from being mistaken for a specific site family.
type EndpointProbe struct {
	Path        string
	Label       string
	Score       int
	Protected   bool
	BodyMatches func([]byte) bool
}

// Adapter translates one provider-specific balance API into AccountSnapshot.
// Implementations should contain authentication, transport and payload
// normalization details; callers only depend on this contract.
type Adapter interface {
	SiteType() SiteType
	// AccountSources returns the source identifiers this adapter writes into
	// AccountSnapshot.Source. The registry uses these identifiers to recover a
	// site type from older cached snapshots that predate a site_type field.
	AccountSources() []string
	Probes() []EndpointProbe
	Fetch(context.Context, *Client, FetchInput) (*AccountSnapshot, error)
}

// AdapterRegistry is an immutable-after-construction lookup used by Client.
// Register replaces an adapter for the same canonical site type, which also
// makes controlled extension straightforward in tests or embedding programs.
type AdapterRegistry struct {
	adapters map[SiteType]Adapter
}

// NewAdapterRegistry constructs a registry from the supplied adapters.
func NewAdapterRegistry(adapters ...Adapter) *AdapterRegistry {
	registry := &AdapterRegistry{adapters: make(map[SiteType]Adapter)}
	for _, adapter := range adapters {
		registry.Register(adapter)
	}
	return registry
}

// DefaultAdapterRegistry contains all built-in site adapters.
func DefaultAdapterRegistry() *AdapterRegistry {
	return NewAdapterRegistry(
		sub2APIAdapter{},
		newAPIAdapter{},
		deepSeekAdapter{},
	)
}

// Register adds or replaces one adapter.
func (r *AdapterRegistry) Register(adapter Adapter) {
	if r == nil || adapter == nil {
		return
	}
	siteType := NormalizeSiteType(string(adapter.SiteType()))
	if siteType == "" || siteType == SiteTypeUnknown {
		return
	}
	r.adapters[siteType] = adapter
}

// Adapter returns the adapter registered for siteType.
func (r *AdapterRegistry) Adapter(siteType SiteType) (Adapter, bool) {
	if r == nil {
		return nil, false
	}
	adapter, ok := r.adapters[NormalizeSiteType(string(siteType))]
	return adapter, ok
}

// SupportsFetch reports whether a registered adapter can fetch account data for
// siteType. Callers should use this capability query instead of maintaining
// their own lists of site types.
func (r *AdapterRegistry) SupportsFetch(siteType SiteType) bool {
	_, ok := r.Adapter(siteType)
	return ok
}

// SiteTypeForAccountSource resolves a persisted AccountSnapshot.Source to the
// adapter that produced it. A canonical site type is also accepted for
// backwards-compatible caches.
func (r *AdapterRegistry) SiteTypeForAccountSource(source string) SiteType {
	if r == nil {
		return SiteTypeUnknown
	}
	if siteType := NormalizeSiteType(source); r.SupportsFetch(siteType) {
		return siteType
	}
	normalizedSource := strings.ToLower(strings.TrimSpace(source))
	if normalizedSource == "" {
		return SiteTypeUnknown
	}
	for _, adapter := range r.Adapters() {
		for _, source := range adapter.AccountSources() {
			if normalizedSource == strings.ToLower(strings.TrimSpace(source)) {
				return adapter.SiteType()
			}
		}
	}
	return SiteTypeUnknown
}

// SupportsAccountFetch reports whether a built-in site adapter supports
// account/balance retrieval. It is the shared eligibility check for periodic
// refresh loops and similar callers.
func SupportsAccountFetch(siteType SiteType) bool {
	return DefaultAdapterRegistry().SupportsFetch(siteType)
}

// SiteTypeFromAccountSource resolves a persisted account source with the
// built-in adapter registry.
func SiteTypeFromAccountSource(source string) SiteType {
	return DefaultAdapterRegistry().SiteTypeForAccountSource(source)
}

// Adapters returns a stable, site-type-sorted snapshot.
func (r *AdapterRegistry) Adapters() []Adapter {
	if r == nil {
		return nil
	}
	siteTypes := make([]string, 0, len(r.adapters))
	for siteType := range r.adapters {
		siteTypes = append(siteTypes, string(siteType))
	}
	sort.Strings(siteTypes)
	out := make([]Adapter, 0, len(siteTypes))
	for _, siteType := range siteTypes {
		out = append(out, r.adapters[SiteType(siteType)])
	}
	return out
}

// Fetch dispatches to the registered adapter.
func (r *AdapterRegistry) Fetch(
	ctx context.Context,
	client *Client,
	input FetchInput,
) (*AccountSnapshot, error) {
	siteType := NormalizeSiteType(string(input.SiteType))
	adapter, ok := r.Adapter(siteType)
	if !ok {
		return nil, unsupportedSite(siteType)
	}
	snapshot, err := adapter.Fetch(ctx, client, input)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, unexpectedPayload(
			fmt.Sprintf("%s adapter returned a nil snapshot", siteType),
			nil,
		)
	}
	// Enforce the common contract even if a custom adapter omitted metadata.
	snapshot.SiteType = siteType
	return snapshot, nil
}

type sub2APIAdapter struct{}

func (sub2APIAdapter) SiteType() SiteType { return SiteTypeSub2API }
func (sub2APIAdapter) AccountSources() []string {
	return []string{sub2APIUsageSource}
}
func (sub2APIAdapter) Probes() []EndpointProbe {
	return []EndpointProbe{
		{Path: "/api/v1/status", Label: "Sub2API v1 status", Score: 3, BodyMatches: looksLikeSub2APIStatus},
		{Path: "/api/v1/settings/public", Label: "Sub2API public settings", Score: 4, BodyMatches: looksLikeSub2APISettings},
		{Path: "/api/v1/auth/me", Label: "Sub2API auth me", Score: 3, Protected: true, BodyMatches: looksLikeSub2APIAuthChallenge},
		{Path: "/setup/status", Label: "Sub2API setup status", Score: 1, BodyMatches: looksLikeSub2APISetupStatus},
		{Path: "/health", Label: "Sub2API health"},
	}
}
func (sub2APIAdapter) Fetch(ctx context.Context, client *Client, input FetchInput) (*AccountSnapshot, error) {
	return client.fetchSub2APIUsage(ctx, input)
}

type newAPIAdapter struct{}

func (newAPIAdapter) SiteType() SiteType { return SiteTypeNewAPI }
func (newAPIAdapter) AccountSources() []string {
	return []string{newAPIUserSelfSource}
}
func (newAPIAdapter) Probes() []EndpointProbe {
	return []EndpointProbe{
		{Path: "/api/status", Label: "New-API status", Score: 5, BodyMatches: looksLikeNewAPIStatus},
		{Path: "/api/user/self", Label: "New-API user self", Score: 3, Protected: true, BodyMatches: looksLikeNewAPIAuthChallenge},
		{Path: "/api/user/self/groups", Label: "New-API user groups", Score: 3, Protected: true, BodyMatches: looksLikeNewAPIAuthChallenge},
	}
}
func (newAPIAdapter) Fetch(ctx context.Context, client *Client, input FetchInput) (*AccountSnapshot, error) {
	return client.fetchNewAPIUserSelf(ctx, input)
}
