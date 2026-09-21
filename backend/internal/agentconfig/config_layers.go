package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// MergeMode selects how the bootstrap config candidates are combined.
type MergeMode string

const (
	// MergeModeOff keeps the historical single-file behaviour: the first
	// existing candidate in the search order wins and lower layers never
	// contribute missing keys.
	MergeModeOff MergeMode = "off"
	// MergeModeDryRun keeps single-file behaviour but computes (and logs) what a
	// layered merge would change, so the blast radius can be sized before the
	// merge is switched on.
	MergeModeDryRun MergeMode = "dry-run"
	// MergeModeOn merges every existing layer from lowest to highest
	// precedence; a higher layer only overrides the keys it explicitly writes.
	MergeModeOn MergeMode = "on"

	// MergeConfigEnvVar selects the merge mode; see ParseMergeMode.
	MergeConfigEnvVar = "AICLI_CONFIG_MERGE"

	// dryRunReportKeyLimit caps how many key names a dry-run report lists.
	dryRunReportKeyLimit = 20
)

// ParseMergeMode normalises a raw AICLI_CONFIG_MERGE value. Unknown values fall
// back to MergeModeOff so a typo can never silently enable layering.
func ParseMergeMode(raw string) MergeMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "off", "false", "0", "no", "disabled":
		return MergeModeOff
	case "dry-run", "dryrun", "preview", "warn":
		return MergeModeDryRun
	case "on", "true", "1", "yes", "enabled", "merge":
		return MergeModeOn
	default:
		return MergeModeOff
	}
}

// MergeModeFromEnv returns the configured merge mode (default: off).
func MergeModeFromEnv() MergeMode {
	return ParseMergeMode(os.Getenv(MergeConfigEnvVar))
}

// ConfigLayerKind identifies a candidate's precedence role.
type ConfigLayerKind string

const (
	// LayerKindPortable is the bundle/default layer shipped with the binary.
	LayerKindPortable ConfigLayerKind = "portable"
	// LayerKindLegacy is a loose current-directory config file.
	LayerKindLegacy ConfigLayerKind = "legacy"
	// LayerKindUser is $HOME/.aicli/<config>.
	LayerKindUser ConfigLayerKind = "user"
	// LayerKindProject is ./.aicli/<config> (highest precedence).
	LayerKindProject ConfigLayerKind = "project"
)

// ConfigLayer is one bootstrap config candidate in precedence order.
type ConfigLayer struct {
	Kind    ConfigLayerKind
	Path    string
	Present bool
	// ReadOnly marks a layer that ships with the bundle/repository: it supplies
	// defaults but never receives writes; edits are routed to the highest
	// writable layer instead. runtime.yaml no longer has such a layer — its
	// stack is user + project only (see RuntimeConfigLayerStack) — but the flag
	// stays part of the layer contract for bundle-style stacks.
	ReadOnly bool
}

// RuntimeConfigLayerStack returns the runtime.yaml candidates ordered from the
// lowest precedence to the highest:
//
//  1. $HOME/.aicli/runtime.yaml   user level (created on first write)
//  2. ./.aicli/runtime.yaml       project level
//
// The repository/development layouts (configs/runtime.yaml,
// backend/configs/runtime.yaml) are deliberately absent: backend/configs is a
// development directory, so it must never act as an implicit configuration
// source for aicli processes. A file there is only read when a caller passes it
// explicitly (for example `runtime-server --config backend/configs/runtime.yaml`).
//
// Both layers are merged (a higher layer only overrides the keys it explicitly
// writes); see LoadMergedRuntimeConfigDocument.
func RuntimeConfigLayerStack() []ConfigLayer {
	name := aiclipaths.DefaultRuntimeConfigFileName
	layers := make([]ConfigLayer, 0, 2)
	if home, err := userHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		layers = append(layers, ConfigLayer{Kind: LayerKindUser, Path: filepath.Join(home, ".aicli", name)})
	} else {
		// Keep a user-level candidate even when the home directory cannot be
		// resolved, so the write target stays deterministic.
		layers = append(layers, ConfigLayer{Kind: LayerKindUser, Path: filepath.Join(".aicli", name)})
	}
	layers = append(layers, ConfigLayer{Kind: LayerKindProject, Path: filepath.Join(".aicli", name)})
	for index := range layers {
		layers[index].Present = fileExists(layers[index].Path)
	}
	return layers
}

