package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProviderConfigUpdate describes a partial update to providers.items.<name>.
// Nil fields are not touched, allowing callers to preserve unrelated provider keys.
type ProviderConfigUpdate struct {
	Name                 string
	SetDefaultProvider   bool
	Enabled              *bool
	Protocol             *string
	CompatibilityProfile *string
	BaseURL              *string
	APIPath              *string
	ForwardURL           *string
	APIKey               *string
	APIKeyRef            *string
	AuthMode             *string
	AuthRef              *string
	// APIKeys writes the api_keys pool. Nil leaves the pool untouched;
	// a non-nil empty slice removes it.
	APIKeys *[]string
	// Proxy writes the provider-level proxy override (providers.items.<name>.proxy).
	// Nil leaves the proxy node untouched.
	Proxy *ProxyConfig
	// ClearProxy removes the provider-level proxy node entirely.
	ClearProxy            bool
	ModelsPath            *string
	ModelsVerifiedAt      *string
	SupportedModels       *[]string
	DefaultModel          *string
	SupportTypes          *[]string
	MaxTokensLimit        *int
	EnableImageGeneration *bool
	ModelCapabilities     *map[string]ModelCapabilitySpec

	// Site / account snapshot fields.
	SiteType           *string
	SiteTypeConfidence *string
	SiteTypeDetectedAt *string
	SiteTypeScores     *map[string]int
	AccountAuthRef     *string
	// Account is written when non-nil. Use ClearAccount to remove the account node.
	Account      *ProviderAccountSnapshot
	ClearAccount bool
}

// UpdateProviderConfig updates one provider node without rewriting unrelated config sections.
func UpdateProviderConfig(configPath string, update ProviderConfigUpdate) (*Provider, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}
	update.Name = strings.TrimSpace(update.Name)
	if update.Name == "" {
		return nil, fmt.Errorf("provider name is required")
	}

	raw, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config file %s: %w", configPath, err)
	}
	if os.IsNotExist(err) {
		if _, _, starterErr := EnsureStarterConfigAtPath(configPath); starterErr != nil {
			return nil, starterErr
		}
		raw, err = os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("read starter config file %s: %w", configPath, err)
		}
	}

	document, err := parseYAMLDocument(raw)
	if err != nil {
		return nil, err
	}
	root, err := ensureYAMLRootMapping(document)
	if err != nil {
		return nil, err
	}

	providersNode := ensureChildMapping(root, "providers")
	itemsNode := ensureChildMapping(providersNode, "items")
	providersNode.Style = 0
	itemsNode.Style = 0
	providerNode := mappingValue(itemsNode, update.Name)
	if providerNode == nil || providerNode.Kind != yaml.MappingNode {
		providerNode = &yaml.Node{Kind: yaml.MappingNode}
		upsertYAMLMappingValue(itemsNode, update.Name, providerNode)
	}
	providerNode.Style = 0

	applyProviderConfigYAMLUpdate(providerNode, update)
	if update.SetDefaultProvider {
		upsertYAMLMappingValue(providersNode, "default_provider", stringYAMLNode(update.Name))
	}

	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("encode config yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("finalize config yaml: %w", err)
	}
	if err := writeFileAtomic(configPath, output.Bytes()); err != nil {
		return nil, err
	}

	updated := &Provider{}
	if err := decodeYAMLNode(providerNode, updated); err != nil {
		return nil, fmt.Errorf("decode updated provider %s: %w", update.Name, err)
	}
	return updated, nil
}

