package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type ProviderListFilter struct {
	Protocol string
	Enabled  *bool
}

type ProviderSummary struct {
	Name                 string   `json:"name"`
	Enabled              bool     `json:"enabled"`
	Default              bool     `json:"default"`
	Protocol             string   `json:"protocol,omitempty"`
	AuthMode             string   `json:"auth_mode,omitempty"`
	HasAPIKey            bool     `json:"has_api_key"`
	HasAPIKeyRef         bool     `json:"has_api_key_ref"`
	APIKeyRef            string   `json:"api_key_ref,omitempty"`
	HasAuthRef           bool     `json:"has_auth_ref"`
	AuthRef              string   `json:"auth_ref,omitempty"`
	BaseURL              string   `json:"base_url,omitempty"`
	APIPath              string   `json:"api_path,omitempty"`
	ForwardURL           string   `json:"forward_url,omitempty"`
	DefaultModel         string   `json:"default_model,omitempty"`
	SupportedModelsCount int      `json:"supported_models_count"`
	ModelsVerifiedAt     string   `json:"models_verified_at,omitempty"`
	Groups               []string `json:"groups,omitempty"`
}

type ProviderDeleteRequest struct {
	Names              []string
	Cascade            bool
	ClearDefault       bool
	ReplacementDefault string
	PruneAuth          bool
	DryRun             bool
	AuthStorePath      string
}

type ProviderDeleteBlocker struct {
	Provider   string   `json:"provider,omitempty"`
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	References []string `json:"references,omitempty"`
}

type ProviderGroupReference struct {
	Group    string `json:"group"`
	Provider string `json:"provider"`
}

type ProviderAuthPruneSkip struct {
	Ref       string   `json:"ref"`
	Reason    string   `json:"reason"`
	Providers []string `json:"providers,omitempty"`
}

type ProviderDeleteResult struct {
	ConfigPath         string                   `json:"config_path,omitempty"`
	DryRun             bool                     `json:"dry_run"`
	Requested          []string                 `json:"requested"`
	Deleted            []string                 `json:"deleted,omitempty"`
	NotFound           []string                 `json:"not_found,omitempty"`
	Blocked            []ProviderDeleteBlocker  `json:"blocked,omitempty"`
	RemovedGroupRefs   []ProviderGroupReference `json:"removed_group_refs,omitempty"`
	RemovedGroups      []string                 `json:"removed_groups,omitempty"`
	ClearedDefaults    []string                 `json:"cleared_defaults,omitempty"`
	ReplacementDefault string                   `json:"replacement_default,omitempty"`
	AuthPruned         []string                 `json:"auth_pruned,omitempty"`
	AuthSkipped        []ProviderAuthPruneSkip  `json:"auth_skipped,omitempty"`
}

type ProviderEnableResult struct {
	ConfigPath string   `json:"config_path,omitempty"`
	Enabled    bool     `json:"enabled"`
	Updated    []string `json:"updated,omitempty"`
	NotFound   []string `json:"not_found,omitempty"`
}

type ProviderDefaultResult struct {
	ConfigPath      string `json:"config_path,omitempty"`
	DefaultProvider string `json:"default_provider"`
	PreviousDefault string `json:"previous_default,omitempty"`
}

func ListProviderSummaries(cfg *Config, filter ProviderListFilter) []ProviderSummary {
	if cfg == nil || cfg.Providers.Items == nil {
		return nil
	}
	groupNames := providerGroupNamesByProvider(cfg.ProviderGroups)
	names := make([]string, 0, len(cfg.Providers.Items))
	for name := range cfg.Providers.Items {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ProviderSummary, 0, len(names))
	for _, name := range names {
		provider := cfg.Providers.Items[name]
		if strings.TrimSpace(filter.Protocol) != "" && !strings.EqualFold(provider.GetProtocol(), filter.Protocol) {
			continue
		}
		if filter.Enabled != nil && provider.Enabled != *filter.Enabled {
			continue
		}
		out = append(out, ProviderSummary{
			Name:                 name,
			Enabled:              provider.Enabled,
			Default:              strings.EqualFold(strings.TrimSpace(cfg.Providers.DefaultProvider), name),
			Protocol:             provider.GetProtocol(),
			AuthMode:             strings.TrimSpace(provider.AuthMode),
			HasAPIKey:            strings.TrimSpace(provider.APIKey) != "" || len(provider.APIKeys) > 0,
			HasAPIKeyRef:         strings.TrimSpace(provider.APIKeyRef) != "",
			APIKeyRef:            strings.TrimSpace(provider.APIKeyRef),
			HasAuthRef:           strings.TrimSpace(provider.AuthRef) != "",
			AuthRef:              strings.TrimSpace(provider.AuthRef),
			BaseURL:              strings.TrimSpace(provider.BaseURL),
			APIPath:              strings.TrimSpace(provider.APIPath),
			ForwardURL:           strings.TrimSpace(provider.ForwardURL),
			DefaultModel:         strings.TrimSpace(provider.DefaultModel),
			SupportedModelsCount: len(provider.SupportedModels),
			ModelsVerifiedAt:     strings.TrimSpace(provider.ModelsVerifiedAt),
			Groups:               append([]string(nil), groupNames[strings.ToLower(name)]...),
		})
	}
	return out
}