// WritableLayer returns the layer that receives new keys and redirected writes:
// the highest present writable layer, or the user-level candidate (which the
// caller may not have created yet).
func WritableLayer(layers []ConfigLayer) (ConfigLayer, bool) {
	for index := len(layers) - 1; index >= 0; index-- {
		if layers[index].Present && !layers[index].ReadOnly {
			return layers[index], true
		}
	}
	// Nothing exists yet: default to the user-level candidate so the first edit
	// creates $HOME/.aicli/<name> instead of dropping a file into the CWD (D4).
	for index := len(layers) - 1; index >= 0; index-- {
		if !layers[index].ReadOnly && layers[index].Kind == LayerKindUser {
			return layers[index], false
		}
	}
	for index := len(layers) - 1; index >= 0; index-- {
		if !layers[index].ReadOnly {
			return layers[index], false
		}
	}
	return ConfigLayer{}, false
}

// RuntimeConfigWriteTarget returns the file a runtime.yaml write belongs to
// when the key has no origin: the highest writable layer that exists, otherwise
// the user-level path (so the first edit creates ~/.aicli/runtime.yaml instead
// of touching a repository file).
func RuntimeConfigWriteTarget() (string, ConfigLayerKind) {
	layer, _ := WritableLayer(RuntimeConfigLayerStack())
	if strings.TrimSpace(layer.Path) == "" {
		return "", ""
	}
	if absolute, err := filepath.Abs(layer.Path); err == nil && absolute != "" {
		return absolute, layer.Kind
	}
	return filepath.Clean(layer.Path), layer.Kind
}

// ConfigLayerStack returns every bootstrap config candidate ordered from the
// lowest precedence to the highest:
//
//  1. ./configs/<name>      portable bundle (also the repository layout)
//  2. ./<name>              loose current-directory config
//  3. ./aicli.yaml          legacy aicli config name
//  4. $HOME/.aicli/<name>   user level
//  5. ./.aicli/<name>       project level
//
// aicli and runtime-server must both derive their candidate list from this
// function; otherwise the CLI and the server disagree about precedence.
func ConfigLayerStack() []ConfigLayer {
	names := defaultConfigSearchNames()
	layers := make([]ConfigLayer, 0, 2*len(names)+3)
	for _, name := range names {
		layers = append(layers, ConfigLayer{Kind: LayerKindPortable, Path: filepath.Join("configs", name)})
	}
	for _, name := range names {
		layers = append(layers, ConfigLayer{Kind: LayerKindLegacy, Path: name})
	}
	layers = append(layers, ConfigLayer{Kind: LayerKindLegacy, Path: aiclipaths.DefaultCLIConfigFileName})
	if home, err := userHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		for _, name := range names {
			layers = append(layers, ConfigLayer{Kind: LayerKindUser, Path: filepath.Join(home, ".aicli", name)})
		}
	}
	for _, name := range names {
		layers = append(layers, ConfigLayer{Kind: LayerKindProject, Path: filepath.Join(".aicli", name)})
	}
	for index := range layers {
		layers[index].Present = fileExists(layers[index].Path)
	}
	return layers
}

// ConfigLayerSearchPaths returns the layer paths ordered from the highest
// precedence to the lowest, i.e. the order used by first-match-wins lookups
// such as startup .env discovery and single-file resolution.
func ConfigLayerSearchPaths() []string {
	layers := ConfigLayerStack()
	paths := make([]string, 0, len(layers))
	for index := len(layers) - 1; index >= 0; index-- {
		paths = append(paths, layers[index].Path)
	}
	return paths
}

