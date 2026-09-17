package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// MergedConfigDocument is the exported, read-only view of a layered load. It is
// what runtime-server serves over GET /config/document when layered merging is
// active, so the CLI and the web surface describe the same effective config.
type MergedConfigDocument struct {
	Merged         map[string]interface{}
	MergedYAML     []byte
	Layers         []ConfigLayer
	PresentLayers  []ConfigLayer
	Origins        map[string]string
	OriginFiles    map[string]string
	FallbackKeys   []string
	OverriddenKeys []string
	SourcePath     string
	// DefaultWriteLayer receives new keys and writes redirected away from
	// read-only layers. Nil for documents that predate it.
	DefaultWriteLayer *ConfigLayer
}

// LoadMergedRuntimeConfigDocument merges the runtime.yaml stack. runtime.yaml
// has its own schema, so callers decode MergedYAML themselves; the layer and
// origin metadata is identical to the agent-config document.
func LoadMergedRuntimeConfigDocument() (*MergedConfigDocument, error) {
	document, err := loadLayeredDocumentFor(RuntimeConfigLayerStack())
	if err != nil {
		return nil, err
	}
	present := make([]ConfigLayer, 0, len(document.Layers))
	for _, layer := range document.Layers {
		if layer.Present {
			present = append(present, layer)
		}
	}
	merged := &MergedConfigDocument{
		Merged:         document.Merged,
		MergedYAML:     document.MergedYAML,
		Layers:         document.Layers,
		PresentLayers:  present,
		Origins:        document.Origins,
		OriginFiles:    document.OriginFiles,
		FallbackKeys:   document.FallbackKeys,
		OverriddenKeys: document.OverriddenKeys,
		SourcePath:     document.SourcePath,
	}
	if layer, ok := WritableLayer(document.Layers); ok || strings.TrimSpace(layer.Path) != "" {
		merged.DefaultWriteLayer = &layer
	}
	return merged, nil
}

// LoadMergedConfigDocument performs a layered load without touching the global
// config singleton, so callers such as the runtime-server can recompute the
// effective document on every request.
func LoadMergedConfigDocument() (*MergedConfigDocument, error) {
	document, err := loadLayeredConfigDocument()
	if err != nil {
		return nil, err
	}
	present := make([]ConfigLayer, 0, len(document.Layers))
	for _, layer := range document.Layers {
		if layer.Present {
			present = append(present, layer)
		}
	}
	return &MergedConfigDocument{
		Merged:         document.Merged,
		MergedYAML:     document.MergedYAML,
		Layers:         document.Layers,
		PresentLayers:  present,
		Origins:        document.Origins,
		OriginFiles:    document.OriginFiles,
		FallbackKeys:   document.FallbackKeys,
		OverriddenKeys: document.OverriddenKeys,
		SourcePath:     document.SourcePath,
	}, nil
}

// MultiLayer reports whether more than one layer exists on disk. Single-layer
// setups keep byte-identical single-file behaviour everywhere.
func (m *MergedConfigDocument) MultiLayer() bool {
	return m != nil && len(m.PresentLayers) > 1
}

// DocumentChange is one leaf-level edit to apply to a layer file.
type DocumentChange struct {
	Path   []string
	Value  interface{}
	Delete bool
}

// ApplyMergedDocumentChanges writes the difference between the current merged
// document and updated back into the layers that own each changed key, and
// returns the number of applied edits.
//
// Rules (design §7 / D3):
//   - a changed key is written to the layer that supplied its effective value;
//   - a key that disappears from updated is written as an explicit null in that
//     layer, which masks the lower layers without deleting them;
//   - keys with no recorded origin fall back to fallbackPath (the highest
//     present layer, i.e. "new keys go to the highest writable layer");
//   - untouched keys are not rewritten, so `${VAR}` placeholders and manual
//     formatting survive in the files we do not edit.
func ApplyMergedDocumentChanges(merged *MergedConfigDocument, updated interface{}, fallbackPath string) (int, error) {
	if merged == nil {
		return 0, fmt.Errorf("merged config document is required")
	}
	updatedMap, ok := normalizeDocumentValue(updated).(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("updated config document must be a mapping")
	}

	changes := make([]DocumentChange, 0, 16)
	collectDocumentChanges(merged.Merged, updatedMap, nil, &changes)
	if len(changes) == 0 {
		return 0, nil
	}

	groups := make(map[string][]DocumentChange, len(changes))
	order := make([]string, 0, len(changes))
	for _, change := range changes {
		target := merged.targetFileFor(strings.Join(change.Path, "."), fallbackPath)
		if strings.TrimSpace(target) == "" {
			return 0, fmt.Errorf("no writable layer for config key %s", strings.Join(change.Path, "."))
		}
		if _, seen := groups[target]; !seen {
			order = append(order, target)
		}
		groups[target] = append(groups[target], change)
	}

	applied := 0
	for _, target := range order {
		if err := applyDocumentChanges(target, groups[target]); err != nil {
			return applied, err
		}
		applied += len(groups[target])
	}
	return applied, nil
}

