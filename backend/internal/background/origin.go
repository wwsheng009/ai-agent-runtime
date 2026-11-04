package background

import (
	"context"
	"strings"
)

// JobOrigin captures the parent turn/episode that submitted a background job.
// It is attached by the tool broker from the run context, never from
// model-supplied tool arguments. Durable job metadata keeps the identity so a
// terminal projection can decide between same-turn resume and notification-only
// long after the submitting request returned.
type JobOrigin struct {
	RootScopeID        string
	ParentSessionID    string
	ParentTurnID       string
	ParentEpisodeID    string
	SettlementID       string
	SettlementCohortID string
	// FinalizationPolicy is "required", "detached" or "explicit_followup".
	// Empty is normalized by the supervision projection to detached.
	FinalizationPolicy string
}

type jobOriginContextKey struct{}

// WithJobOrigin annotates a job submission with its parent identity.
func WithJobOrigin(ctx context.Context, origin JobOrigin) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, jobOriginContextKey{}, normalizeJobOrigin(origin))
}

// JobOriginFromContext returns the submitting run's parent identity, or the
// zero value for direct manager callers and legacy paths.
func JobOriginFromContext(ctx context.Context) JobOrigin {
	if ctx == nil {
		return JobOrigin{}
	}
	origin, _ := ctx.Value(jobOriginContextKey{}).(JobOrigin)
	return normalizeJobOrigin(origin)
}

func normalizeJobOrigin(origin JobOrigin) JobOrigin {
	origin.RootScopeID = strings.TrimSpace(origin.RootScopeID)
	origin.ParentSessionID = strings.TrimSpace(origin.ParentSessionID)
	origin.ParentTurnID = strings.TrimSpace(origin.ParentTurnID)
	origin.ParentEpisodeID = strings.TrimSpace(origin.ParentEpisodeID)
	origin.SettlementID = strings.TrimSpace(origin.SettlementID)
	origin.SettlementCohortID = strings.TrimSpace(origin.SettlementCohortID)
	origin.FinalizationPolicy = strings.ToLower(strings.TrimSpace(origin.FinalizationPolicy))
	return origin
}

// ApplyJobOrigin copies the identity into durable job metadata. It is a no-op
// when no origin was captured, so direct/legacy submissions stay compatible.
func ApplyJobOrigin(metadata map[string]interface{}, origin JobOrigin) {
	origin = normalizeJobOrigin(origin)
	if metadata == nil || origin == (JobOrigin{}) {
		return
	}
	values := map[string]string{
		JobMetadataRootScopeID:        origin.RootScopeID,
		JobMetadataParentSessionID:    origin.ParentSessionID,
		JobMetadataParentTurnID:       origin.ParentTurnID,
		JobMetadataParentEpisodeID:    origin.ParentEpisodeID,
		JobMetadataSettlementID:       origin.SettlementID,
		JobMetadataSettlementCohortID: origin.SettlementCohortID,
		JobMetadataFinalizationPolicy: origin.FinalizationPolicy,
	}
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		metadata[key] = value
	}
}

// JobOriginFromMetadata reads the identity persisted with a job. Missing keys
// stay empty; callers decide the safe fallback.
func JobOriginFromMetadata(metadata map[string]interface{}) JobOrigin {
	read := func(key string) string {
		if metadata == nil {
			return ""
		}
		value, _ := metadata[key].(string)
		return strings.TrimSpace(value)
	}
	return normalizeJobOrigin(JobOrigin{
		RootScopeID:        read(JobMetadataRootScopeID),
		ParentSessionID:    read(JobMetadataParentSessionID),
		ParentTurnID:       read(JobMetadataParentTurnID),
		ParentEpisodeID:    read(JobMetadataParentEpisodeID),
		SettlementID:       read(JobMetadataSettlementID),
		SettlementCohortID: read(JobMetadataSettlementCohortID),
		FinalizationPolicy: read(JobMetadataFinalizationPolicy),
	})
}

const (
	JobMetadataRootScopeID        = "root_scope_id"
	JobMetadataParentSessionID    = "parent_session_id"
	JobMetadataParentTurnID       = "parent_turn_id"
	JobMetadataParentEpisodeID    = "parent_episode_id"
	JobMetadataSettlementID       = "settlement_id"
	JobMetadataSettlementCohortID = "settlement_cohort_id"
	JobMetadataFinalizationPolicy = "finalization_policy"
)

// Background job policies. Plain strings avoid a dependency from background to
// the supervision/subagentbatch control planes; the projection maps unknowns
// back to the safe detached default.
type JobFinalizationPolicy string

const (
	JobPolicyRequired         JobFinalizationPolicy = "required"
	JobPolicyDetached         JobFinalizationPolicy = "detached"
	JobPolicyExplicitFollowup JobFinalizationPolicy = "explicit_followup"
)

// ParseJobFinalizationPolicy canonicalizes a caller-supplied policy. Empty is
// the backward-compatible detached default for background jobs; an unknown value
// is rejected instead of being silently downgraded, because a typo in
// finalization_policy must not turn required work into a detached job.
func ParseJobFinalizationPolicy(value string) (JobFinalizationPolicy, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "detached":
		return JobPolicyDetached, true
	case "required":
		return JobPolicyRequired, true
	case "explicit_followup", "explicit-followup":
		return JobPolicyExplicitFollowup, true
	default:
		return "", false
	}
}