// mergeMergeMapsMasked behaves like mergeMergeMaps but treats an explicit null
// in the overlay as "remove this key from the lower layers" (Kubernetes/Helm
// style) instead of storing a nil value. It is only used by the layered loader;
// the preset path keeps the original nil-preserving semantics.
func mergeMergeMapsMasked(base, overlay map[string]interface{}) map[string]interface{} {
	if len(base) == 0 {
		return normalizeMergeMap(overlay)
	}
	if len(overlay) == 0 {
		return base
	}
	for overlayKey, overlayValue := range overlay {
		baseKey, baseValue, exists := findFoldKey(base, overlayKey)
		if overlayValue == nil {
			if exists {
				delete(base, baseKey)
			}
			continue
		}
		if !exists {
			base[overlayKey] = overlayValue
			continue
		}
		baseMap, baseIsMap := baseValue.(map[string]interface{})
		overlayMap, overlayIsMap := overlayValue.(map[string]interface{})
		if baseIsMap && overlayIsMap {
			base[overlayKey] = mergeMergeMapsMasked(baseMap, overlayMap)
			if baseKey != overlayKey {
				delete(base, baseKey)
			}
			continue
		}
		base[overlayKey] = overlayValue
		if baseKey != overlayKey {
			delete(base, baseKey)
		}
	}
	return base
}

// layeredConfigDocument is the result of merging every present config layer.
type layeredConfigDocument struct {
	// Layers is the full candidate stack with absolute paths for present files.
	Layers []ConfigLayer
	// Merged is the merged document and MergedYAML its YAML encoding.
	Merged     map[string]interface{}
	MergedYAML []byte
	// Origins maps a merged key path ("skills_runtime.config_file") to the
	// layer kind that supplied the effective value.
	Origins map[string]string
	// OriginFiles maps the same key paths to the layer file, which write routing
	// uses to keep edits in the layer that owns the value.
	OriginFiles map[string]string
	// FallbackKeys are keys the single-file behaviour would not have seen:
	// they come from a layer below the highest present one.
	FallbackKeys []string
	// OverriddenKeys are keys defined by more than one layer where the highest
	// present layer wins.
	OverriddenKeys []string
	// SourcePath is the highest present layer (the file that "wins").
	SourcePath string
}

type configLayerDocument struct {
	layer  ConfigLayer
	values map[string]interface{}
}

// loadLayeredConfigDocument merges every present layer, expanding ${VAR}
// placeholders per layer before merging (the same pre-decode point the
// single-file loader uses).
func loadLayeredConfigDocument() (*layeredConfigDocument, error) {
	return loadLayeredDocumentFor(ConfigLayerStack())
}

// loadLayeredDocumentFor merges every present layer of the given stack,
// expanding ${VAR} placeholders per layer before merging (the same pre-decode
// point the single-file loader uses).
func loadLayeredDocumentFor(layers []ConfigLayer) (*layeredConfigDocument, error) {
	documents := make([]configLayerDocument, 0, len(layers))
	merged := map[string]interface{}{}

	for index := range layers {
		if !layers[index].Present {
			continue
		}
		absolutePath := filepath.Clean(layers[index].Path)
		if resolved, err := filepath.Abs(layers[index].Path); err == nil && resolved != "" {
			absolutePath = resolved
		}
		data, err := os.ReadFile(absolutePath)
		if err != nil {
			if os.IsNotExist(err) {
				layers[index].Present = false
				continue
			}
			return nil, fmt.Errorf("failed to read config layer %s: %w", absolutePath, err)
		}
		values, err := yamlToMergeMap([]byte(expandEnvVars(string(data))))
		if err != nil {
			return nil, fmt.Errorf("failed to parse config layer %s: %w", absolutePath, err)
		}
		layers[index].Path = absolutePath
		documents = append(documents, configLayerDocument{layer: layers[index], values: values})
		merged = mergeMergeMapsMasked(merged, values)
	}

	mergedYAML, err := yaml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("failed to encode merged config: %w", err)
	}

	originIndexes := collectConfigOrigins(merged, documents)
	origins := make(map[string]string, len(originIndexes))
	originFiles := make(map[string]string, len(originIndexes))
	fallbackKeys := make([]string, 0, len(originIndexes))
	overriddenKeys := make([]string, 0)
	highest := len(documents) - 1
	for path, index := range originIndexes {
		origins[path] = string(documents[index].layer.Kind)
		originFiles[path] = documents[index].layer.Path
		if index < highest {
			fallbackKeys = append(fallbackKeys, path)
			continue
		}
		if documentsDefinePath(documents[:index], strings.Split(path, ".")) {
			overriddenKeys = append(overriddenKeys, path)
		}
	}
	sort.Strings(fallbackKeys)
	sort.Strings(overriddenKeys)

	document := &layeredConfigDocument{
		Layers:         layers,
		Merged:         merged,
		MergedYAML:     mergedYAML,
		Origins:        origins,
		OriginFiles:    originFiles,
		FallbackKeys:   fallbackKeys,
		OverriddenKeys: overriddenKeys,
	}
	if len(documents) > 0 {
		document.SourcePath = documents[len(documents)-1].layer.Path
	}
	return document, nil
}

