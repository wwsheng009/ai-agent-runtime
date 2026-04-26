package runtimeserver

import (
	"fmt"
	"os"
	"strings"
)

type effectiveConfigDocument struct {
	Raw               []byte
	Parsed            interface{}
	SourcePath        string
	SnapshotRecovered bool
}

var sparseProviderRecoveryKeys = []string{"model_capabilities", "supports_max_output_tokens"}

func loadEffectiveConfigDocument(
	basePath string,
	snapshotPath string,
	format string,
) (*effectiveConfigDocument, error) {
	basePath = strings.TrimSpace(basePath)
	snapshotPath = strings.TrimSpace(snapshotPath)

	if snapshotPath == "" || !fileExists(snapshotPath) {
		raw, err := readConfigDocumentBytes(basePath)
		if err != nil {
			return nil, err
		}
		parsed, err := parseConfigDocumentValue(raw, format)
		if err != nil {
			return nil, err
		}
		return &effectiveConfigDocument{
			Raw:        raw,
			Parsed:     parsed,
			SourcePath: basePath,
		}, nil
	}

	snapshotRaw, err := readConfigDocumentBytes(snapshotPath)
	if err != nil {
		return nil, err
	}
	snapshotParsed, err := parseConfigDocumentValue(snapshotRaw, format)
	if err != nil {
		return nil, err
	}

	baseRaw, err := readConfigDocumentBytes(basePath)
	if err != nil {
		return nil, err
	}
	baseParsed, err := parseConfigDocumentValue(baseRaw, format)
	if err != nil {
		return nil, err
	}

	if !shouldRecoverSparseSnapshot(baseParsed, snapshotParsed) {
		return &effectiveConfigDocument{
			Raw:        snapshotRaw,
			Parsed:     snapshotParsed,
			SourcePath: snapshotPath,
		}, nil
	}

	normalizedSnapshotParsed := normalizeSparseSnapshotOverlay(baseParsed, snapshotParsed)
	mergedParsed := mergeConfigDocumentValues(baseParsed, normalizedSnapshotParsed)
	mergedRaw, err := marshalConfigDocumentValue(mergedParsed, format)
	if err != nil {
		return nil, err
	}

	return &effectiveConfigDocument{
		Raw:               normalizeDocumentBytes(mergedRaw, format),
		Parsed:            mergedParsed,
		SourcePath:        snapshotPath,
		SnapshotRecovered: true,
	}, nil
}

func readConfigDocumentBytes(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config document: %w", err)
	}
	return raw, nil
}

func shouldRecoverSparseSnapshot(baseParsed, snapshotParsed interface{}) bool {
	baseRoot, baseOK := baseParsed.(map[string]interface{})
	snapshotRoot, snapshotOK := snapshotParsed.(map[string]interface{})
	if !baseOK || !snapshotOK || len(baseRoot) == 0 {
		return false
	}

	if len(snapshotRoot) == 0 {
		return true
	}

	if configDocumentHasProviderItems(baseRoot) && !configDocumentHasProviderItems(snapshotRoot) {
		return true
	}
	if providerItemsNeedSparseRecovery(baseRoot, snapshotRoot) {
		return true
	}

	return len(baseRoot) >= 3 && len(snapshotRoot) == 1
}

func configDocumentHasProviderItems(root map[string]interface{}) bool {
	return len(configDocumentProviderItems(root)) > 0
}

func configDocumentProviderItems(root map[string]interface{}) map[string]interface{} {
	providers, ok := root["providers"].(map[string]interface{})
	if !ok || providers == nil {
		return nil
	}
	items, ok := providers["items"].(map[string]interface{})
	if !ok || len(items) == 0 {
		return nil
	}
	return items
}

func providerItemsNeedSparseRecovery(baseRoot, snapshotRoot map[string]interface{}) bool {
	baseItems := configDocumentProviderItems(baseRoot)
	snapshotItems := configDocumentProviderItems(snapshotRoot)
	if len(baseItems) == 0 || len(snapshotItems) == 0 {
		return false
	}

	for name, baseValue := range baseItems {
		baseItem, baseOK := baseValue.(map[string]interface{})
		snapshotValue, hasSnapshot := snapshotItems[name]
		if !baseOK || !hasSnapshot {
			continue
		}
		snapshotItem, snapshotOK := snapshotValue.(map[string]interface{})
		if !snapshotOK {
			snapshotItem = nil
		}
		if providerItemNeedsSparseRecovery(baseItem, snapshotItem) {
			return true
		}
	}
	return false
}

func providerItemNeedsSparseRecovery(baseItem, snapshotItem map[string]interface{}) bool {
	if len(baseItem) == 0 {
		return false
	}

	for _, key := range sparseProviderRecoveryKeys {
		baseValue, baseHasKey := baseItem[key]
		if !baseHasKey || configDocumentValueEffectivelyEmpty(baseValue) {
			continue
		}
		snapshotValue, snapshotHasKey := snapshotItem[key]
		if !snapshotHasKey || configDocumentValueEffectivelyEmpty(snapshotValue) {
			return true
		}
	}
	return false
}

func normalizeSparseSnapshotOverlay(baseParsed, snapshotParsed interface{}) interface{} {
	baseRoot, baseOK := baseParsed.(map[string]interface{})
	snapshotRoot, snapshotOK := normalizeConfigDocumentValue(snapshotParsed).(map[string]interface{})
	if !baseOK || !snapshotOK {
		return snapshotParsed
	}

	baseItems := configDocumentProviderItems(baseRoot)
	snapshotItems := configDocumentProviderItems(snapshotRoot)
	if len(baseItems) == 0 || snapshotItems == nil {
		return snapshotRoot
	}

	for name, baseValue := range baseItems {
		baseItem, baseOK := baseValue.(map[string]interface{})
		snapshotValue, hasSnapshot := snapshotItems[name]
		if !baseOK || !hasSnapshot {
			continue
		}

		snapshotItem, snapshotOK := snapshotValue.(map[string]interface{})
		if !snapshotOK || snapshotItem == nil {
			if providerItemNeedsSparseRecovery(baseItem, nil) {
				snapshotItems[name] = map[string]interface{}{}
			}
			continue
		}

		for _, key := range sparseProviderRecoveryKeys {
			baseField, baseHasKey := baseItem[key]
			if !baseHasKey || configDocumentValueEffectivelyEmpty(baseField) {
				continue
			}
			if snapshotField, snapshotHasKey := snapshotItem[key]; snapshotHasKey && configDocumentValueEffectivelyEmpty(snapshotField) {
				delete(snapshotItem, key)
			}
		}
	}

	return snapshotRoot
}

func configDocumentValueEffectivelyEmpty(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []interface{}:
		return len(typed) == 0
	case map[string]interface{}:
		return len(typed) == 0
	default:
		return false
	}
}

func mergeConfigDocumentValues(base, overlay interface{}) interface{} {
	base = normalizeConfigDocumentValue(base)
	overlay = normalizeConfigDocumentValue(overlay)

	overlayMap, overlayIsMap := overlay.(map[string]interface{})
	if !overlayIsMap {
		return overlay
	}

	baseMap, _ := base.(map[string]interface{})
	result := make(map[string]interface{}, len(baseMap)+len(overlayMap))
	for key, value := range baseMap {
		result[key] = normalizeConfigDocumentValue(value)
	}
	for key, value := range overlayMap {
		if baseChild, ok := baseMap[key]; ok {
			result[key] = mergeConfigDocumentValues(baseChild, value)
			continue
		}
		result[key] = normalizeConfigDocumentValue(value)
	}
	return result
}
