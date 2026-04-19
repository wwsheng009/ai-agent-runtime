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

	mergedParsed := mergeConfigDocumentValues(baseParsed, snapshotParsed)
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

	return len(baseRoot) >= 3 && len(snapshotRoot) == 1
}

func configDocumentHasProviderItems(root map[string]interface{}) bool {
	providers, ok := root["providers"].(map[string]interface{})
	if !ok || providers == nil {
		return false
	}
	items, ok := providers["items"].(map[string]interface{})
	return ok && len(items) > 0
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