// targetFileFor resolves the layer file a change belongs to, defaulting to the
// highest present layer for keys that do not exist yet.
func (m *MergedConfigDocument) targetFileFor(dottedPath, fallback string) string {
	router := &Config{
		ConfigLayers:      m.Layers,
		ConfigOriginFiles: m.OriginFiles,
		ConfigMergeMode:   MergeModeOn,
	}
	if target := router.WriteTargetForKeys(dottedPath); target != "" && !m.isReadOnlyLayer(target) {
		return target
	}
	// Brand-new keys and keys owned by a read-only (portable) layer both go to
	// the default writable layer, so edits never dirty shipped/repository files.
	if m.DefaultWriteLayer != nil && strings.TrimSpace(m.DefaultWriteLayer.Path) != "" {
		return strings.TrimSpace(m.DefaultWriteLayer.Path)
	}
	return strings.TrimSpace(fallback)
}

// isReadOnlyLayer reports whether path belongs to a read-only layer of the stack.
func (m *MergedConfigDocument) isReadOnlyLayer(path string) bool {
	cleaned := cleanLayerComparisonPath(path)
	if cleaned == "" {
		return false
	}
	for _, layer := range m.Layers {
		if layer.ReadOnly && cleanLayerComparisonPath(layer.Path) == cleaned {
			return true
		}
	}
	return false
}

func cleanLayerComparisonPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if absolute, err := filepath.Abs(trimmed); err == nil && absolute != "" {
		trimmed = absolute
	}
	return filepath.Clean(trimmed)
}

// lookupDocumentPath resolves a dotted key path inside a merged document.
func lookupDocumentPath(m map[string]interface{}, segments []string) (interface{}, bool) {
	var current interface{} = m
	for _, segment := range segments {
		asMap, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		_, value, exists := findFoldKey(asMap, segment)
		if !exists {
			return nil, false
		}
		current = value
	}
	return current, true
}

// SetDocumentValue writes one dotted key into a document map, creating
// intermediate mappings as needed. A nil value marks the key for deletion
// (written as an explicit null so lower layers stay masked).
func SetDocumentValue(document map[string]interface{}, dottedPath string, value interface{}) {
	if document == nil {
		return
	}
	setDocumentPath(document, strings.Split(strings.TrimSpace(dottedPath), "."), value, value == nil)
}

// ApplyDocumentPathChange writes a single dotted key back to the layer that owns
// it; read-only origins and new keys go to fallbackPath. It is the single-key
// variant of ApplyMergedDocumentChanges for persistence hooks that already know
// which key they touched, and it is a no-op when the value is unchanged.
func (m *MergedConfigDocument) ApplyDocumentPathChange(dottedPath string, value interface{}, fallbackPath string) (int, error) {
	if m == nil {
		return 0, fmt.Errorf("merged config document is required")
	}
	trimmed := strings.TrimSpace(dottedPath)
	if trimmed == "" {
		return 0, fmt.Errorf("config key is required")
	}
	if current, ok := lookupDocumentPath(m.Merged, strings.Split(trimmed, ".")); ok {
		if reflect.DeepEqual(normalizeDocumentValue(current), normalizeDocumentValue(value)) {
			return 0, nil
		}
	}
	target := m.targetFileFor(trimmed, fallbackPath)
	if strings.TrimSpace(target) == "" {
		return 0, fmt.Errorf("no writable layer for config key %s", trimmed)
	}
	change := DocumentChange{
		Path:   strings.Split(trimmed, "."),
		Value:  value,
		Delete: value == nil,
	}
	if err := applyDocumentChanges(target, []DocumentChange{change}); err != nil {
		return 0, err
	}
	return 1, nil
}

// OriginKindFor returns the layer kind that supplied a merged key path ("" when
// the key has no recorded origin).
func (m *MergedConfigDocument) OriginKindFor(key string) string {
	if m == nil || len(m.Origins) == 0 {
		return ""
	}
	router := &Config{
		ConfigLayers:      m.Layers,
		ConfigOriginFiles: m.OriginFiles,
		ConfigMergeMode:   MergeModeOn,
	}
	path, ok := router.originPathFor(key)
	if !ok {
		return ""
	}
	return m.Origins[path]
}

// WriteLayerKindFor returns the layer kind a write for key would land in: the
// origin layer when the key exists, otherwise the highest present layer
// (matching the fallback ApplyMergedDocumentChanges uses for new keys). This is
// what the config UI uses to attribute a pending change (design §9 H4).
func (m *MergedConfigDocument) WriteLayerKindFor(key string) string {
	if kind := m.OriginKindFor(key); kind != "" {
		// A read-only origin is only a default: the write still lands in the
		// writable layer, and the attribution must say so.
		if origin := m.OriginFiles[key]; origin == "" || !m.isReadOnlyLayer(origin) {
			return kind
		}
		if m.DefaultWriteLayer != nil && strings.TrimSpace(string(m.DefaultWriteLayer.Kind)) != "" {
			return string(m.DefaultWriteLayer.Kind)
		}
		return kind
	}
	if m.DefaultWriteLayer != nil && strings.TrimSpace(string(m.DefaultWriteLayer.Kind)) != "" {
		return string(m.DefaultWriteLayer.Kind)
	}
	if m == nil || len(m.PresentLayers) == 0 {
		return ""
	}
	return string(m.PresentLayers[len(m.PresentLayers)-1].Kind)
}

