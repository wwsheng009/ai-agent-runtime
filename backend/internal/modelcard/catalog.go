package modelcard

import (
	"fmt"
	"sort"
	"strings"

	configassets "github.com/wwsheng009/ai-agent-runtime/configs"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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
	// layerRank 记录贡献过该卡片的最高层序号（0 = 最低层），由 LoadSources
	// 的分层合并回填；只用于同优先级/同匹配分卡片的层序兜底，不参与序列化。
	layerRank int
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

type RecommendedProviderTemplateMatch struct {
	Template ProviderTemplate
	Applied  []AppliedCard
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

// LoadSources 按"低层在前、高层在后"的顺序合并多个目录来源。
//
// 合并维度见 layers.go：同 id（provider_templates / cards）字段级合并、高层
// 覆盖同名标量与数组；不同 id 并集保留，由 Resolve 按 priority → 匹配分 →
// 层序做逐字段补齐。
//
// strict=false 时单个来源的读取 / 解析 / 校验失败只记 warning 并跳过该层，
// 已合并的低层结果不受影响；strict=true 时直接返回错误。
func LoadSources(sources []Source, strict bool) (*Catalog, []Warning, error) {
	accumulated := newLayerCatalog()
	var warnings []Warning
	for rank, source := range sources {
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
		doc, err := parseLayerSource(name, source.Data)
		if err != nil {
			if strict {
				return nil, warnings, err
			}
			warnings = append(warnings, Warning{Source: name, Code: "parse_failed", Message: err.Error()})
			continue
		}
		if doc.Version != 1 {
			err := fmt.Errorf("validate model card catalog %s: unsupported version %d", name, doc.Version)
			if strict {
				return nil, warnings, err
			}
			warnings = append(warnings, Warning{Source: name, Code: "validate_failed", Message: err.Error()})
			continue
		}
		next := accumulated.clone()
		if err := next.mergeSource(doc, rank); err != nil {
			wrapped := fmt.Errorf("validate model card catalog %s: %w", name, err)
			if strict {
				return nil, warnings, wrapped
			}
			warnings = append(warnings, Warning{Source: name, Code: "validate_failed", Message: wrapped.Error()})
			continue
		}
		if _, err := next.catalog(); err != nil {
			wrapped := fmt.Errorf("validate model card catalog %s: %w", name, err)
			if strict {
				return nil, warnings, wrapped
			}
			warnings = append(warnings, Warning{Source: name, Code: "validate_failed", Message: wrapped.Error()})
			continue
		}
		accumulated = next
	}
	merged, err := accumulated.catalog()
	if err != nil {
		// 空累积目录本身合法（version=1、无条目）；走到这里说明内部状态异常。
		wrapped := fmt.Errorf("validate model card catalog: %w", err)
		if strict {
			return nil, warnings, wrapped
		}
		warnings = append(warnings, Warning{Source: "model_cards.yaml", Code: "validate_failed", Message: wrapped.Error()})
		return &Catalog{Version: 1}, warnings, nil
	}
	return merged, warnings, nil
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

func (c *Catalog) RecommendedProviderTemplates(ctx Context, modelID string) []RecommendedProviderTemplateMatch {
	if c == nil || strings.TrimSpace(modelID) == "" {
		return nil
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

	out := make([]RecommendedProviderTemplateMatch, 0)
	seenTemplates := make(map[string]struct{})
	for _, match := range matches {
		templateID := strings.ToLower(strings.TrimSpace(match.Card.ProviderTemplate))
		if templateID == "" {
			continue
		}
		if _, seen := seenTemplates[templateID]; seen {
			continue
		}
		template, ok := c.ProviderTemplate(match.Card.ProviderTemplate)
		if !ok {
			continue
		}
		seenTemplates[templateID] = struct{}{}
		out = append(out, RecommendedProviderTemplateMatch{
			Template: template,
			Applied: []AppliedCard{{
				CardID:           strings.TrimSpace(match.Card.ID),
				ProviderTemplate: strings.TrimSpace(match.Card.ProviderTemplate),
				Fields:           CapabilityFieldNames(match.Card.Capability),
				Score:            match.Score,
				Fallback:         match.Card.Fallback,
			}},
		})
	}
	if len(out) > 0 {
		return out
	}
	for _, match := range c.fallbackMatches(ctx) {
		if strings.TrimSpace(match.Card.ProviderTemplate) == "" {
			continue
		}
		template, ok := c.ProviderTemplate(match.Card.ProviderTemplate)
		if !ok {
			continue
		}
		return []RecommendedProviderTemplateMatch{{
			Template: template,
			Applied: []AppliedCard{{
				CardID:           strings.TrimSpace(match.Card.ID),
				ProviderTemplate: strings.TrimSpace(match.Card.ProviderTemplate),
				Fields:           CapabilityFieldNames(match.Card.Capability),
				Score:            match.Score,
				Fallback:         match.Card.Fallback,
			}},
		}}
	}
	return nil
}

func (c *Catalog) RecommendedProviderTemplate(ctx Context, modelID string) (ProviderTemplate, []AppliedCard, bool) {
	matches := c.RecommendedProviderTemplates(ctx, modelID)
	if len(matches) == 0 {
		return ProviderTemplate{}, nil, false
	}
	return matches[0].Template, matches[0].Applied, true
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
		if matches[i].Card.layerRank != matches[j].Card.layerRank {
			return matches[i].Card.layerRank > matches[j].Card.layerRank
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
