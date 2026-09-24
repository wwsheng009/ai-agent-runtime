package agentconfig

import (
	"bytes"
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"
)

// ApplyConfigOverlayYAML deep-merges an overlay document (for example a profile
// runtime.overrides fragment) on top of an already-decoded config and returns a
// fresh *Config plus whether the overlay actually changed anything.
//
// Composition mirrors ApplyMatchingSystemPresets: encode the live config to a
// document, merge with MergeConfigYAML, decode once, validate once. Two
// properties matter for session-scoped overlays:
//
//   - base is never mutated, so the process config (and any caller-held
//     baseline) stays intact;
//   - a no-op overlay returns (nil, false, nil) instead of a re-encoded clone,
//     which lets callers keep using base verbatim (NFR-1: sessions without
//     overrides must behave bit-for-bit like before).
//
// The yaml:"-" bookkeeping fields are carried over from base, because losing
// them would silently break config write-back routing and doctor output.
func ApplyConfigOverlayYAML(base *Config, overlayYAML []byte) (*Config, bool, error) {
	if base == nil || len(bytes.TrimSpace(overlayYAML)) == 0 {
		return nil, false, nil
	}
	baseYAML, err := yaml.Marshal(base)
	if err != nil {
		return nil, false, fmt.Errorf("encode config for overlay merge: %w", err)
	}
	mergedYAML, err := MergeConfigYAML(baseYAML, overlayYAML)
	if err != nil {
		return nil, false, fmt.Errorf("merge config overlay: %w", err)
	}
	// Byte comparison is unreliable here: MergeConfigYAML re-encodes a map, so
	// even a no-op overlay changes key order and quoting. Compare the
	// normalised documents instead.
	baseMap, err := yamlToMergeMap(baseYAML)
	if err != nil {
		return nil, false, fmt.Errorf("parse base document for overlay merge: %w", err)
	}
	mergedMap, err := yamlToMergeMap(mergedYAML)
	if err != nil {
		return nil, false, fmt.Errorf("parse merged document for overlay merge: %w", err)
	}
	if reflect.DeepEqual(baseMap, mergedMap) {
		return nil, false, nil
	}
	merged := &Config{}
	if err := unmarshalYAML(mergedYAML, merged); err != nil {
		return nil, false, fmt.Errorf("decode config overlay: %w", err)
	}
	if err := validateLoadedConfig(merged); err != nil {
		return nil, false, fmt.Errorf("invalid config overlay: %w", err)
	}
	CopyConfigRuntimeMetadata(merged, base)
	return merged, true, nil
}

// CopyConfigRuntimeMetadata carries the yaml:"-" bookkeeping fields (write
// target, layer observability, merge mode) from base onto cfg. Overlays decode
// a fresh *Config, and these fields never participate in YAML decoding.
func CopyConfigRuntimeMetadata(cfg, base *Config) {
	if cfg == nil || base == nil {
		return
	}
	cfg.ConfigFilePath = base.ConfigFilePath
	cfg.ConfigLayers = base.ConfigLayers
	cfg.ConfigOrigins = base.ConfigOrigins
	cfg.ConfigOriginFiles = base.ConfigOriginFiles
	cfg.ConfigMergeMode = base.ConfigMergeMode
}
