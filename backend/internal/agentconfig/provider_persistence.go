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
	Name               string
	SetDefaultProvider bool
	Enabled            *bool
	Protocol           *string
	BaseURL            *string
	APIKey             *string
	APIKeyRef          *string
	AuthMode           *string
	AuthRef            *string
	ModelsPath         *string
	ModelsVerifiedAt   *string
	SupportedModels    *[]string
	DefaultModel       *string
	ModelCapabilities  *map[string]ModelCapabilitySpec
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
	upsertRequiredStringYAMLValue(node, "base_url", update.BaseURL)
	upsertRequiredStringYAMLValue(node, "default_model", update.DefaultModel)
	upsertOptionalStringYAMLValue(node, "api_key", update.APIKey)
	upsertOptionalStringYAMLValue(node, "api_key_ref", update.APIKeyRef)
	upsertOptionalStringYAMLValue(node, "auth_mode", update.AuthMode)
	upsertOptionalStringYAMLValue(node, "auth_ref", update.AuthRef)
	upsertOptionalStringYAMLValue(node, "models_path", update.ModelsPath)
	upsertOptionalStringYAMLValue(node, "models_verified_at", update.ModelsVerifiedAt)
	if update.SupportedModels != nil {
		upsertYAMLMappingValue(node, "supported_models", stringSliceYAMLNode(*update.SupportedModels))
	}
	if update.ModelCapabilities != nil {
		if len(*update.ModelCapabilities) == 0 {
			removeYAMLMappingValue(node, "model_capabilities")
		} else {
			upsertYAMLMappingValue(node, "model_capabilities", modelCapabilitiesYAMLNode(*update.ModelCapabilities))
		}
	}
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
		if trimmed != "" && value > 0 {
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