func DeleteProvidersConfig(configPath string, req ProviderDeleteRequest) (*ProviderDeleteResult, error) {
	document, root, err := readProviderConfigDocument(configPath)
	if err != nil {
		return nil, err
	}
	requested := normalizeProviderManagementNames(req.Names)
	result := &ProviderDeleteResult{
		ConfigPath: strings.TrimSpace(configPath),
		DryRun:     req.DryRun,
		Requested:  requested,
	}
	if len(requested) == 0 {
		return result, nil
	}

	providersNode := mappingValue(root, "providers")
	itemsNode := (*yaml.Node)(nil)
	if providersNode != nil && providersNode.Kind == yaml.MappingNode {
		itemsNode = mappingValue(providersNode, "items")
	}
	if itemsNode == nil || itemsNode.Kind != yaml.MappingNode {
		result.NotFound = append(result.NotFound, requested...)
		return result, nil
	}

	deleteSet := make(map[string]struct{}, len(requested))
	providerNodes := make(map[string]*yaml.Node, len(requested))
	for _, name := range requested {
		node := mappingValue(itemsNode, name)
		if node == nil {
			result.NotFound = append(result.NotFound, name)
			continue
		}
		deleteSet[strings.ToLower(name)] = struct{}{}
		providerNodes[name] = node
	}
	if len(deleteSet) == 0 {
		return result, nil
	}

	replacementDefault := strings.TrimSpace(req.ReplacementDefault)
	if replacementDefault != "" {
		if _, deleting := deleteSet[strings.ToLower(replacementDefault)]; deleting || mappingValue(itemsNode, replacementDefault) == nil {
			for name := range providerNodes {
				result.Blocked = append(result.Blocked, ProviderDeleteBlocker{
					Provider: name,
					Code:     "invalid_replacement_default",
					Message:  fmt.Sprintf("replacement default provider %q is missing or also being deleted", replacementDefault),
				})
			}
		}
	}

	deletedDefault := false
	if providersNode != nil {
		if value := scalarString(mappingValue(providersNode, "default_provider")); value != "" {
			if _, deleting := deleteSet[strings.ToLower(value)]; deleting {
				deletedDefault = true
				if replacementDefault == "" && !req.ClearDefault {
					result.Blocked = append(result.Blocked, ProviderDeleteBlocker{
						Provider: value,
						Code:     "default_provider",
						Message:  "provider is providers.default_provider; pass --clear-default or --set-default",
					})
				}
			}
		}
	}

	groupRefs := providerGroupReferences(root, deleteSet)
	if len(groupRefs) > 0 && !req.Cascade {
		refsByProvider := make(map[string][]string)
		for _, ref := range groupRefs {
			refsByProvider[strings.ToLower(ref.Provider)] = append(refsByProvider[strings.ToLower(ref.Provider)], ref.Group)
		}
		for _, name := range requested {
			if refs := refsByProvider[strings.ToLower(name)]; len(refs) > 0 {
				sort.Strings(refs)
				result.Blocked = append(result.Blocked, ProviderDeleteBlocker{
					Provider:   name,
					Code:       "provider_group_reference",
					Message:    "provider is referenced by provider_groups; pass --cascade to remove those references",
					References: refs,
				})
			}
		}
	}

	if gatewayNode := gatewayProviderNameNode(root); gatewayNode != nil {
		gateway := scalarString(gatewayNode)
		if _, deleting := deleteSet[strings.ToLower(gateway)]; deleting {
			if replacementDefault == "" {
				result.Blocked = append(result.Blocked, ProviderDeleteBlocker{
					Provider: gateway,
					Code:     "skills_runtime_gateway_provider",
					Message:  "provider is skills_runtime.gateway_provider_name; pass --set-default to replace it first",
				})
			}
		}
	}

	result.Blocked = dedupeProviderDeleteBlockers(result.Blocked)
	if len(result.Blocked) > 0 {
		sortProviderDeleteResult(result)
		return result, nil
	}

	result.Deleted = append([]string(nil), existingRequestedProviderNames(requested, providerNodes)...)
	if req.Cascade {
		result.RemovedGroupRefs, result.RemovedGroups = removeProviderGroupReferences(root, deleteSet)
	}
	if deletedDefault {
		if replacementDefault != "" {
			upsertYAMLMappingValue(providersNode, "default_provider", stringYAMLNode(replacementDefault))
			result.ReplacementDefault = replacementDefault
		} else if req.ClearDefault {
			upsertYAMLMappingValue(providersNode, "default_provider", stringYAMLNode(""))
			result.ClearedDefaults = append(result.ClearedDefaults, "providers.default_provider")
		}
	}
	if applyAICLIChatProviderRemoval(root, deleteSet, replacementDefault) {
		if replacementDefault != "" {
			result.ReplacementDefault = replacementDefault
		} else {
			result.ClearedDefaults = append(result.ClearedDefaults, "aicli.chat.default_provider")
		}
	}
	if gatewayNode := gatewayProviderNameNode(root); gatewayNode != nil && replacementDefault != "" {
		if _, deleting := deleteSet[strings.ToLower(scalarString(gatewayNode))]; deleting {
			gatewayNode.Value = replacementDefault
			result.ReplacementDefault = replacementDefault
		}
	}

	deletedAuthRefs := providerAuthRefsFromNodes(providerNodes)
	for _, name := range result.Deleted {
		removeYAMLMappingValue(itemsNode, name)
	}
	if req.PruneAuth {
		pruneRefs, skipped := planDeletedProviderAuthRefs(deletedAuthRefs, providerAuthRefsInItems(itemsNode))
		result.AuthSkipped = skipped
		result.AuthPruned = pruneRefs
	}

	sortProviderDeleteResult(result)
	if req.DryRun {
		return result, nil
	}
	if err := writeProviderConfigDocument(configPath, document); err != nil {
		return nil, err
	}
	if req.PruneAuth {
		result.AuthPruned, err = deleteProviderAuthRefs(req.AuthStorePath, result.AuthPruned)
		if err != nil {
			return result, err
		}
		sortProviderDeleteResult(result)
	}
	return result, nil
}