func applyProviderConfigYAMLUpdate(node *yaml.Node, update ProviderConfigUpdate) {
	if update.Enabled != nil {
		upsertYAMLMappingValue(node, "enabled", boolYAMLNode(*update.Enabled))
	}
	upsertRequiredStringYAMLValue(node, "protocol", update.Protocol)
	if update.CompatibilityProfile != nil {
		profile := strings.TrimSpace(*update.CompatibilityProfile)
		if profile == "" {
			removeYAMLMappingValue(node, "compatibility")
		} else {
			compatibility := ensureChildMapping(node, "compatibility")
			upsertYAMLMappingValue(compatibility, "profile", stringYAMLNode(profile))
		}
	}
	upsertRequiredStringYAMLValue(node, "base_url", update.BaseURL)
	upsertOptionalStringYAMLValue(node, "api_path", update.APIPath)
	upsertOptionalStringYAMLValue(node, "forward_url", update.ForwardURL)
	upsertRequiredStringYAMLValue(node, "default_model", update.DefaultModel)
	upsertOptionalStringYAMLValue(node, "api_key", update.APIKey)
	upsertOptionalStringYAMLValue(node, "api_key_ref", update.APIKeyRef)
	upsertOptionalStringYAMLValue(node, "auth_mode", update.AuthMode)
	upsertOptionalStringYAMLValue(node, "auth_ref", update.AuthRef)
	if update.APIKeys != nil {
		if len(*update.APIKeys) == 0 {
			removeYAMLMappingValue(node, "api_keys")
		} else {
			upsertYAMLMappingValue(node, "api_keys", stringSliceYAMLNode(*update.APIKeys))
		}
	}
	if update.ClearProxy {
		removeYAMLMappingValue(node, "proxy")
	} else if update.Proxy != nil {
		upsertYAMLMappingValue(node, "proxy", providerProxyYAMLNode(*update.Proxy))
	}
	upsertOptionalStringYAMLValue(node, "models_path", update.ModelsPath)
	upsertOptionalStringYAMLValue(node, "models_verified_at", update.ModelsVerifiedAt)
	if update.SupportedModels != nil {
		upsertYAMLMappingValue(node, "supported_models", stringSliceYAMLNode(*update.SupportedModels))
	}
	if update.SupportTypes != nil {
		if len(*update.SupportTypes) == 0 {
			removeYAMLMappingValue(node, "support_types")
		} else {
			upsertYAMLMappingValue(node, "support_types", stringSliceYAMLNode(*update.SupportTypes))
		}
	}
	if update.MaxTokensLimit != nil {
		if *update.MaxTokensLimit <= 0 {
			removeYAMLMappingValue(node, "max_tokens_limit")
		} else {
			upsertYAMLMappingValue(node, "max_tokens_limit", intYAMLNode(*update.MaxTokensLimit))
		}
	}
	if update.EnableImageGeneration != nil {
		upsertYAMLMappingValue(node, "enable_image_generation", boolYAMLNode(*update.EnableImageGeneration))
	}
	if update.ModelCapabilities != nil {
		if len(*update.ModelCapabilities) == 0 {
			removeYAMLMappingValue(node, "model_capabilities")
		} else {
			upsertYAMLMappingValue(node, "model_capabilities", modelCapabilitiesYAMLNode(*update.ModelCapabilities))
		}
	}
	upsertOptionalStringYAMLValue(node, "site_type", update.SiteType)
	upsertOptionalStringYAMLValue(node, "site_type_confidence", update.SiteTypeConfidence)
	upsertOptionalStringYAMLValue(node, "site_type_detected_at", update.SiteTypeDetectedAt)
	if update.SiteTypeScores != nil {
		if len(*update.SiteTypeScores) == 0 {
			removeYAMLMappingValue(node, "site_type_scores")
		} else {
			upsertYAMLMappingValue(node, "site_type_scores", intMapYAMLNode(*update.SiteTypeScores))
		}
	}
	upsertOptionalStringYAMLValue(node, "account_auth_ref", update.AccountAuthRef)
	if update.ClearAccount {
		removeYAMLMappingValue(node, "account")
	} else if update.Account != nil {
		upsertYAMLMappingValue(node, "account", providerAccountSnapshotYAMLNode(*update.Account))
	}
}

// providerProxyYAMLNode builds a providers.items.<name>.proxy YAML node.
// Enabled is always written explicitly so that a disabled proxy (e.g. after
// --disable) overrides a previously enabled one and the global providers.proxy.
func providerProxyYAMLNode(proxy ProxyConfig) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	upsertNonZeroStringYAMLValue(node, "http", proxy.HTTP)
	upsertNonZeroStringYAMLValue(node, "https", proxy.HTTPS)
	upsertNonZeroStringYAMLValue(node, "no_proxy", proxy.NoProxy)
	upsertYAMLMappingValue(node, "enabled", boolYAMLNode(proxy.Enabled))
	return node
}

