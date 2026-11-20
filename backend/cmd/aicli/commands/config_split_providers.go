package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type configSplitProvidersResult struct {
	Target         string   `json:"target"`
	Owner          string   `json:"owner,omitempty"`
	Providers      int      `json:"providers"`
	ProviderGroups int      `json:"provider_groups"`
	DryRun         bool     `json:"dry_run"`
	WroteTarget    bool     `json:"wrote_target"`
	PrunedOwner    bool     `json:"pruned_owner"`
	Backup         string   `json:"backup,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// NewConfigSplitProvidersCommand implements a conservative provider migration:
// it copies the provider-owned top-level sections from their current owner
// layer into providers.yaml. Only a single owner layer is supported in this
// version; multi-layer provider definitions must be consolidated manually.
func NewConfigSplitProvidersCommand() *cobra.Command {
	var (
		write     bool
		keepMain  bool
		pruneMain bool
		project   bool
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "split-providers",
		Short: "把 config.yaml 的 provider 配置迁移到 providers.yaml",
		Long: `把当前生效配置中的 providers / provider_groups 复制到独立的 providers.yaml。

默认只输出计划（dry-run）。--write --keep-main 保留主配置副本，便于回滚；
--write --prune-main 会从 owner 配置里移除这两个顶层节（不支持多 owner 层）。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if keepMain && pruneMain && cmd.Flags().Changed("keep-main") {
				return fmt.Errorf("--keep-main 与 --prune-main 互斥")
			}
			if !keepMain && !pruneMain {
				return fmt.Errorf("--keep-main=false 需要同时指定 --prune-main")
			}
			if pruneMain {
				keepMain = false
			}
			result, err := runConfigSplitProviders(configSplitProvidersOptions{
				Write:     write,
				PruneMain: pruneMain,
				Project:   project,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				raw, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return nil
			}
			printConfigSplitProvidersResult(cmd, result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "实际写盘（默认只输出计划）")
	cmd.Flags().BoolVar(&keepMain, "keep-main", true, "保留主配置副本（默认）")
	cmd.Flags().BoolVar(&pruneMain, "prune-main", false, "从 owner 配置移除 providers/provider_groups")
	cmd.Flags().BoolVar(&project, "project", false, "迁移到项目层 ./.aicli/providers.yaml")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "以 JSON 输出")
	return cmd
}

type configSplitProvidersOptions struct {
	Write     bool
	PruneMain bool
	Project   bool
}

func runConfigSplitProviders(options configSplitProvidersOptions) (configSplitProvidersResult, error) {
	result := configSplitProvidersResult{DryRun: !options.Write}
	if !agentconfig.ProviderConfigFileEnabled() {
		return result, fmt.Errorf("provider config file is disabled by %s=off", agentconfig.ProviderConfigEnvVar)
	}
	if agentconfig.MergeModeFromEnv() != agentconfig.MergeModeOn {
		return result, fmt.Errorf("%s must be on to migrate providers into the layered provider file", agentconfig.MergeConfigEnvVar)
	}
	merged, err := agentconfig.LoadMergedConfigDocument()
	if err != nil {
		return result, err
	}
	layers := merged.Layers

	ownerSet := map[string]struct{}{}
	for key, file := range merged.OriginFiles {
		if key != "providers" &&
			!strings.HasPrefix(key, "providers.") &&
			key != "provider_groups" &&
			!strings.HasPrefix(key, "provider_groups.") {
			continue
		}
		layer, ok := layerByPath(layers, file)
		if !ok || layer.Fragment {
			continue
		}
		if layer.ReadOnly {
			return result, fmt.Errorf("provider definitions originate in read-only layer %s; refusing to migrate", file)
		}
		ownerSet[file] = struct{}{}
	}
	if len(ownerSet) == 0 {
		result.Warnings = append(result.Warnings, "provider 配置已经来自 provider fragment，或当前没有 provider 定义")
		return result, nil
	}
	if len(ownerSet) > 1 {
		owners := make([]string, 0, len(ownerSet))
		for file := range ownerSet {
			owners = append(owners, file)
		}
		sort.Strings(owners)
		return result, fmt.Errorf("provider definitions span multiple layers (%s); consolidate them into one config first", strings.Join(owners, ", "))
	}
	for file := range ownerSet {
		result.Owner = file
	}
	ownerLayer, _ := layerByPath(layers, result.Owner)
	wantTarget := agentconfig.LayerKindUserProviders
	if options.Project || ownerLayer.Kind == agentconfig.LayerKindProject {
		// A project config must not be migrated into the lower-precedence user
		// provider file: the stale project providers would keep masking it.
		wantTarget = agentconfig.LayerKindProjectProviders
	}
	var targetLayer agentconfig.ConfigLayer
	for _, layer := range agentconfig.ProviderConfigLayerStack() {
		if layer.Kind == wantTarget {
			targetLayer = layer
			break
		}
	}
	if strings.TrimSpace(targetLayer.Path) == "" {
		return result, fmt.Errorf("provider config layer is not available")
	}
	targetPath, err := filepath.Abs(targetLayer.Path)
	if err != nil {
		return result, err
	}
	result.Target = targetPath
	if sameLayerPathForCommand(result.Owner, targetPath) {
		return result, fmt.Errorf("provider target and owner are the same file: %s", targetPath)
	}
	if info, statErr := os.Stat(targetPath); statErr == nil && info.Size() > 0 {
		return result, fmt.Errorf("provider target already exists and is not empty: %s", targetPath)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return result, statErr
	}

	if options.Write {
		// The migration reads the owner and writes both the owner (optional) and
		// the provider target, so take both shared config locks in sorted order.
		unlockAll := agentconfig.LockConfigFileWriteAll(result.Owner, targetPath)
		defer unlockAll()
	}
	ownerDocument, ownerRoot, ownerRaw, err := readYAMLMappingDocument(result.Owner)
	if err != nil {
		return result, err
	}
	providersNode := mappingNodeValue(ownerRoot, "providers")
	groupsNode := mappingNodeValue(ownerRoot, "provider_groups")
	if providersNode == nil && groupsNode == nil {
		return result, fmt.Errorf("owner config %s has no providers/provider_groups section", result.Owner)
	}
	result.Providers = providerItemCount(providersNode)
	result.ProviderGroups = sequenceLength(groupsNode)

	if !options.Write {
		return result, nil
	}

	targetDocument := &yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{{
			Kind: yaml.MappingNode,
		}},
	}
	targetRoot := targetDocument.Content[0]
	if providersNode != nil {
		targetRoot.Content = append(targetRoot.Content, stringYAMLNode("providers"), providersNode)
	}
	if groupsNode != nil {
		targetRoot.Content = append(targetRoot.Content, stringYAMLNode("provider_groups"), groupsNode)
	}
	targetRaw, err := marshalSplitProvidersDocument(targetDocument)
	if err != nil {
		return result, fmt.Errorf("encode providers.yaml: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return result, err
	}
	err = agentconfig.WriteConfigFileAtomic(targetPath, targetRaw, 0o600)
	if err != nil {
		return result, err
	}
	result.WroteTarget = true

	if options.PruneMain {
		backupPath := result.Owner + ".bak"
		if err := agentconfig.WriteConfigFileAtomic(backupPath, ownerRaw, 0o600); err != nil {
			return result, err
		}
		result.Backup = backupPath
		removeMappingNodeValue(ownerRoot, "providers")
		removeMappingNodeValue(ownerRoot, "provider_groups")
		ownerOutput, err := marshalSplitProvidersDocument(ownerDocument)
		if err != nil {
			return result, fmt.Errorf("encode owner config: %w", err)
		}
		if err := agentconfig.WriteConfigFileAtomic(result.Owner, ownerOutput, 0); err != nil {
			return result, err
		}
		result.PrunedOwner = true
	} else {
		result.Warnings = append(result.Warnings,
			"主配置仍保留 provider 副本；providers.yaml 优先，确认无误后可用 --prune-main 清理")
	}

	if _, err := agentconfig.LoadMergedConfigDocument(); err != nil {
		return result, fmt.Errorf("verify migrated config: %w", err)
	}
	return result, nil
}

