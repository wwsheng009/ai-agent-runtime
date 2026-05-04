package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"gopkg.in/yaml.v3"
)

// AICLIChatPreferenceUpdate describes a partial update to the persisted chat defaults.
type AICLIChatPreferenceUpdate struct {
	DefaultProvider *string
	DefaultModel    *string
	ReasoningEffort *string
	// Stream 使用指针指针以便在“不修改”、“显式 true”、“显式 false”三种语义之间区分。
	// 外层指针为 nil 表示不修改；非 nil 时内层指针的值会被写入配置（包括 false）。
	Stream **bool
}

// UpdateAICLIChatPreferences updates the aicli.chat section inside a config file
// without rewriting unrelated top-level sections.
func UpdateAICLIChatPreferences(configPath string, update AICLIChatPreferenceUpdate) (*AICLIChatConfig, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			if _, _, starterErr := EnsureStarterConfigAtPath(configPath); starterErr != nil {
				return nil, starterErr
			}
			raw, err = os.ReadFile(configPath)
			if err != nil {
				return nil, fmt.Errorf("read starter config file %s: %w", configPath, err)
			}
		} else {
			return nil, fmt.Errorf("read config file %s: %w", configPath, err)
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

	current, err := currentAICLIChatConfig(root)
	if err != nil {
		return nil, err
	}
	applyAICLIChatPreferenceUpdate(current, update)

	sectionNode, err := marshalYAMLNode(current)
	if err != nil {
		return nil, err
	}

	aicliNode := mappingValue(root, "aicli")
	if aicliNode == nil || aicliNode.Kind != yaml.MappingNode {
		aicliNode = &yaml.Node{Kind: yaml.MappingNode}
		upsertYAMLMappingValue(root, "aicli", aicliNode)
	}
	upsertYAMLMappingValue(aicliNode, "chat", sectionNode)

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

	return current, nil
}

func applyAICLIChatPreferenceUpdate(current *AICLIChatConfig, update AICLIChatPreferenceUpdate) {
	if current == nil {
		return
	}
	if update.DefaultProvider != nil {
		current.DefaultProvider = strings.TrimSpace(*update.DefaultProvider)
	}
	if update.DefaultModel != nil {
		current.DefaultModel = strings.TrimSpace(*update.DefaultModel)
	}
	if update.ReasoningEffort != nil {
		current.ReasoningEffort = runtimetypes.NormalizeReasoningEffort(*update.ReasoningEffort)
	}
	if update.Stream != nil {
		if *update.Stream == nil {
			current.Stream = nil
		} else {
			value := **update.Stream
			current.Stream = &value
		}
	}
}

func currentAICLIChatConfig(root *yaml.Node) (*AICLIChatConfig, error) {
	current := &AICLIChatConfig{}
	if root == nil {
		return current, nil
	}
	aicliNode := mappingValue(root, "aicli")
	if aicliNode == nil {
		return current, nil
	}
	chatNode := mappingValue(aicliNode, "chat")
	if chatNode == nil {
		return current, nil
	}
	if err := decodeYAMLNode(chatNode, current); err != nil {
		return nil, fmt.Errorf("decode aicli.chat section: %w", err)
	}
	return current, nil
}

func parseYAMLDocument(raw []byte) (*yaml.Node, error) {
	var document yaml.Node
	if len(bytes.TrimSpace(raw)) == 0 {
		document = yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{{
				Kind: yaml.MappingNode,
			}},
		}
		return &document, nil
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}
	return &document, nil
}

func ensureYAMLRootMapping(document *yaml.Node) (*yaml.Node, error) {
	if document == nil {
		return nil, fmt.Errorf("config root document is nil")
	}
	if document.Kind == 0 {
		document.Kind = yaml.DocumentNode
	}
	if document.Kind != yaml.DocumentNode {
		return nil, fmt.Errorf("config root must be a yaml document")
	}
	if len(document.Content) == 0 {
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	root := document.Content[0]
	if root.Kind == 0 {
		root.Kind = yaml.MappingNode
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config root must be a mapping")
	}
	return root, nil
}

func currentMappingValue(root *yaml.Node, key string) *yaml.Node {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Kind == yaml.ScalarNode && root.Content[i].Value == key {
			return root.Content[i+1]
		}
	}
	return nil
}

func mappingValue(root *yaml.Node, key string) *yaml.Node {
	return currentMappingValue(root, key)
}

func upsertYAMLMappingValue(root *yaml.Node, key string, value *yaml.Node) {
	if root == nil || key == "" || value == nil {
		return
	}
	if root.Kind == 0 {
		root.Kind = yaml.MappingNode
	}
	if root.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Kind == yaml.ScalarNode && root.Content[i].Value == key {
			root.Content[i+1] = value
			return
		}
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func marshalYAMLNode(value interface{}) (*yaml.Node, error) {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml node: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode yaml node: %w", err)
	}
	if len(document.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	return document.Content[0], nil
}

func decodeYAMLNode(node *yaml.Node, target interface{}) error {
	if node == nil {
		return nil
	}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	if err := encoder.Encode(node); err != nil {
		_ = encoder.Close()
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return yaml.Unmarshal(buf.Bytes(), target)
}

func writeFileAtomic(path string, data []byte) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("config path is required")
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}

	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
	}

	temp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()

	if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("prepare temp config file mode: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp config file: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(path)
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			return fmt.Errorf("replace config file: %w (retry after remove: %v)", err, retryErr)
		}
	}
	return nil
}
