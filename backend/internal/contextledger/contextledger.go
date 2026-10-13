// Package contextledger maintains the provider-observed context anchor and
// projects the next prompt's token size.
//
// Design (2026-10-11 provider-usage-first incident fix): provider-reported
// usage is the authoritative absolute context measurement. The local estimator
// is only used to project growth since the anchor ("estimate for delta") and
// as the fallback when no anchor is available. Any pre-send decision (prompt
// preflight gate, auto-compact trigger, status display) must compare the
// projection, never the raw full-history estimate.
//
// The projection is:
//
//	delta      = max(0, messageEstimateNow - observation.MessageEstimate)
//	projection = max(observation.PromptTokens + delta,
//	                 messageEstimateNow + toolTokensNow)
//
// When the anchor identity is invalidated (epoch change after a restart,
// history rewrite, ...) but a provider observation still exists, the
// estimated fallback is scaled by the observation's wire-bias ratio
// (observation.PromptTokens / observation.MessageEstimate, clamped to
// [1, estimateCalibrationMax]): the local estimator measures parsed message
// content while the provider counts the serialized wire payload, a systematic
// 1.2x-1.9x undercount. Without the scaling an invalidated anchor silently
// re-opens the oversized-request failure class the anchor was introduced to
// close (2026-10-11: 1.18x; 2026-10-13: 1.56x-1.85x, gate admitted a
// ~1.1M-token prompt against a 1M window).
//
// The anchor is invalidated when the provider/model, prompt-cache epoch, tool
// surface fingerprint or the message prefix it was observed on changes.
package contextledger

import (
	"math"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	// SourceProviderReported marks observations that come from provider
	// usage. Only these may become anchors; locally back-filled estimates
	// must never be stored as observations.
	SourceProviderReported = "provider_reported"

	// ProjectionAnchored means the projection is based on a valid anchor.
	ProjectionAnchored = "anchored"
	// ProjectionEstimated means no valid anchor exists; the value is the
	// local full estimate (legacy behavior).
	ProjectionEstimated = "estimated"

	// anchorCeilingFactor rejects anchors that are implausibly larger than the
	// current local estimate. Its target is order-of-magnitude garbage (e.g. a
	// run-level cumulative usage wrongly stored as an anchor: 100x+ for a long
	// turn), not estimator error: the 2026-10-11 incident measured ~1.18x
	// (gate estimate 223,594 vs provider count 262,948), and wire-format JSON
	// overhead plus byte-level tokenizers can plausibly reach ~5x. 16x keeps
	// the guard effective against corruption without rejecting legitimate
	// anchors.
	anchorCeilingFactor = 16

	// estimateCalibrationMax caps the wire-bias calibration applied to the
	// estimated fallback. The observed samples sit at 1.18x (2026-10-11) and
	// 1.56x-1.85x (2026-10-13); the cap only guards corrupted observations,
	// implausible-anchor detection stays in invalidReason.
	estimateCalibrationMax = 3.0
)

// Observation is a frozen record of one provider-reported request usage plus
// the identity of the exact prompt that produced it.
type Observation struct {
	// PromptTokens is the provider-reported complete input count
	// (TokenUsage.InputTotal: includes cached read/write).
	PromptTokens int
	// MessageEstimate is the local estimate of the sent messages only
	// (excluding tool schemas) at observation time.
	MessageEstimate int
	// Fingerprint is the message fingerprint of the sent prompt.
	Fingerprint string
	// MessageCount is the number of sent messages (prefix validation).
	MessageCount int
	// ToolFingerprint is ToolDefinitionsFingerprint of the sent tool surface.
	ToolFingerprint string
	// Epoch is the prompt-cache epoch actually used by the request.
	Epoch int
	// Provider/Model identify the tokenizer that produced PromptTokens.
	Provider string
	Model    string
	// Source must be SourceProviderReported.
	Source string
	// ObservedAt is informational.
	ObservedAt time.Time
}

// Valid reports whether the observation carries the minimum identity needed
// to be used as an anchor.
func (o Observation) Valid() bool {
	return o.PromptTokens > 0 &&
		o.MessageCount > 0 &&
		strings.TrimSpace(o.Fingerprint) != ""
}

// Fingerprinter hashes a message list into a stable identity. Callers inject
// the same fingerprint function used for prompt-cache prefix validation
// (agent.promptMessageFingerprint).
type Fingerprinter func([]types.Message) string

// ProjectInput describes the prompt whose size is being projected.
type ProjectInput struct {
	// Messages is the current prompt message list.
	Messages []types.Message
	// MessageEstimate is the local estimate of Messages only.
	MessageEstimate int
	// ToolTokens is the local estimate of the current tool schemas.
	ToolTokens int
	// ToolFingerprint identifies the current tool surface.
	ToolFingerprint string
	// Epoch is the current prompt-cache epoch.
	Epoch int
	// Provider/Model are the current request identity.
	Provider string
	Model    string
}

// Projection is the decision value: use Tokens for every gate/trigger and
// display comparison. Source and InvalidReason are observability fields.
type Projection struct {
	Tokens         int
	EstimateTokens int
	AnchorTokens   int
	DeltaTokens    int
	// Calibration is the wire-bias factor applied to the estimate when the
	// projection fell back to estimated with a usable observation (0 = none).
	Calibration   float64
	Source        string
	InvalidReason string
}