func providerAccountSnapshotYAMLNode(snapshot ProviderAccountSnapshot) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	upsertNonZeroStringYAMLValue(node, "source", snapshot.Source)
	upsertNonZeroStringYAMLValue(node, "mode", snapshot.Mode)
	upsertNonZeroStringYAMLValue(node, "currency", snapshot.Currency)
	upsertOptionalFloatYAMLValue(node, "wallet_balance", snapshot.WalletBalance)
	if snapshot.IsAvailable != nil {
		upsertYAMLMappingValue(node, "is_available", boolYAMLNode(*snapshot.IsAvailable))
	}
	if len(snapshot.BalanceDetails) > 0 {
		upsertYAMLMappingValue(node, "balance_details", providerBalanceDetailsYAMLNode(snapshot.BalanceDetails))
	}
	upsertOptionalFloatYAMLValue(node, "quota_balance", snapshot.QuotaBalance)
	upsertOptionalFloatYAMLValue(node, "quota_remaining", snapshot.QuotaRemaining)
	upsertOptionalFloatYAMLValue(node, "quota_used", snapshot.QuotaUsed)
	upsertOptionalFloatYAMLValue(node, "quota_limit", snapshot.QuotaLimit)
	upsertNonZeroStringYAMLValue(node, "quota_display_type", snapshot.QuotaDisplayType)
	upsertNonZeroStringYAMLValue(node, "quota_display_unit", snapshot.QuotaDisplayUnit)
	upsertOptionalFloatYAMLValue(node, "quota_display_scale", snapshot.QuotaDisplayScale)
	upsertNonZeroStringYAMLValue(node, "plan_name", snapshot.PlanName)
	upsertNonZeroStringYAMLValue(node, "external_user_id", snapshot.ExternalUserID)
	upsertNonZeroStringYAMLValue(node, "external_username_masked", snapshot.ExternalUsernameMasked)
	if len(snapshot.Subscriptions) > 0 {
		upsertYAMLMappingValue(node, "subscriptions", providerAccountSubscriptionsYAMLNode(snapshot.Subscriptions))
	}
	if snapshot.Usage != nil {
		upsertYAMLMappingValue(node, "usage", providerAccountUsageYAMLNode(*snapshot.Usage))
	}
	upsertNonZeroStringYAMLValue(node, "fetched_at", snapshot.FetchedAt)
	if snapshot.Partial {
		upsertYAMLMappingValue(node, "partial", boolYAMLNode(true))
	}
	upsertNonZeroStringYAMLValue(node, "last_error", snapshot.LastError)
	return node
}

func providerBalanceDetailsYAMLNode(items []ProviderBalanceDetail) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode}
	for _, item := range items {
		entry := &yaml.Node{Kind: yaml.MappingNode}
		upsertNonZeroStringYAMLValue(entry, "currency", item.Currency)
		upsertYAMLMappingValue(entry, "total_balance", floatYAMLNode(item.TotalBalance))
		upsertYAMLMappingValue(entry, "granted_balance", floatYAMLNode(item.GrantedBalance))
		upsertYAMLMappingValue(entry, "topped_up_balance", floatYAMLNode(item.ToppedUpBalance))
		node.Content = append(node.Content, entry)
	}
	return node
}

func providerAccountSubscriptionsYAMLNode(items []ProviderAccountSubscription) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode}
	for _, item := range items {
		entry := &yaml.Node{Kind: yaml.MappingNode}
		upsertNonZeroStringYAMLValue(entry, "name", item.Name)
		upsertNonZeroStringYAMLValue(entry, "status", item.Status)
		upsertOptionalFloatYAMLValue(entry, "remaining", item.Remaining)
		upsertNonZeroStringYAMLValue(entry, "period_end", item.PeriodEnd)
		node.Content = append(node.Content, entry)
	}
	return node
}

func providerAccountUsageYAMLNode(usage ProviderAccountUsage) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	upsertOptionalInt64YAMLValue(node, "total_requests", usage.TotalRequests)
	upsertOptionalFloatYAMLValue(node, "total_cost", usage.TotalCost)
	upsertOptionalInt64YAMLValue(node, "today_requests", usage.TodayRequests)
	upsertOptionalFloatYAMLValue(node, "today_cost", usage.TodayCost)
	return node
}

func upsertOptionalFloatYAMLValue(node *yaml.Node, key string, value *float64) {
	if value == nil {
		return
	}
	upsertYAMLMappingValue(node, key, floatYAMLNode(*value))
}

func upsertOptionalInt64YAMLValue(node *yaml.Node, key string, value *int64) {
	if value == nil {
		return
	}
	upsertYAMLMappingValue(node, key, intYAMLNode(int(*value)))
}

func ensureChildMapping(parent *yaml.Node, key string) *yaml.Node {
	child := mappingValue(parent, key)
	if child == nil || child.Kind != yaml.MappingNode {
		child = &yaml.Node{Kind: yaml.MappingNode}
		upsertYAMLMappingValue(parent, key, child)
	}
	child.Style = 0
	return child
}

func upsertRequiredStringYAMLValue(node *yaml.Node, key string, value *string) {
	if value == nil {
		return
	}
	upsertYAMLMappingValue(node, key, stringYAMLNode(strings.TrimSpace(*value)))
}

func upsertOptionalStringYAMLValue(node *yaml.Node, key string, value *string) {
	if value == nil {
		return
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		removeYAMLMappingValue(node, key)
		return
	}
	upsertYAMLMappingValue(node, key, stringYAMLNode(trimmed))
}

func removeYAMLMappingValue(root *yaml.Node, key string) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Kind == yaml.ScalarNode && root.Content[i].Value == key {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			return
		}
	}
}

func stringYAMLNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func boolYAMLNode(value bool) *yaml.Node {
	if value {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}
}

func stringSliceYAMLNode(values []string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		node.Content = append(node.Content, stringYAMLNode(value))
	}
	return node
}

func intYAMLNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