// collectDocumentChanges walks both documents and records set/delete edits.
func collectDocumentChanges(current, updated map[string]interface{}, prefix []string, out *[]DocumentChange) {
	for key, updatedValue := range updated {
		path := appendPath(prefix, key)
		currentValue, exists := lookupDocumentValue(current, key)
		updatedNormalized := normalizeDocumentValue(updatedValue)
		if !exists {
			*out = append(*out, DocumentChange{Path: path, Value: updatedNormalized})
			continue
		}
		currentMap, currentIsMap := currentValue.(map[string]interface{})
		updatedMap, updatedIsMap := updatedNormalized.(map[string]interface{})
		if currentIsMap && updatedIsMap {
			collectDocumentChanges(currentMap, updatedMap, path, out)
			continue
		}
		if !reflect.DeepEqual(normalizeDocumentValue(currentValue), updatedNormalized) {
			*out = append(*out, DocumentChange{Path: path, Value: updatedNormalized})
		}
	}
	for key, currentValue := range current {
		if _, exists := lookupDocumentValue(updated, key); exists {
			continue
		}
		// A removed key becomes an explicit null in the layer that owned it, so
		// lower layers stay untouched but stop contributing the value.
		_ = currentValue
		*out = append(*out, DocumentChange{Path: appendPath(prefix, key), Delete: true})
	}
}

func appendPath(prefix []string, key string) []string {
	path := make([]string, 0, len(prefix)+1)
	path = append(path, prefix...)
	return append(path, key)
}

// applyDocumentChanges applies edits to one layer file. The file is parsed
// without environment expansion so untouched `${VAR}` placeholders survive.
func applyDocumentChanges(file string, changes []DocumentChange) error {
	// The writable target may not exist yet (fresh installation: the first edit
	// creates $HOME/.aicli/runtime.yaml), so make sure its directory exists.
	if dir := filepath.Dir(file); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config layer directory %s: %w", dir, err)
		}
	}
	root := map[string]interface{}{}
	raw, err := os.ReadFile(file)
	switch {
	case err == nil:
		if len(bytes.TrimSpace(raw)) > 0 {
			decoded, parseErr := yamlToMergeMap(raw)
			if parseErr != nil {
				return fmt.Errorf("parse config layer %s: %w", file, parseErr)
			}
			if decoded == nil {
				return fmt.Errorf("config layer %s is not a mapping", file)
			}
			root = decoded
		}
	case os.IsNotExist(err):
	default:
		return fmt.Errorf("read config layer %s: %w", file, err)
	}

	for _, change := range changes {
		setDocumentPath(root, change.Path, change.Value, change.Delete)
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("encode config layer %s: %w", file, err)
	}
	if err := writeFileAtomic(file, out); err != nil {
		return err
	}
	return nil
}

// setDocumentPath writes (or masks, when remove is true) one path in a decoded
// layer document, creating intermediate mappings as needed and reusing the
// existing key spelling so no duplicate keys are introduced.
func setDocumentPath(root map[string]interface{}, path []string, value interface{}, remove bool) {
	if len(path) == 0 {
		return
	}
	node := root
	for _, segment := range path[:len(path)-1] {
		existing, ok := lookupDocumentValue(node, segment)
		child, isMap := existing.(map[string]interface{})
		if !ok || !isMap {
			child = map[string]interface{}{}
			node[segment] = child
		}
		node = child
	}
	last := path[len(path)-1]
	if spelling, ok := lookupDocumentKey(node, last); ok {
		last = spelling
	}
	if remove {
		node[last] = nil
		return
	}
	node[last] = value
}

// lookupDocumentValue finds a key case-insensitively, mirroring the merge
// helper's folding behaviour.
func lookupDocumentValue(m map[string]interface{}, key string) (interface{}, bool) {
	if value, ok := m[key]; ok {
		return value, true
	}
	lowered := strings.ToLower(key)
	for existing, value := range m {
		if strings.ToLower(existing) == lowered {
			return value, true
		}
	}
	return nil, false
}

func lookupDocumentKey(m map[string]interface{}, key string) (string, bool) {
	if _, ok := m[key]; ok {
		return key, true
	}
	lowered := strings.ToLower(key)
	for existing := range m {
		if strings.ToLower(existing) == lowered {
			return existing, true
		}
	}
	return "", false
}

// normalizeDocumentValue converts decoded YAML containers into the plain
// map[string]interface{}/[]interface{} shapes used across this package.
func normalizeDocumentValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			out[key] = normalizeDocumentValue(child)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			out[fmt.Sprint(key)] = normalizeDocumentValue(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, child := range typed {
			out = append(out, normalizeDocumentValue(child))
		}
		return out
	default:
		return value
	}
}