// Ledger stores the latest observation for one session/run. It is safe for
// concurrent use; Project and Observe are individually atomic.
type Ledger struct {
	mu  sync.Mutex
	obs Observation
	ok  bool
	fp  Fingerprinter
}

// New creates a ledger using fp for prefix validation. A nil fp disables
// prefix validation and makes Project fall back to estimated.
func New(fp Fingerprinter) *Ledger {
	return &Ledger{fp: fp}
}

// Observe stores obs as the current anchor. It rejects observations that are
// not provider-reported or lack prompt identity, returning false without
// changing the ledger.
func (l *Ledger) Observe(obs Observation) bool {
	if l == nil {
		return false
	}
	if strings.TrimSpace(obs.Source) != SourceProviderReported {
		return false
	}
	if !obs.Valid() {
		return false
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now()
	}
	l.mu.Lock()
	l.obs = obs
	l.ok = true
	l.mu.Unlock()
	return true
}

// Snapshot returns the current observation (zero value when unset).
func (l *Ledger) Snapshot() (Observation, bool) {
	if l == nil {
		return Observation{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.obs, l.ok
}

// Clear drops the anchor; the next projection falls back to estimated.
func (l *Ledger) Clear() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.obs = Observation{}
	l.ok = false
	l.mu.Unlock()
}

// Project returns the projected token size for in.
func (l *Ledger) Project(in ProjectInput) Projection {
	if l == nil {
		return projectEstimate(in, "no_ledger", 0)
	}
	l.mu.Lock()
	obs, ok, fp := l.obs, l.ok, l.fp
	l.mu.Unlock()
	return Project(obs, ok, fp, in)
}

// Project is the pure projection function (also usable by hosts that keep
// their own observation storage, e.g. the chat actor pre-turn check).
func Project(obs Observation, ok bool, fp Fingerprinter, in ProjectInput) Projection {
	if !ok {
		return projectEstimate(in, "no_anchor", 0)
	}
	if !obs.Valid() {
		return projectEstimate(in, "anchor_invalid", 0)
	}
	if reason := invalidReason(obs, fp, in); reason != "" {
		return projectEstimate(in, reason, estimateCalibration(obs))
	}

	delta := in.MessageEstimate - obs.MessageEstimate
	if delta < 0 {
		delta = 0
	}
	tokens := obs.PromptTokens + delta
	estimate := estimateTokens(in)
	if tokens < estimate {
		tokens = estimate
	}
	return Projection{
		Tokens:         tokens,
		EstimateTokens: estimate,
		AnchorTokens:   obs.PromptTokens,
		DeltaTokens:    delta,
		Source:         ProjectionAnchored,
	}
}

// estimateCalibration returns the observation's wire-bias ratio
// (provider prompt tokens over the local message estimate at observation
// time), clamped to [1, estimateCalibrationMax]. 0 means "unavailable": the
// caller keeps the raw local estimate.
func estimateCalibration(obs Observation) float64 {
	if obs.PromptTokens <= 0 || obs.MessageEstimate <= 0 {
		return 0
	}
	ratio := float64(obs.PromptTokens) / float64(obs.MessageEstimate)
	if ratio < 1 {
		return 1
	}
	if ratio > estimateCalibrationMax {
		return estimateCalibrationMax
	}
	return ratio
}

func projectEstimate(in ProjectInput, reason string, calibration float64) Projection {
	estimate := estimateTokens(in)
	tokens := estimate
	if calibration > 1 {
		// Ceil so the gate never rounds a borderline payload down below the
		// provider's expected count.
		tokens = int(math.Ceil(float64(estimate) * calibration))
	}
	return Projection{
		Tokens:         tokens,
		EstimateTokens: estimate,
		Calibration:    calibration,
		Source:         ProjectionEstimated,
		InvalidReason:  reason,
	}
}

func estimateTokens(in ProjectInput) int {
	estimate := in.MessageEstimate + in.ToolTokens
	if estimate < 0 {
		return 0
	}
	return estimate
}

func invalidReason(obs Observation, fp Fingerprinter, in ProjectInput) string {
	if obs.Provider != "" && in.Provider != "" && !strings.EqualFold(obs.Provider, in.Provider) {
		return "provider_changed"
	}
	if obs.Model != "" && in.Model != "" && !strings.EqualFold(obs.Model, in.Model) {
		return "model_changed"
	}
	if in.Epoch != obs.Epoch {
		return "epoch_changed"
	}
	// An empty current tool fingerprint means the caller cannot identify the
	// surface (e.g. the pre-turn check before the turn's tools are frozen);
	// prefix/epoch/provider identity still validate the anchor in that case.
	if in.ToolFingerprint != "" && in.ToolFingerprint != obs.ToolFingerprint {
		return "tool_surface_changed"
	}
	if len(in.Messages) < obs.MessageCount {
		return "history_shrank"
	}
	if fp == nil {
		return "fingerprint_unavailable"
	}
	if prefix := fp(in.Messages[:obs.MessageCount]); prefix != obs.Fingerprint {
		return "prefix_changed"
	}
	// Implausible-anchor guard: applied last so the cheapest structural
	// checks report first.
	if estimate := estimateTokens(in); estimate > 0 && obs.PromptTokens > anchorCeilingFactor*estimate {
		return "anchor_anomaly"
	}
	return ""
}