func SetProvidersEnabledConfig(configPath string, names []string, enabled bool) (*ProviderEnableResult, error) {
	document, root, err := readProviderConfigDocument(configPath)
	if err != nil {
		return nil, err
	}
	result := &ProviderEnableResult{ConfigPath: strings.TrimSpace(configPath), Enabled: enabled}
	providersNode := mappingValue(root, "providers")
	itemsNode := (*yaml.Node)(nil)
	if providersNode != nil && providersNode.Kind == yaml.MappingNode {
		itemsNode = mappingValue(providersNode, "items")
	}
	for _, name := range normalizeProviderManagementNames(names) {
		node := (*yaml.Node)(nil)
		if itemsNode != nil && itemsNode.Kind == yaml.MappingNode {
			node = mappingValue(itemsNode, name)
		}
		if node == nil || node.Kind != yaml.MappingNode {
			result.NotFound = append(result.NotFound, name)
			continue
		}
		upsertYAMLMappingValue(node, "enabled", boolYAMLNode(enabled))
		result.Updated = append(result.Updated, name)
	}
	sort.Strings(result.Updated)
	sort.Strings(result.NotFound)
	if len(result.Updated) == 0 {
		return result, nil
	}
	if err := writeProviderConfigDocument(configPath, document); err != nil {
		return nil, err
	}
	return result, nil
}

func SetDefaultProviderConfig(configPath, name string) (*ProviderDefaultResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("provider name is required")
	}
	document, root, err := readProviderConfigDocument(configPath)
	if err != nil {
		return nil, err
	}
	providersNode := ensureChildMapping(root, "providers")
	itemsNode := ensureChildMapping(providersNode, "items")
	if mappingValue(itemsNode, name) == nil {
		return nil, fmt.Errorf("provider %q not found", name)
	}
	previous := scalarString(mappingValue(providersNode, "default_provider"))
	upsertYAMLMappingValue(providersNode, "default_provider", stringYAMLNode(name))
	if err := writeProviderConfigDocument(configPath, document); err != nil {
		return nil, err
	}
	return &ProviderDefaultResult{
		ConfigPath:      strings.TrimSpace(configPath),
		DefaultProvider: name,
		PreviousDefault: previous,
	}, nil
}