// collectConfigOrigins walks the merged document and records, for every leaf
// key path, the highest layer index that defines it. Keys removed by a masking
// null are absent from the merged document and therefore carry no origin.
func collectConfigOrigins(merged map[string]interface{}, documents []configLayerDocument) map[string]int {
	origins := map[string]int{}
	var walk func(segments []string, node map[string]interface{})
	walk = func(segments []string, node map[string]interface{}) {
		for key, value := range node {
			path := make([]string, 0, len(segments)+1)
			path = append(path, segments...)
			path = append(path, key)
			if child, ok := value.(map[string]interface{}); ok {
				walk(path, child)
				continue
			}
			for index := len(documents) - 1; index >= 0; index-- {
				if documentDefinesPath(documents[index], path) {
					origins[strings.Join(path, ".")] = index
					break
				}
			}
		}
	}
	walk(nil, merged)
	return origins
}

// documentDefinesPath reports whether the document defines the dotted path,
// folding key case the same way the merge does.
func documentDefinesPath(document configLayerDocument, segments []string) bool {
	node := document.values
	for position, segment := range segments {
		_, value, ok := findFoldKey(node, segment)
		if !ok {
			return false
		}
		if position == len(segments)-1 {
			return true
		}
		child, isMap := value.(map[string]interface{})
		if !isMap {
			return false
		}
		node = child
	}
	return false
}

func documentsDefinePath(documents []configLayerDocument, segments []string) bool {
	for _, document := range documents {
		if documentDefinesPath(document, segments) {
			return true
		}
	}
	return false
}

// InitGlobalConfigLayered loads the bootstrap config honouring
// AICLI_CONFIG_MERGE. resolvedPath is the bootstrap path the caller picked for
// single-file mode; explicitPath is the --config value, which short-circuits
// layering so an explicit selection always means exactly that file.
func InitGlobalConfigLayered(resolvedPath, explicitPath string) (*Config, error) {
	mode := MergeModeFromEnv()
	if strings.TrimSpace(explicitPath) != "" || mode != MergeModeOn {
		cfg, err := InitGlobalConfig(resolvedPath)
		if err != nil {
			return nil, err
		}
		attachLayerObservability(cfg, mode)
		if mode == MergeModeDryRun {
			reportLayeredMergePreview()
		}
		return cfg, nil
	}
	return initGlobalConfigMergedLayers()
}

