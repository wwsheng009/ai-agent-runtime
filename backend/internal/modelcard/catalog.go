package modelcard

import (
	"fmt"
	"sort"
	"strings"

	configassets "github.com/wwsheng009/ai-agent-runtime/configs"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"gopkg.in/yaml.v3"
)

const BuiltinSourceName = "embedded:model_cards.yaml"

type Source struct {
	Name string
	Data []byte
	Err  error
}

type Warning struct {
	Source  string `json:"source,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Catalog struct {
	Version           int                `yaml:"version" json:"version"`
	ProviderTemplates []ProviderTemplate `yaml:"provider_templates" json:"provider_templates,omitempty"`
	Cards             []Card             `yaml:"cards" json:"cards"`
}

type Card struct {
	ID               string                          `yaml:"id" json:"id"`
	Title            string                          `yaml:"title,omitempty" json:"title,omitempty"`
	Priority         int                             `yaml:"priority,omitempty" json:"priority,omitempty"`
	Fallback         bool                            `yaml:"fallback,omitempty" json:"fallback,omitempty"`
	ProviderTemplate string                          `yaml:"provider_template,omitempty" json:"provider_template,omitempty"`
	Match            MatchSpec                       `yaml:"match" json:"match"`
	Capability       agentconfig.ModelCapabilitySpec `yaml:"capability" json:"capability"`
}

type ProviderTemplate struct {
	ID             string   `yaml:"id" json:"id"`
	Protocol       string   `yaml:"protocol" json:"protocol"`
	APIPath        string   `yaml:"api_path,omitempty" json:"api_path,omitempty"`
	ForwardURL     string   `yaml:"forward_url,omitempty" json:"forward_url,omitempty"`
	SupportTypes   []string `yaml:"support_types,omitempty" json:"support_types,omitempty"`
	MaxTokensLimit int      `yaml:"max_tokens_limit,omitempty" json:"max_tokens_limit,omitempty"`
}

type MatchSpec struct {
	ModelIDs        []string `yaml:"model_ids" json:"model_ids,omitempty"`
	Aliases         []string `yaml:"aliases" json:"aliases,omitempty"`
	ModelPatterns   []string `yaml:"model_patterns" json:"model_patterns,omitempty"`
	Protocols       []string `yaml:"protocols" json:"protocols,omitempty"`
	ProviderNames   []string `yaml:"provider_names" json:"provider_names,omitempty"`
	BaseURLContains []string `yaml:"base_url_contains" json:"base_url_contains,omitempty"`
}

type Context struct {
	ProviderName     string
	LoginProtocol    string
	RuntimeProtocol  string
	ProviderTemplate string
	BaseURL          string
}

type AppliedCard struct {
	CardID           string   `json:"card_id"`
	ProviderTemplate string   `json:"provider_template,omitempty"`
	Fields           []string `json:"fields,omitempty"`
	Score            int      `json:"-"`
	Fallback         bool     `json:"-"`
}

func BuiltinSource() Source {
	return Source{Name: BuiltinSourceName, Data: configassets.BuiltinModelCardsYAML}
}

func LoadSources(sources []Source, strict bool) (*Catalog, []Warning, error) {
	merged := &Catalog{Version: 1}
	var warnings []Warning
	for _, source := range sources {
		name := strings.TrimSpace(source.Name)
		if name == "" {
			name = "model_cards.yaml"
		}
		if source.Err != nil {
			err := fmt.Errorf("read model card catalog %s: %w", name, source.Err)
			if strict {
				return nil, warnings, err
			}
			warnings = append(warnings, Warning{Source: name, Code: "read_failed", Message: err.Error()})
			continue
		}
		if len(source.Data) == 0 {
			continue
		}
		catalog, err := parseSource(source)
		if err != nil {
			if strict {
				return nil, warnings, err
			}
			warnings = append(warnings, Warning{Source: name, Code: "parse_failed", Message: err.Error()})
			continue
		}
		if catalog.Version != 1 {
			err := fmt.Errorf("unsupported version %d", catalog.Version)
			wrapped := fmt.Errorf("validate model card catalog %s: %w", name, err)
			if strict {
				return nil, warnings, wrapped
			}
			warnings = append(warnings, Warning{Source: name, Code: "validate_failed", Message: wrapped.Error()})
			continue
		}
		candidate := &Catalog{
			Version:           1,
			ProviderTemplates: mergeProviderTemplates(merged.ProviderTemplates, catalog.ProviderTemplates),
			Cards:             append(append([]Card(nil), merged.Cards...), catalog.Cards...),
		}
		if err := candidate.Validate(); err != nil {
			wrapped := fmt.Errorf("validate model card catalog %s: %w", name, err)
			if strict {
				return nil, warnings, wrapped
			}
			warnings = append(warnings, Warning{Source: name, Code: "validate_failed", Message: wrapped.Error()})
			continue
		}
		merged = candidate
	}
	return merged, warnings, nil
}

func mergeProviderTemplates(base, updates []ProviderTemplate) []ProviderTemplate {
	out := make([]ProviderTemplate, 0, len(base)+len(updates))
	indexByID := make(map[string]int, len(base)+len(updates))
	add := func(template ProviderTemplate) {
		id := strings.TrimSpace(template.ID)
		if id == "" {
			out = append(out, cloneProviderTemplate(template))
			return
		}
		key := strings.ToLower(id)
		if index, ok := indexByID[key]; ok {
			out[index] = cloneProviderTemplate(template)
			return
		}
		indexByID[key] = len(out)
		out = append(out, cloneProviderTemplate(template))
	}
	for _, template := range base {
		add(template)
	}
	for _, template := range updates {
		add(template)
	}
	return out
}

func parseSource(source Source) (*Catalog, error) {
	catalog := &Catalog{}
	if err := yaml.Unmarshal(source.Data, catalog); err != nil {
		return nil, fmt.Errorf("parse model card catalog %s: %w", source.Name, err)
	}
	return catalog, nil
}

func (c *Catalog) Validate() error {
	if c == nil {
		return fmt.Errorf("catalog is nil")
	}
	if c.Version != 1 {
		return fmt.Errorf("unsupported version %d", c.Version)
	}
	templateIDs := make(map[string]struct{}, len(c.ProviderTemplates))
	for i, template := range c.ProviderTemplates {
		id := strings.TrimSpace(template.ID)
		if id == "" {
			return fmt.Errorf("provider_templates[%d].id is required", i)
		}
		protocol := strings.TrimSpace(template.Protocol)
		if protocol == "" {
			return fmt.Errorf("provider template %q protocol is required", id)
		}
		key := strings.ToLower(id)
		if _, exists := templateIDs[key]; exists {
			return fmt.Errorf("duplicate provider template id %q", id)
		}
		templateIDs[key] = struct{}{}
	}
	seen := make(map[string]struct{}, len(c.Cards))
	for i, card := range c.Cards {
		id := strings.TrimSpace(card.ID)
		if id == "" {
			return fmt.Errorf("cards[%d].id is required", i)
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate card id %q", id)
		}
		seen[key] = struct{}{}
		if card.Fallback && len(card.Match.Protocols) == 0 && strings.TrimSpace(card.ProviderTemplate) == "" {
			return fmt.Errorf("fallback card %q must declare match.protocols or provider_template", id)
		}
		if !card.Fallback && !card.Match.hasAnyModelMatcher() {
			return fmt.Errorf("card %q must declare model_ids, aliases, or model_patterns", id)
		}
		if capabilityIsEmpty(card.Capability) {
			return fmt.Errorf("card %q capability is empty", id)
		}
		if templateID := strings.TrimSpace(card.ProviderTemplate); templateID != "" {
			if _, exists := templateIDs[strings.ToLower(templateID)]; !exists {
				return fmt.Errorf("card %q references unknown provider_template %q", id, templateID)
			}
		}
	}
	return nil
}

func (m MatchSpec) hasAnyModelMatcher() bool {
	return len(m.ModelIDs) > 0 || len(m.Aliases) > 0 || len(m.ModelPatterns) > 0
}

func (c *Catalog) Resolve(ctx Context, modelID string) (agentconfig.ModelCapabilitySpec, []AppliedCard) {
	if c == nil || strings.TrimSpace(modelID) == "" {
		return agentconfig.ModelCapabilitySpec{}, nil
	}
	matches := make([]matchedCard, 0)
	for _, card := range c.Cards {
		if card.Fallback {
			continue
		}
		score, ok := cardMatchScore(ctx, modelID, card)
		if !ok {
			continue
		}
		matches = append(matches, matchedCard{Card: card, Score: score})
	}
	if len(matches) == 0 {
		matches = c.fallbackMatches(ctx)
	}
	sortMatchedCards(matches)

	var capability agentconfig.ModelCapabilitySpec
	applied := make([]AppliedCard, 0, len(matches))
	for _, match := range matches {
		fillCapabilityMissing(&capability, match.Card.Capability)
		applied = append(applied, AppliedCard{
			CardID:           strings.TrimSpace(match.Card.ID),
			ProviderTemplate: strings.TrimSpace(match.Card.ProviderTemplate),
			Fields:           CapabilityFieldNames(match.Card.Capability),
			Score:            match.Score,
			Fallback:         match.Card.Fallback,
		})
	}
	return capability, applied
}

func (c *Catalog) ProviderTemplate(id string) (ProviderTemplate, bool) {
	id = strings.TrimSpace(id)
	if c == nil || id == "" {
		return ProviderTemplate{}, false
	}
	for _, template := range c.ProviderTemplates {
		if strings.EqualFold(strings.TrimSpace(template.ID), id) {
			return cloneProviderTemplate(template), true
		}
	}
	return ProviderTemplate{}, false
}

func (c *Catalog) ProviderTemplateForProtocol(protocol string) (ProviderTemplate, bool) {
	protocol = strings.TrimSpace(protocol)
	if c == nil || protocol == "" {
		return ProviderTemplate{}, false
	}
	for _, template := range c.ProviderTemplates {
		if strings.EqualFold(strings.TrimSpace(template.Protocol), protocol) {
			return cloneProviderTemplate(template), true
		}
	}
	return ProviderTemplate{}, false
}

func (c *Catalog) ProviderTemplateList() []ProviderTemplate {
	if c == nil || len(c.ProviderTemplates) == 0 {
		return nil
	}
	out := make([]ProviderTemplate, 0, len(c.ProviderTemplates))
	for _, template := range c.ProviderTemplates {
		out = append(out, cloneProviderTemplate(template))
	}
	return out
}

func (c *Catalog) RecommendedProviderTemplate(ctx Context, modelID string) (ProviderTemplate, []AppliedCard, bool) {
	if c == nil || strings.TrimSpace(modelID) == "" {
		return ProviderTemplate{}, nil, false
	}
	matches := make([]matchedCard, 0)
	for _, card := range c.Cards {
		if card.Fallback {
			continue
		}
		if strings.TrimSpace(card.ProviderTemplate) == "" {
			continue
		}
		score, ok := cardRecommendationScore(ctx, modelID, card)
		if !ok {
			continue
		}
		matches = append(matches, matchedCard{Card: card, Score: score})
	}
	sortMatchedCards(matches)
	for _, match := range matches {
		template, ok := c.ProviderTemplate(match.Card.ProviderTemplate)
		if !ok {
			continue
		}
		applied := []AppliedCard{{
			CardID:           strings.TrimSpace(match.Card.ID),
			ProviderTemplate: strings.TrimSpace(match.Card.ProviderTemplate),
			Fields:           CapabilityFieldNames(match.Card.Capability),
			Score:            match.Score,
			Fallback:         match.Card.Fallback,
		}}
		return template, applied, true
	}
	for _, match := range c.fallbackMatches(ctx) {
		if strings.TrimSpace(match.Card.ProviderTemplate) == "" {
			continue
		}
		template, ok := c.ProviderTemplate(match.Card.ProviderTemplate)
		if !ok {
			continue
		}
		applied := []AppliedCard{{
			CardID:           strings.TrimSpace(match.Card.ID),
			ProviderTemplate: strings.TrimSpace(match.Card.ProviderTemplate),
			Fields:           CapabilityFieldNames(match.Card.Capability),
			Score:            match.Score,
			Fallback:         match.Card.Fallback,
		}}
		return template, applied, true
	}
	return ProviderTemplate{}, nil, false
}

func (c *Catalog) fallbackMatches(ctx Context) []matchedCard {
	if c == nil {
		return nil
	}
	matches := make([]matchedCard, 0)
	for _, card := range c.Cards {
		if !card.Fallback {
			continue
		}
		score, ok := fallbackCardMatchScore(ctx, card)
		if !ok {
			continue
		}
		matches = append(matches, matchedCard{Card: card, Score: score})
	}
	sortMatchedCards(matches)
	return matches
}

func sortMatchedCards(matches []matchedCard) {
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Card.Priority != matches[j].Card.Priority {
			return matches[i].Card.Priority > matches[j].Card.Priority
		}
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return strings.TrimSpace(matches[i].Card.ID) < strings.TrimSpace(matches[j].Card.ID)
	})
}

func cloneProviderTemplate(template ProviderTemplate) ProviderTemplate {
	if len(template.SupportTypes) > 0 {
		template.SupportTypes = append([]string(nil), template.SupportTypes...)
	}
	return template
}

type matchedCard struct {
	Card  Card
	Score int
}
