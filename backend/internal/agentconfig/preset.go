package agentconfig

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// System preset configuration: administrator- or user-deployed baseline
// configs that are deep-merged under the user config. Presets may be matched
// per hostname so one deployment can serve multiple machines (for example a
// corporate gateway proxy on *.corp.example.com and plain direct access
// elsewhere).
//
// Resolution order (lowest to highest precedence):
//
//	built-in (embedded presets.yaml)
//	< system preset directory files (sorted by name)
//	< ~/.aicli/presets.yaml
//	< user config.yaml
//
// The built-in presets ship inside the binary and are copied to
// ~/.aicli/presets.yaml on startup when that file is missing (see
// EnsureUserPresetsFile), so users get an editable default layer.
//
// Merging operates on the YAML-document level: only keys that were
// explicitly written in a layer participate, so zero-value Go struct fields
// never shadow values from a lower-precedence layer.

//go:embed presets.yaml
var embeddedPresetsYAML []byte

// PresetFile is the top-level YAML shape for a preset file.
type PresetFile struct {
	Presets []Preset `yaml:"presets"`
}

// PresetMatch holds the conditions that gate whether a preset applies.
// An empty Hostname matches every host.
type PresetMatch struct {
	// Hostname is a case-insensitive glob (path.Match style: * ?) or an
	// exact hostname. Empty matches all hosts.
	Hostname string `yaml:"hostname" mapstructure:"hostname"`
}

// Preset is a named configuration layer merged into the final config when it
// is enabled and its match conditions hold.
type Preset struct {
	Name    string      `yaml:"name" mapstructure:"name"`
	Enabled *bool       `yaml:"enabled" mapstructure:"enabled"`
	Match   PresetMatch `yaml:"match" mapstructure:"match"`
	// Config is the configuration fragment to merge, using the same schema
	// as the user config.yaml. It stays a raw map so that only explicitly
	// written keys participate in the merge. Missing fields are left
	// untouched.
	Config map[string]interface{} `yaml:"config" mapstructure:"config"`
}

// IsEnabled reports whether the preset participates in merging. Unset
// Enabled means enabled (true); explicit false disables it.
func (p Preset) IsEnabled() bool {
	return p.Enabled == nil || *p.Enabled
}

// DecodedConfig decodes the preset's config fragment into a Config struct
// for inspection. Zero-value fields are not meaningful here because the
// fragment is merged at the YAML level.
func (p Preset) DecodedConfig() (*Config, error) {
	if len(p.Config) == 0 {
		return &Config{}, nil
	}
	raw, err := yaml.Marshal(p.Config)
	if err != nil {
		return nil, err
	}
	var out Config
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PresetMatchesHost reports whether the preset's hostname condition accepts
// the given host. An empty preset hostname matches every host; an empty
// host matches only presets without a hostname condition.
func PresetMatchesHost(p Preset, host string) bool {
	pattern := strings.TrimSpace(strings.ToLower(p.Match.Hostname))
	if pattern == "" {
		return true
	}
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return false
	}
	if matched, err := path.Match(pattern, host); err == nil && matched {
		return true
	}
	return pattern == host
}

// SystemPresetDirEnv overrides the system preset directory.
const SystemPresetDirEnv = "AICLI_SYSTEM_CONFIG_DIR"

// SystemPresetFileName is the canonical preset file inside the system preset
// directory (optional; presets/*.yaml are also scanned).
const SystemPresetFileName = "presets.yaml"

// SystemPresetDir resolves the system preset directory: the AICLI_SYSTEM_CONFIG_DIR
// environment variable when set, otherwise the platform default
// (C:\ProgramData\aicli on Windows, /etc/aicli elsewhere).
func SystemPresetDir() string {
	if dir := strings.TrimSpace(os.Getenv(SystemPresetDirEnv)); dir != "" {
		return dir
	}
	if pd := strings.TrimSpace(os.Getenv("ProgramData")); pd != "" && runtime.GOOS == "windows" {
		return filepath.Join(pd, "aicli")
	}
	return "/etc/aicli"
}

// UserPresetsFileName is the per-user preset file under ~/.aicli. It is
// seeded from the embedded presets on first startup and participates in the
// preset layer between system directory files and the user config.
const UserPresetsFileName = "presets.yaml"

// UserPresetsPath returns the per-user preset file path (~/.aicli/presets.yaml).
func UserPresetsPath() string {
	home, err := userHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".aicli", UserPresetsFileName)
}

// EnsureUserPresetsFile copies the embedded presets to ~/.aicli/presets.yaml
// when that file does not exist. It is called during aicli and runtime-server
// startup so users always have an editable, documented default preset layer.
// An existing file is never overwritten.
func EnsureUserPresetsFile() (string, bool, error) {
	path := UserPresetsPath()
	if path == "" {
		return "", false, nil
	}
	if fileExists(path) {
		return path, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, fmt.Errorf("create user presets directory %s: %w", filepath.Dir(path), err)
	}
	if err := writeFileAtomic(path, embeddedPresetsYAML); err != nil {
		return "", false, fmt.Errorf("write user presets file %s: %w", path, err)
	}
	return path, true, nil
}

