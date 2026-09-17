package agentconfig

import "strings"

// Layered write routing (design §7 / decision D3).
//
// When layered merging is active, "which file should this write go to" cannot
// be answered by the single path the caller happens to hold: a provider that
// only exists in the user layer must be edited in the user layer, otherwise the
// value gets pinned into the project layer and the lower layer stops taking
// effect. The router below therefore resolves the target file from the key
// paths a write touches, using the origins recorded at load time.
//
// Safety properties (all three must hold before a write is rerouted):
//
//  1. the active config was loaded with MergeModeOn;
//  2. the path the caller passed is a member of that config's layer stack
//     (custom/explicit paths, temp files and runtime-server paths are never
//     touched);
//  3. at least one written key has a recorded origin layer.

// WriteTargetForKeys returns the layer file that writes for the given dotted
// key paths should target, or "" when no routing applies.
//
// Rules:
//   - keys with a recorded origin go to the file that supplied their value;
//   - when a write spans several layers the highest-precedence file wins, so a
//     single write never splits across files;
//   - keys that do not exist yet return "" (callers keep their default target,
//     which is the highest layer, i.e. "new keys go to the highest writable
//     layer").
func (c *Config) WriteTargetForKeys(keys ...string) string {
	if c == nil || c.ConfigMergeMode != MergeModeOn || len(c.ConfigOriginFiles) == 0 {
		return ""
	}
	target := ""
	for _, key := range keys {
		file := c.originFileFor(key)
		if file == "" {
			continue
		}
		if target == "" || c.layerComesAfter(file, target) {
			target = file
		}
	}
	return target
}

func (c *Config) originFileFor(key string) string {
	path, ok := c.originPathFor(key)
	if !ok {
		return ""
	}
	return c.ConfigOriginFiles[path]
}

// originPathFor resolves a key path to the recorded origin key with the longest
// matching prefix, so subtree writes (for example "providers.items.openai")
// resolve through their leaf origins.
func (c *Config) originPathFor(key string) (string, bool) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return "", false
	}
	bestPath := ""
	for path := range c.ConfigOriginFiles {
		if path == trimmed ||
			strings.HasPrefix(path, trimmed+".") ||
			strings.HasPrefix(trimmed, path+".") {
			if betterOriginPath(c, path, bestPath) {
				bestPath = path
			}
		}
	}
	if bestPath == "" {
		return "", false
	}
	return bestPath, true
}

// betterOriginPath implements a deterministic preference order between two
// candidate origin paths: the longer (more specific) path wins; on a tie the
// layer with the higher precedence wins, and a final lexicographic tie-break
// keeps the result independent of map iteration order.
func betterOriginPath(c *Config, candidate, current string) bool {
	if current == "" {
		return true
	}
	if len(candidate) != len(current) {
		return len(candidate) > len(current)
	}
	candidateFile := c.ConfigOriginFiles[candidate]
	currentFile := c.ConfigOriginFiles[current]
	if candidateFile != currentFile {
		if rank := c.layerIndex(candidateFile) - c.layerIndex(currentFile); rank != 0 {
			return rank > 0
		}
	}
	return candidate < current
}

// layerComesAfter reports whether candidate has higher precedence than other in
// the recorded layer stack.
func (c *Config) layerComesAfter(candidate, other string) bool {
	return c.layerIndex(candidate) > c.layerIndex(other)
}

func (c *Config) layerIndex(path string) int {
	normalized := normalizeLayerPath(path)
	if normalized == "" {
		return -1
	}
	for index, layer := range c.ConfigLayers {
		if candidate := normalizeLayerPath(layer.Path); candidate != "" && strings.EqualFold(candidate, normalized) {
			return index
		}
	}
	return -1
}

// isLayerPath reports whether path is a member of this config's layer stack.
func (c *Config) isLayerPath(path string) bool {
	return c.layerIndex(path) >= 0
}

// routeConfigWritePath reroutes a write to the layer that owns the keys being
// written, when (and only when) the active configuration was loaded with
// layered merging enabled. Every other case returns configPath unchanged, so
// single-file behaviour is preserved exactly.
func routeConfigWritePath(configPath string, keys ...string) string {
	current := globalConfig
	if current == nil || len(keys) == 0 {
		return configPath
	}
	if !current.isLayerPath(configPath) {
		return configPath
	}
	if target := current.WriteTargetForKeys(keys...); target != "" {
		return target
	}
	return configPath
}

// providerUpdateWriteKeys lists the config key paths a provider update touches.
func providerUpdateWriteKeys(name string, update ProviderConfigUpdate) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(update.Name)
	}
	keys := make([]string, 0, 2)
	if name != "" {
		keys = append(keys, "providers.items."+name)
	}
	if update.SetDefaultProvider {
		keys = append(keys, "providers.default_provider")
	}
	return keys
}

// providerKnownToMergedConfig reports whether the active layered configuration
// defines providers.items.<name> in any layer (including the system preset
// layer). It is used by write paths that must validate a provider reference
// against the merged view instead of a single layer file. Single-file mode
// returns false so callers keep the strict file-local check.
func providerKnownToMergedConfig(name string) bool {
	current := globalConfig
	if current == nil || current.ConfigMergeMode != MergeModeOn {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if _, ok := current.Providers.Items[name]; ok {
		return true
	}
	for candidate := range current.Providers.Items {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

// chatUpdateWriteKeys lists the config key paths a chat preference update touches.
func chatUpdateWriteKeys(update AICLIChatPreferenceUpdate) []string {
	keys := []string{"aicli.chat"}
	if update.DefaultProvider != nil {
		keys = append(keys, "providers.default_provider")
	}
	return keys
}

// providerNamesWriteKeys lists providers.items.<name> key paths for a batch.
func providerNamesWriteKeys(names []string) []string {
	keys := make([]string, 0, len(names))
	for _, name := range normalizeProviderManagementNames(names) {
		keys = append(keys, "providers.items."+name)
	}
	return keys
}