func readProviderConfigDocument(configPath string) (*yaml.Node, *yaml.Node, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, nil, fmt.Errorf("config path is required")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read config file %s: %w", configPath, err)
	}
	document, err := parseYAMLDocument(raw)
	if err != nil {
		return nil, nil, err
	}
	root, err := ensureYAMLRootMapping(document)
	if err != nil {
		return nil, nil, err
	}
	return document, root, nil
}

func writeProviderConfigDocument(configPath string, document *yaml.Node) error {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		_ = encoder.Close()
		return fmt.Errorf("encode config yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("finalize config yaml: %w", err)
	}
	return writeFileAtomic(configPath, output.Bytes())
}

func normalizeProviderManagementNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out
}

func existingRequestedProviderNames(requested []string, providers map[string]*yaml.Node) []string {
	out := make([]string, 0, len(providers))
	for _, name := range requested {
		if providers[name] != nil {
			out = append(out, name)
		}
	}
	return out
}

func scalarString(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func providerGroupNamesByProvider(groups []ProviderGroup) map[string][]string {
	out := make(map[string][]string)
	for _, group := range groups {
		groupName := strings.TrimSpace(group.Name)
		if groupName == "" {
			continue
		}
		for _, provider := range group.Providers {
			name := strings.TrimSpace(provider.Name)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			out[key] = append(out[key], groupName)
		}
	}
	for key := range out {
		sort.Strings(out[key])
	}
	return out
}

func providerGroupReferences(root *yaml.Node, deleteSet map[string]struct{}) []ProviderGroupReference {
	node := mappingValue(root, "provider_groups")
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	refs := make([]ProviderGroupReference, 0)
	for _, groupNode := range node.Content {
		if groupNode == nil || groupNode.Kind != yaml.MappingNode {
			continue
		}
		groupName := scalarString(mappingValue(groupNode, "name"))
		providersNode := mappingValue(groupNode, "providers")
		if providersNode == nil || providersNode.Kind != yaml.SequenceNode {
			continue
		}
		for _, providerNode := range providersNode.Content {
			name := providerGroupProviderName(providerNode)
			if _, ok := deleteSet[strings.ToLower(name)]; ok {
				refs = append(refs, ProviderGroupReference{Group: groupName, Provider: name})
			}
		}
	}
	return refs
}

func removeProviderGroupReferences(root *yaml.Node, deleteSet map[string]struct{}) ([]ProviderGroupReference, []string) {
	node := mappingValue(root, "provider_groups")
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, nil
	}
	refs := make([]ProviderGroupReference, 0)
	removedGroups := make([]string, 0)
	keptGroups := make([]*yaml.Node, 0, len(node.Content))
	for _, groupNode := range node.Content {
		if groupNode == nil || groupNode.Kind != yaml.MappingNode {
			keptGroups = append(keptGroups, groupNode)
			continue
		}
		groupName := scalarString(mappingValue(groupNode, "name"))
		providersNode := mappingValue(groupNode, "providers")
		if providersNode == nil || providersNode.Kind != yaml.SequenceNode {
			keptGroups = append(keptGroups, groupNode)
			continue
		}
		keptProviders := make([]*yaml.Node, 0, len(providersNode.Content))
		for _, providerNode := range providersNode.Content {
			name := providerGroupProviderName(providerNode)
			if _, ok := deleteSet[strings.ToLower(name)]; ok {
				refs = append(refs, ProviderGroupReference{Group: groupName, Provider: name})
				continue
			}
			keptProviders = append(keptProviders, providerNode)
		}
		providersNode.Content = keptProviders
		if len(keptProviders) == 0 {
			removedGroups = append(removedGroups, groupName)
			continue
		}
		keptGroups = append(keptGroups, groupNode)
	}
	node.Content = keptGroups
	return refs, removedGroups
}

func providerGroupProviderName(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return strings.TrimSpace(node.Value)
	case yaml.MappingNode:
		return scalarString(mappingValue(node, "name"))
	default:
		return ""
	}
}

func gatewayProviderNameNode(root *yaml.Node) *yaml.Node {
	node := mappingValue(root, "skills_runtime")
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	return mappingValue(node, "gateway_provider_name")
}