// initGlobalConfigMergedLayers is the MergeModeOn entry: every present layer is
// merged, validated once, and then the system preset layer is applied below the
// result exactly like the single-file path does.
func initGlobalConfigMergedLayers() (*Config, error) {
	document, err := loadLayeredConfigDocument()
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if len(bytes.TrimSpace(document.MergedYAML)) > 0 {
		if err := unmarshalYAML(document.MergedYAML, cfg); err != nil {
			return nil, fmt.Errorf("failed to decode merged config: %w", err)
		}
		if err := validateLoadedConfig(cfg); err != nil {
			return nil, fmt.Errorf("invalid merged config: %w", err)
		}
	}
	if merged, err := applySystemPresetLayer(document.MergedYAML, cfg); err != nil {
		return nil, err
	} else {
		cfg = merged
	}

	cfg.ConfigFilePath = document.SourcePath
	cfg.ConfigLayers = document.Layers
	cfg.ConfigOrigins = document.Origins
	cfg.ConfigOriginFiles = document.OriginFiles
	cfg.ConfigMergeMode = MergeModeOn
	globalConfig = cfg
	return cfg, nil
}

// attachLayerObservability records the candidate stack (and the mode) on an
// otherwise single-file load so doctor/UI surfaces can explain precedence.
func attachLayerObservability(cfg *Config, mode MergeMode) {
	if cfg == nil {
		return
	}
	layers := ConfigLayerStack()
	for index := range layers {
		if !layers[index].Present {
			continue
		}
		if resolved, err := filepath.Abs(layers[index].Path); err == nil && resolved != "" {
			layers[index].Path = resolved
		}
	}
	cfg.ConfigLayers = layers
	cfg.ConfigMergeMode = mode
}

// ReloadGlobalConfig re-loads configuration after an in-session mutation using
// the same precedence rules as startup.
//
// A path that belongs to the candidate layer stack is re-resolved as a full
// layered load (so editing one layer cannot drop keys contributed by another),
// while a custom path outside the stack keeps explicit single-file semantics.
func ReloadGlobalConfig(configPath string) (*Config, error) {
	trimmed := strings.TrimSpace(configPath)
	if trimmed != "" && !isConfigLayerPath(trimmed) {
		return InitGlobalConfigLayered(trimmed, trimmed)
	}
	return InitGlobalConfigLayered(trimmed, "")
}

// isConfigLayerPath reports whether path denotes one of the candidate layer
// files (compared after cleaning and absolutising, case-insensitively).
func isConfigLayerPath(path string) bool {
	cleaned := normalizeLayerPath(path)
	if cleaned == "" {
		return false
	}
	for _, layer := range ConfigLayerStack() {
		if candidate := normalizeLayerPath(layer.Path); candidate != "" && strings.EqualFold(candidate, cleaned) {
			return true
		}
	}
	return false
}

func normalizeLayerPath(path string) string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return ""
	}
	if absolute, err := filepath.Abs(cleaned); err == nil && absolute != "" {
		return absolute
	}
	return cleaned
}

// reportLayeredMergePreview logs what enabling the merge would change, without
// changing any behaviour (MergeModeDryRun).
func reportLayeredMergePreview() {
	document, err := loadLayeredConfigDocument()
	if err != nil {
		logger.Warn("Config merge preview failed", logger.String("error", err.Error()))
		return
	}
	if len(document.FallbackKeys) == 0 && len(document.OverriddenKeys) == 0 {
		logger.Info("Config merge preview: layered merge would not change any key")
		return
	}
	logger.Info("Config merge preview (AICLI_CONFIG_MERGE=dry-run; behaviour unchanged)",
		logger.String("layers", describeConfigLayers(document.Layers)),
		logger.String("fallback_keys", joinCappedKeys(document.FallbackKeys)),
		logger.String("fallback_key_count", fmt.Sprintf("%d", len(document.FallbackKeys))),
		logger.String("overridden_key_count", fmt.Sprintf("%d", len(document.OverriddenKeys))),
	)
}

func describeConfigLayers(layers []ConfigLayer) string {
	parts := make([]string, 0, len(layers))
	for _, layer := range layers {
		if !layer.Present {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%s", layer.Kind, layer.Path))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " > ")
}

func joinCappedKeys(keys []string) string {
	if len(keys) > dryRunReportKeyLimit {
		keys = keys[:dryRunReportKeyLimit]
	}
	return strings.Join(keys, ",")
}