// LoadSystemPresets loads the built-in default presets merged with every
// preset file under the system preset directory and the per-user preset
// file. Files are applied in sorted order within each directory; a later
// definition with the same name replaces the earlier one field-by-field
// (deep merge), so a file may enable a built-in preset by redeclaring it
// with enabled: true.
func LoadSystemPresets() ([]Preset, error) {
	presets, err := parsePresetYAML(embeddedPresetsYAML)
	if err != nil {
		return nil, fmt.Errorf("parse built-in system presets: %w", err)
	}

	dir := SystemPresetDir()
	candidates := make([]string, 0, 8)
	if file := filepath.Join(dir, SystemPresetFileName); fileExists(file) {
		candidates = append(candidates, file)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "presets")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".yaml") {
				continue
			}
			candidates = append(candidates, filepath.Join(dir, "presets", entry.Name()))
		}
	}
	sort.Strings(candidates)
	for _, file := range candidates {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read system presets %s: %w", file, err)
		}
		filePresets, err := parsePresetYAML([]byte(expandEnvVars(string(raw))))
		if err != nil {
			return nil, fmt.Errorf("parse system presets %s: %w", file, err)
		}
		for _, filePreset := range filePresets {
			presets = mergePresetIntoList(presets, filePreset)
		}
	}

	if userFile := UserPresetsPath(); userFile != "" && fileExists(userFile) {
		raw, err := os.ReadFile(userFile)
		if err != nil {
			return nil, fmt.Errorf("read user presets %s: %w", userFile, err)
		}
		userPresets, err := parsePresetYAML([]byte(expandEnvVars(string(raw))))
		if err != nil {
			return nil, fmt.Errorf("parse user presets %s: %w", userFile, err)
		}
		for _, userPreset := range userPresets {
			presets = mergePresetIntoList(presets, userPreset)
		}
	}
	return presets, nil
}

func parsePresetYAML(data []byte) ([]Preset, error) {
	var file PresetFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	for index := range file.Presets {
		if file.Presets[index].Config != nil {
			file.Presets[index].Config = normalizeMergeValue(file.Presets[index].Config).(map[string]interface{})
		}
	}
	return file.Presets, nil
}

// mergePresetIntoList merges overlay into the existing preset with the same
// name (deep merge), appending it when no match exists.
func mergePresetIntoList(presets []Preset, overlay Preset) []Preset {
	for index := range presets {
		if presets[index].Name != overlay.Name {
			continue
		}
		merged := presets[index]
		if overlay.Enabled != nil {
			merged.Enabled = overlay.Enabled
		}
		if overlay.Match.Hostname != "" {
			merged.Match.Hostname = overlay.Match.Hostname
		}
		if overlay.Config != nil {
			merged.Config = mergeMergeMaps(merged.Config, overlay.Config)
		}
		presets[index] = merged
		return presets
	}
	return append(presets, overlay)
}

// systemHostname is swappable in tests, mirroring the userHomeDir pattern.
var systemHostname = os.Hostname

// MergeWithPresets merges the user config document with the preset baseline
// (built-in + system directory + ~/.aicli/presets.yaml, filtered by hostname
// match) and returns the merged YAML document. When no preset layer is
// active (no system preset directory and no user preset file), nil is
// returned and the caller should keep the user config unchanged.
func MergeWithPresets(userYAML []byte) ([]byte, error) {
	if !presetsActive() {
		return nil, nil
	}
	host, err := systemHostname()
	if err != nil {
		host = ""
	}
	baseline, err := MergeSystemPresetBaseline(host)
	if err != nil {
		return nil, err
	}
	mergedYAML, err := MergeConfigYAML(baseline, userYAML)
	if err != nil {
		return nil, err
	}
	return mergedYAML, nil
}

// presetsActive reports whether any preset layer beyond the binary defaults
// is deployed: a per-user presets file or preset files under the system
// preset directory. An empty system directory does not activate merging.
func presetsActive() bool {
	if fileExists(UserPresetsPath()) {
		return true
	}
	dir := SystemPresetDir()
	if dir == "" {
		return false
	}
	if fileExists(filepath.Join(dir, SystemPresetFileName)) {
		return true
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "presets")); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".yaml") {
				return true
			}
		}
	}
	return false
}

// MergeSystemPresetBaseline merges every enabled preset whose hostname
// condition matches the given host into a single Config-level YAML document.
func MergeSystemPresetBaseline(host string) ([]byte, error) {
	presets, err := LoadSystemPresets()
	if err != nil {
		return nil, err
	}
	var baseline []byte
	for _, preset := range presets {
		if !preset.IsEnabled() || len(preset.Config) == 0 || !PresetMatchesHost(preset, host) {
			continue
		}
		presetYAML, err := yaml.Marshal(preset.Config)
		if err != nil {
			return nil, fmt.Errorf("encode system preset %q: %w", preset.Name, err)
		}
		baseline, err = MergeConfigYAML(baseline, presetYAML)
		if err != nil {
			return nil, fmt.Errorf("merge system preset %q: %w", preset.Name, err)
		}
	}
	return baseline, nil
}