func applyAICLIChatProviderRemoval(root *yaml.Node, deleteSet map[string]struct{}, replacement string) bool {
	aicliNode := mappingValue(root, "aicli")
	if aicliNode == nil || aicliNode.Kind != yaml.MappingNode {
		return false
	}
	chatNode := mappingValue(aicliNode, "chat")
	if chatNode == nil || chatNode.Kind != yaml.MappingNode {
		return false
	}
	current := scalarString(mappingValue(chatNode, "default_provider"))
	if _, deleting := deleteSet[strings.ToLower(current)]; !deleting {
		return false
	}
	if strings.TrimSpace(replacement) != "" {
		upsertYAMLMappingValue(chatNode, "default_provider", stringYAMLNode(strings.TrimSpace(replacement)))
	} else {
		removeYAMLMappingValue(chatNode, "default_provider")
	}
	return true
}

func providerAuthRefsFromNodes(nodes map[string]*yaml.Node) []string {
	refs := make([]string, 0, len(nodes)*2)
	seen := map[string]struct{}{}
	for _, node := range nodes {
		if node == nil || node.Kind != yaml.MappingNode {
			continue
		}
		for _, key := range []string{"api_key_ref", "auth_ref"} {
			ref := scalarString(mappingValue(node, key))
			if ref == "" {
				continue
			}
			lower := strings.ToLower(ref)
			if _, ok := seen[lower]; ok {
				continue
			}
			seen[lower] = struct{}{}
			refs = append(refs, ref)
		}
	}
	return refs
}

func providerAuthRefsInItems(itemsNode *yaml.Node) map[string][]string {
	out := make(map[string][]string)
	if itemsNode == nil || itemsNode.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(itemsNode.Content); i += 2 {
		name := strings.TrimSpace(itemsNode.Content[i].Value)
		node := itemsNode.Content[i+1]
		if node == nil || node.Kind != yaml.MappingNode || name == "" {
			continue
		}
		for _, key := range []string{"api_key_ref", "auth_ref"} {
			ref := scalarString(mappingValue(node, key))
			if ref == "" {
				continue
			}
			out[strings.ToLower(ref)] = append(out[strings.ToLower(ref)], name)
		}
	}
	for key := range out {
		sort.Strings(out[key])
	}
	return out
}

func planDeletedProviderAuthRefs(refs []string, remaining map[string][]string) ([]string, []ProviderAuthPruneSkip) {
	if len(refs) == 0 {
		return nil, nil
	}
	pruneRefs := make([]string, 0, len(refs))
	skipped := make([]ProviderAuthPruneSkip, 0)
	seen := map[string]struct{}{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		key := strings.ToLower(ref)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if providers := remaining[key]; len(providers) > 0 {
			skipped = append(skipped, ProviderAuthPruneSkip{Ref: ref, Reason: "shared_ref", Providers: append([]string(nil), providers...)})
			continue
		}
		pruneRefs = append(pruneRefs, ref)
	}
	return pruneRefs, skipped
}

func deleteProviderAuthRefs(authStorePath string, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	path := strings.TrimSpace(authStorePath)
	if path == "" {
		path = DefaultAuthStorePath()
	}
	return DeleteProviderAuthRefsFromPath(path, refs)
}

func dedupeProviderDeleteBlockers(blockers []ProviderDeleteBlocker) []ProviderDeleteBlocker {
	out := make([]ProviderDeleteBlocker, 0, len(blockers))
	seen := map[string]struct{}{}
	for _, blocker := range blockers {
		key := strings.ToLower(blocker.Provider + "\x00" + blocker.Code)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		sort.Strings(blocker.References)
		out = append(out, blocker)
	}
	return out
}

func sortProviderDeleteResult(result *ProviderDeleteResult) {
	if result == nil {
		return
	}
	sort.Strings(result.Deleted)
	sort.Strings(result.NotFound)
	sort.Strings(result.RemovedGroups)
	sort.Strings(result.ClearedDefaults)
	sort.Strings(result.AuthPruned)
	sort.Slice(result.Blocked, func(i, j int) bool {
		if result.Blocked[i].Provider != result.Blocked[j].Provider {
			return result.Blocked[i].Provider < result.Blocked[j].Provider
		}
		return result.Blocked[i].Code < result.Blocked[j].Code
	})
	sort.Slice(result.RemovedGroupRefs, func(i, j int) bool {
		if result.RemovedGroupRefs[i].Group != result.RemovedGroupRefs[j].Group {
			return result.RemovedGroupRefs[i].Group < result.RemovedGroupRefs[j].Group
		}
		return result.RemovedGroupRefs[i].Provider < result.RemovedGroupRefs[j].Provider
	})
	sort.Slice(result.AuthSkipped, func(i, j int) bool {
		return result.AuthSkipped[i].Ref < result.AuthSkipped[j].Ref
	})
}
