package memoryruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

const (
	summaryInputFingerprintKey = "input_fingerprint"
	// Bump this when the project-summary prompt, deterministic renderer,
	// selection rules or output validation contract changes. Job buckets and
	// rebuild reasons are deliberately not part of the generation identity.
	projectSummaryTemplateVersion = "project_summary_v1"
)

// Preparation is shared by the reuse check and generation: the fingerprint
// must describe the budgeted, deduplicated records actually sent to the model,
// not the larger active-record scan.
type preparedProjectSummary struct {
	text        string
	ids         []string
	tokens      int
	budget      int
	records     []memorystore.Record
	fingerprint string
}

func (c *Consolidator) prepareSummary(records []memorystore.Record) preparedProjectSummary {
	input := preparedProjectSummary{budget: c.Config.SummaryTokenBudget}
	if input.budget <= 0 {
		input.budget = 800
	}
	input.text, input.ids, input.tokens = deterministicSummary(records, input.budget)
	input.records = filterRecordsByIDs(records, input.ids)
	if len(input.records) > 0 {
		input.fingerprint = c.summaryInputFingerprint(input)
	}
	return input
}

type summaryGenerationSettings struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}

func projectSummaryGenerationSettings(cfg Config) summaryGenerationSettings {
	return summaryGenerationSettings{
		Provider: firstNonEmpty(cfg.ConsolidationProvider, cfg.ExtractionProvider),
		Model:    firstNonEmpty(cfg.ConsolidationModel, cfg.ExtractionModel),
		Effort:   firstNonEmpty(cfg.ConsolidationReasoningEffort, cfg.ExtractionReasoningEffort, "low"),
	}
}

// Only durable source/content/freshness state and prompt-visible fields belong
// here. UseCount, LastUsedAt and UpdatedAt affect selection/order, which is
// already reflected by the ordered record list; including their raw values
// would invalidate an otherwise identical input after every recall.
type summaryFingerprintRecord struct {
	ID              string                `json:"id"`
	Scope           memorystore.Scope     `json:"scope"`
	ProjectKey      string                `json:"project_key"`
	Kind            string                `json:"kind"`
	Text            string                `json:"text"`
	Tags            []string              `json:"tags,omitempty"`
	Confidence      float64               `json:"confidence"`
	ContentHash     string                `json:"content_hash"`
	Revision        int64                 `json:"revision"`
	Status          memorystore.Status    `json:"status"`
	Freshness       memorystore.Freshness `json:"freshness"`
	Sensitivity     string                `json:"sensitivity"`
	Pinned          bool                  `json:"pinned"`
	SourceKind      string                `json:"source_kind"`
	SourceHash      string                `json:"source_hash"`
	SourceRevision  int64                 `json:"source_revision"`
	ValidFrom       time.Time             `json:"valid_from"`
	ValidUntil      time.Time             `json:"valid_until"`
	RevalidateAfter time.Time             `json:"revalidate_after"`
	LastVerifiedAt  time.Time             `json:"last_verified_at"`
	StaleReason     string                `json:"stale_reason"`
	DeletedAt       time.Time             `json:"deleted_at"`
}