func floatYAMLNode(value float64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(value, 'f', -1, 64)}
}

func intMapYAMLNode(values map[string]int) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	keys := make([]string, 0, len(values))
	normalized := make(map[string]int, len(values))
	for key, value := range values {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" && value >= 0 {
			if _, exists := normalized[trimmed]; !exists {
				keys = append(keys, trimmed)
			}
			normalized[trimmed] = value
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		node.Content = append(node.Content, stringYAMLNode(key), intYAMLNode(normalized[key]))
	}
	return node
}

func modelCapabilitiesYAMLNode(capabilities map[string]ModelCapabilitySpec) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	keys := make([]string, 0, len(capabilities))
	normalized := make(map[string]ModelCapabilitySpec, len(capabilities))
	for key, spec := range capabilities {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" && !modelCapabilitySpecIsEmpty(spec) {
			if _, exists := normalized[trimmed]; !exists {
				keys = append(keys, trimmed)
			}
			normalized[trimmed] = spec
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		node.Content = append(node.Content, stringYAMLNode(key), modelCapabilitySpecYAMLNode(normalized[key]))
	}
	return node
}

func modelCapabilitySpecYAMLNode(spec ModelCapabilitySpec) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	if len(spec.InputModalities) > 0 {
		upsertYAMLMappingValue(node, "input_modalities", stringSliceYAMLNode(spec.InputModalities))
	}
	if spec.NativeTools.ImageGeneration || spec.NativeTools.ImagesGenerationsAPI {
		tools := &yaml.Node{Kind: yaml.MappingNode}
		if spec.NativeTools.ImageGeneration {
			upsertYAMLMappingValue(tools, "image_generation", boolYAMLNode(true))
		}
		if spec.NativeTools.ImagesGenerationsAPI {
			upsertYAMLMappingValue(tools, "images_generations_api", boolYAMLNode(true))
		}
		upsertYAMLMappingValue(node, "native_tools", tools)
	}
	if spec.ReasoningModel {
		upsertYAMLMappingValue(node, "reasoning_model", boolYAMLNode(true))
	}
	if len(spec.ReasoningEfforts) > 0 {
		upsertYAMLMappingValue(node, "reasoning_efforts", stringSliceYAMLNode(spec.ReasoningEfforts))
	}
	if len(spec.ReasoningEffortBudgets) > 0 {
		upsertYAMLMappingValue(node, "reasoning_effort_budgets", intMapYAMLNode(spec.ReasoningEffortBudgets))
	}
	upsertNonZeroStringYAMLValue(node, "default_reasoning_effort", spec.DefaultReasoningEffort)
	upsertNonZeroIntYAMLValue(node, "max_context_tokens", spec.MaxContextTokens)
	upsertNonZeroIntYAMLValue(node, "max_tokens", spec.MaxTokens)
	if spec.AutoCompactRatio > 0 {
		upsertYAMLMappingValue(node, "auto_compact_ratio", floatYAMLNode(spec.AutoCompactRatio))
	}
	upsertNonZeroIntYAMLValue(node, "auto_compact_token_limit", spec.AutoCompactTokenLimit)
	upsertNonZeroStringYAMLValue(node, "auto_compact_mode", spec.AutoCompactMode)
	if spec.SupportsRemoteCompact {
		upsertYAMLMappingValue(node, "supports_remote_compact", boolYAMLNode(true))
	}
	upsertNonZeroStringYAMLValue(node, "compact_reasoning_effort", spec.CompactReasoningEffort)
	return node
}

func modelCapabilitySpecIsEmpty(spec ModelCapabilitySpec) bool {
	return len(spec.InputModalities) == 0 &&
		!spec.NativeTools.ImageGeneration &&
		!spec.NativeTools.ImagesGenerationsAPI &&
		!spec.ReasoningModel &&
		len(spec.ReasoningEfforts) == 0 &&
		len(spec.ReasoningEffortBudgets) == 0 &&
		strings.TrimSpace(spec.DefaultReasoningEffort) == "" &&
		spec.MaxContextTokens == 0 &&
		spec.MaxTokens == 0 &&
		spec.AutoCompactRatio == 0 &&
		spec.AutoCompactTokenLimit == 0 &&
		strings.TrimSpace(spec.AutoCompactMode) == "" &&
		!spec.SupportsRemoteCompact &&
		strings.TrimSpace(spec.CompactReasoningEffort) == ""
}

func upsertNonZeroStringYAMLValue(node *yaml.Node, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	upsertYAMLMappingValue(node, key, stringYAMLNode(strings.TrimSpace(value)))
}

func upsertNonZeroIntYAMLValue(node *yaml.Node, key string, value int) {
	if value <= 0 {
		return
	}
	upsertYAMLMappingValue(node, key, intYAMLNode(value))
}
