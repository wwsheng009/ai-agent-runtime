package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetHookEnabled persists the per-handler enabled flag into the `state` map of
// a hooks file. JSON files are rewritten with stable indentation; YAML files
// are edited through yaml.Node so comments, key order, and formatting survive.
func SetHookEnabled(path, id string, enabled bool) error {
	path = strings.TrimSpace(path)
	id = strings.TrimSpace(id)
	if path == "" || id == "" {
		return fmt.Errorf("hooks path and handler id are required")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		return setHookEnabledYAML(path, id, enabled)
	}
	if ext != ".json" {
		return fmt.Errorf("unsupported hooks file extension %q", ext)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse hooks file %s: %w", path, err)
	}
	state := map[string]HookState{}
	if raw, ok := doc["state"]; ok && len(raw) > 0 {
		_ = json.Unmarshal(raw, &state)
	}
	current := state[id]
	current.Enabled = &enabled
	state[id] = current
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	doc["state"] = encoded
	output, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	output = append(output, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, output, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// setHookEnabledYAML edits the `state` mapping through yaml.Node so comments,
// key order, and the rest of the document are preserved.
func setHookEnabledYAML(path, id string, enabled bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse hooks file %s: %w", path, err)
	}
	if root.Kind == 0 {
		root = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if len(root.Content) == 0 {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.MappingNode})
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return fmt.Errorf("hooks file %s root is not a mapping", path)
	}
	state := ensureYAMLMapping(doc, "state")
	entry := ensureYAMLMapping(state, id)
	setYAMLScalar(entry, "enabled", fmt.Sprintf("%t", enabled), "!!bool")

	output, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, output, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ensureYAMLMapping(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			if parent.Content[i+1].Kind != yaml.MappingNode {
				parent.Content[i+1] = &yaml.Node{Kind: yaml.MappingNode}
			}
			return parent.Content[i+1]
		}
	}
	value := &yaml.Node{Kind: yaml.MappingNode}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
	return value
}

func setYAMLScalar(parent *yaml.Node, key, value, tag string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
			return
		}
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value},
	)
}