// ApplyMatchingSystemPresets merges every enabled preset whose hostname
// condition matches the given host into base, returning the merged config.
// User config is the overlay on top of the preset baseline; callers should
// start from an empty base and merge the user config afterwards.
func ApplyMatchingSystemPresets(base *Config, host string) (*Config, error) {
	baseline, err := MergeSystemPresetBaseline(host)
	if err != nil {
		return base, err
	}
	var merged Config
	if len(bytes.TrimSpace(baseline)) > 0 {
		if err := yaml.Unmarshal(baseline, &merged); err != nil {
			return base, fmt.Errorf("decode system preset baseline: %w", err)
		}
	}
	if base != nil {
		overlay, err := yaml.Marshal(base)
		if err != nil {
			return base, err
		}
		if len(bytes.TrimSpace(overlay)) > 0 {
			finalYAML, err := MergeConfigYAML(baseline, overlay)
			if err != nil {
				return base, err
			}
			if err := yaml.Unmarshal(finalYAML, &merged); err != nil {
				return base, fmt.Errorf("decode merged system preset baseline: %w", err)
			}
		}
	}
	return &merged, nil
}

// MergeConfigYAML deep-merges the overlay YAML document into the base YAML
// document at the key level: nested maps merge recursively, scalars and
// slices from the overlay replace base values. Only keys present in a layer
// participate, so a fragment never shadows lower-precedence values it did
// not explicitly set. Map keys differing only in case (for example a preset
// writing "Headers" and a user writing "headers") are treated as the same
// key and merged. Empty/nil inputs are treated as empty documents.
func MergeConfigYAML(baseYAML, overlayYAML []byte) ([]byte, error) {
	base, err := yamlToMergeMap(baseYAML)
	if err != nil {
		return nil, fmt.Errorf("parse base YAML for merge: %w", err)
	}
	overlay, err := yamlToMergeMap(overlayYAML)
	if err != nil {
		return nil, fmt.Errorf("parse overlay YAML for merge: %w", err)
	}
	merged := mergeMergeMaps(base, overlay)
	return yaml.Marshal(merged)
}

func yamlToMergeMap(data []byte) (map[string]interface{}, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]interface{}{}, nil
	}
	var out map[string]interface{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return normalizeMergeMap(out), nil
}

// normalizeMergeMap converts nested map[interface{}]interface{} values into
// map[string]interface{} forms and preserves slices. Key casing is kept
// intact: only field-name casing collisions matter during merge, and those
// are handled by fold-key matching.
func normalizeMergeMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = normalizeMergeValue(value)
	}
	return out
}

func normalizeMergeValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		return normalizeMergeMap(v)
	case map[interface{}]interface{}:
		converted := make(map[string]interface{}, len(v))
		for k, item := range v {
			converted[fmt.Sprintf("%v", k)] = normalizeMergeValue(item)
		}
		return converted
	case []interface{}:
		for index := range v {
			v[index] = normalizeMergeValue(v[index])
		}
		return v
	default:
		return value
	}
}

// foldKey returns the case-folded form used to detect key collisions.
func foldKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// mergeMergeMaps recursively merges overlay into base (overlay wins). Keys
// that collide case-insensitively (field-name variants such as "Headers" vs
// "headers") merge into the overlay's key spelling; data keys such as
// provider names and header names keep their original casing. The base map
// is mutated and returned.
func mergeMergeMaps(base, overlay map[string]interface{}) map[string]interface{} {
	if len(base) == 0 {
		return normalizeMergeMap(overlay)
	}
	if len(overlay) == 0 {
		return base
	}
	for overlayKey, overlayValue := range overlay {
		baseKey, baseValue, exists := findFoldKey(base, overlayKey)
		if !exists {
			base[overlayKey] = overlayValue
			continue
		}
		baseMap, baseIsMap := baseValue.(map[string]interface{})
		overlayMap, overlayIsMap := overlayValue.(map[string]interface{})
		switch {
		case baseIsMap && overlayIsMap:
			base[overlayKey] = mergeMergeMaps(baseMap, overlayMap)
			if baseKey != overlayKey {
				delete(base, baseKey)
			}
		default:
			base[overlayKey] = overlayValue
			if baseKey != overlayKey {
				delete(base, baseKey)
			}
		}
	}
	return base
}

// findFoldKey locates the base entry whose key collides case-insensitively
// with the given key, preferring an exact match.
func findFoldKey(base map[string]interface{}, key string) (string, interface{}, bool) {
	if value, exists := base[key]; exists {
		return key, value, true
	}
	folded := foldKey(key)
	for candidate, value := range base {
		if foldKey(candidate) == folded {
			return candidate, value, true
		}
	}
	return "", nil, false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