func (c *Consolidator) summaryInputFingerprint(input preparedProjectSummary) string {
	projectKey := ""
	if c.Store != nil {
		projectKey = c.Store.ProjectKey()
	}
	payload := struct {
		Template    string                     `json:"template"`
		ProjectKey  string                     `json:"project_key"`
		Budget      int                        `json:"budget"`
		MaxTokens   int                        `json:"max_tokens"`
		Generator   string                     `json:"generator"`
		Settings    summaryGenerationSettings  `json:"settings"`
		LLMSettings *summaryGenerationSettings `json:"llm_settings,omitempty"`
		LLMDefaults *summaryGenerationSettings `json:"llm_defaults,omitempty"`
		Records     []summaryFingerprintRecord `json:"records"`
	}{
		Template: projectSummaryTemplateVersion, ProjectKey: projectKey,
		Budget: input.budget, MaxTokens: summaryModelMaxTokens(input.budget),
		Generator: fmt.Sprintf("%T", c.Config.Summarizer),
		Settings:  projectSummaryGenerationSettings(c.Config),
		Records:   make([]summaryFingerprintRecord, 0, len(input.records)),
	}
	// WithLLMProjectSummarizer holds a configuration snapshot. Include its
	// actual routing too, so changing that snapshot cannot silently reuse a
	// summary made by a different provider/model/effort.
	if summarizer, ok := c.Config.Summarizer.(*LLMProjectSummarizer); ok && summarizer != nil {
		settings := projectSummaryGenerationSettings(summarizer.Config)
		payload.LLMSettings = &settings
		if summarizer.Runtime != nil && (settings.Provider == "" || settings.Model == "") {
			// Empty request routing can fall back to runtime defaults. A
			// restart/hot reload that changes those defaults is not a hit.
			defaults := summaryGenerationSettings{}
			if settings.Provider == "" {
				defaults.Provider = strings.TrimSpace(summarizer.Runtime.DefaultProvider())
			}
			if settings.Model == "" {
				defaults.Model = strings.TrimSpace(summarizer.Runtime.DefaultModel())
			}
			payload.LLMDefaults = &defaults
		}
	}
	for _, record := range input.records {
		payload.Records = append(payload.Records, summaryFingerprintRecord{
			ID: record.ID, Scope: record.Scope, ProjectKey: record.ProjectKey,
			Kind: record.Kind, Text: record.Text, Tags: record.Tags,
			Confidence: record.Confidence, ContentHash: record.ContentHash,
			Revision: record.Revision, Status: record.Status, Freshness: record.Freshness,
			Sensitivity: record.Sensitivity, Pinned: record.Pinned,
			SourceKind: record.SourceKind, SourceHash: record.SourceHash,
			SourceRevision: record.SourceRevision,
			ValidFrom:      record.ValidFrom.UTC(), ValidUntil: record.ValidUntil.UTC(),
			RevalidateAfter: record.RevalidateAfter.UTC(), LastVerifiedAt: record.LastVerifiedAt.UTC(),
			StaleReason: record.StaleReason, DeletedAt: record.DeletedAt.UTC(),
		})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		// Unrepresentable input must disable reuse, never collapse unrelated
		// inputs onto the hash of an empty/partial serialization.
		return ""
	}
	sum := sha256.Sum256(body)
	return "v1:" + hex.EncodeToString(sum[:])
}

func (c *Consolidator) reusableSummary(ctx context.Context, input preparedProjectSummary) (*memorystore.Summary, error) {
	if input.fingerprint == "" || len(input.ids) == 0 {
		return nil, nil
	}
	// ActiveSummary alone is insufficient: freshness can lag source expiry,
	// and the source links/watermark may no longer satisfy the injection gate.
	summary, err := c.Store.GatedActiveSummary(ctx, c.now())
	if err != nil || summary == nil {
		return nil, err
	}
	fingerprint, _ := summary.Metadata[summaryInputFingerprintKey].(string)
	if fingerprint != input.fingerprint || summary.ID == "" ||
		summary.Status != "active" || summary.Freshness != memorystore.FreshnessFresh ||
		summary.Scope != memorystore.ScopeProject || summary.ProjectKey != c.Store.ProjectKey() ||
		summary.SummaryKind != "long_term" || summary.InputWatermark == "" ||
		summary.SourceCount != len(input.ids) || !slices.Equal(summary.RecordIDs, input.ids) {
		return nil, nil
	}
	if strings.TrimSpace(summary.Text) == "" || projectSummaryValidationError(summary.Text) != "" ||
		memoryredact.DetectSecret(summary.Text) != "" {
		return nil, nil
	}
	sum := sha256.Sum256([]byte(summary.Text))
	tokens := memorystore.EstimateTokens(summary.Text)
	if summary.ContentHash != hex.EncodeToString(sum[:]) || tokens > input.budget ||
		summary.TokenEstimate != tokens {
		return nil, nil
	}
	compiler, _ := summary.Metadata["compiler"].(string)
	if c.Config.Summarizer == nil {
		if compiler != "deterministic_v1" {
			return nil, nil
		}
	} else if compiler != "llm_v1" {
		// A transient model failure must not pin its fail-open deterministic
		// fallback forever. A later bucket still gets a chance to recover.
		return nil, nil
	}
	return summary, nil
}