func printConfigSplitProvidersResult(cmd *cobra.Command, result configSplitProvidersResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Provider split plan\n")
	fmt.Fprintf(out, "  target:          %s\n", result.Target)
	if result.Owner != "" {
		fmt.Fprintf(out, "  owner:           %s\n", result.Owner)
	}
	fmt.Fprintf(out, "  providers:       %d\n", result.Providers)
	fmt.Fprintf(out, "  provider_groups: %d\n", result.ProviderGroups)
	fmt.Fprintf(out, "  dry_run:         %v\n", result.DryRun)
	if result.WroteTarget {
		fmt.Fprintf(out, "  wrote_target:    true\n")
	}
	if result.PrunedOwner {
		fmt.Fprintf(out, "  pruned_owner:    true (backup: %s)\n", result.Backup)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(out, "  warning:         %s\n", warning)
	}
	if !result.DryRun {
		fmt.Fprintf(out, "\n迁移完成。建议检查 providers.yaml 与 config.yaml 后重启/刷新相关进程。\n")
	}
}

func layerByPath(layers []agentconfig.ConfigLayer, path string) (agentconfig.ConfigLayer, bool) {
	cleaned := normalizeCommandPath(path)
	for _, layer := range layers {
		if normalizeCommandPath(layer.Path) == cleaned {
			return layer, true
		}
	}
	return agentconfig.ConfigLayer{}, false
}

func normalizeCommandPath(path string) string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if absolute, err := filepath.Abs(cleaned); err == nil && absolute != "" {
		return absolute
	}
	return cleaned
}

func sameLayerPathForCommand(left, right string) bool {
	a := normalizeCommandPath(left)
	b := normalizeCommandPath(right)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

func readYAMLMappingDocument(path string) (*yaml.Node, *yaml.Node, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	document := &yaml.Node{}
	if len(strings.TrimSpace(string(raw))) == 0 {
		document = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	} else if err := yaml.Unmarshal(raw, document); err != nil {
		return nil, nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(document.Content) == 0 {
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, nil, fmt.Errorf("%s root is not a YAML mapping", path)
	}
	return document, root, raw, nil
}

func mappingNodeValue(root *yaml.Node, key string) *yaml.Node {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Kind == yaml.ScalarNode && root.Content[index].Value == key {
			return root.Content[index+1]
		}
	}
	return nil
}

func removeMappingNodeValue(root *yaml.Node, key string) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Kind == yaml.ScalarNode && root.Content[index].Value == key {
			root.Content = append(root.Content[:index], root.Content[index+2:]...)
			return
		}
	}
}

func providerItemCount(providers *yaml.Node) int {
	if providers == nil || providers.Kind != yaml.MappingNode {
		return 0
	}
	items := mappingNodeValue(providers, "items")
	if items == nil || items.Kind != yaml.MappingNode {
		return 0
	}
	return len(items.Content) / 2
}

func sequenceLength(node *yaml.Node) int {
	if node == nil || node.Kind != yaml.SequenceNode {
		return 0
	}
	return len(node.Content)
}

func stringYAMLNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func marshalSplitProvidersDocument(document *yaml.Node) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		_ = encoder.Close()
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
