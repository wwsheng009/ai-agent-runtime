package runtimeserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	"gopkg.in/yaml.v3"
)

var configDocumentEnvPattern = regexp.MustCompile(`\$\{([^}:]+)(:-([^}]*))?\}`)

type LocalConfigDocumentService struct {
	baseConfigPath string
	snapshotPath   string
	hotReloader    ConfigDocumentHotReloader
}

func NewLocalConfigDocumentService(configPath string) *LocalConfigDocumentService {
	info := ResolveAgentConfigSnapshotInfo(configPath)
	if strings.TrimSpace(info.BasePath) == "" && strings.TrimSpace(info.SnapshotPath) == "" {
		return nil
	}
	return &LocalConfigDocumentService{
		baseConfigPath: info.BasePath,
		snapshotPath:   info.SnapshotPath,
	}
}

func (s *LocalConfigDocumentService) SetHotReloader(hotReloader ConfigDocumentHotReloader) {
	if s == nil {
		return
	}
	s.hotReloader = hotReloader
}

func (s *LocalConfigDocumentService) LoadDocument() (*skillsapi.ConfigDocument, error) {
	documentPath := s.documentPath()
	if s == nil || strings.TrimSpace(documentPath) == "" {
		return nil, fmt.Errorf("config path is required")
	}

	format := detectConfigDocumentFormat(documentPath)
	effectiveDocument, err := s.loadEffectiveDocument(format)
	if err != nil {
		return nil, err
	}

	raw := effectiveDocument.Raw
	parsed := effectiveDocument.Parsed
	warnings := append(
		buildConfigDocumentWarnings(nil, nil, nil),
		s.snapshotWarning(effectiveDocument.SourcePath, effectiveDocument.SnapshotRecovered)...,
	)
	warnings = append(warnings,
		"结构化保存会重新序列化整个文档，注释和手工排版可能会丢失；原始 YAML 模式更适合保留注释。",
	)

	doc := &skillsapi.ConfigDocument{
		Path:                   resolveAbsolutePath(documentPath),
		Format:                 format,
		Raw:                    string(raw),
		Parsed:                 parsed,
		Sections:               summarizeConfigSections(parsed),
		SizeBytes:              len(raw),
		RestartRequired:        false,
		SupportsStructuredSave: true,
		Warnings:               warnings,
	}

	if info, statErr := os.Stat(effectiveDocument.SourcePath); statErr == nil {
		doc.UpdatedAt = info.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00")
	}

	return doc, nil
}

func (s *LocalConfigDocumentService) PreviewDocument(
	req skillsapi.ConfigDocumentSaveRequest,
) (*skillsapi.ConfigDocument, error) {
	documentPath := s.documentPath()
	if s == nil || strings.TrimSpace(documentPath) == "" {
		return nil, fmt.Errorf("config path is required")
	}

	format := detectConfigDocumentFormat(documentPath)
	currentDocument, err := s.loadEffectiveDocument(format)
	if err != nil {
		return nil, err
	}

	content, mergedSparseStructured, err := s.resolveDocumentBytesWithCurrent(
		req,
		format,
		currentDocument.Parsed,
	)
	if err != nil {
		return nil, err
	}
	if err := validateConfigDocument(content, format); err != nil {
		return nil, err
	}

	parsed, err := parseConfigDocumentValue(content, format)
	if err != nil {
		return nil, err
	}

	currentParsed := currentDocument.Parsed
	impact := analyzeConfigDocumentRuntimeImpact(currentParsed, parsed)

	warnings := append([]string{
		"这是预览结果，尚未写入磁盘。",
	}, buildConfigDocumentWarnings(impact, nil, nil)...)
	if mergedSparseStructured {
		warnings = append(warnings,
			"检测到本次 structured 草稿只包含局部节点，预览结果已自动按当前有效配置补齐其余未提交部分。",
		)
	}
	warnings = append(warnings,
		s.snapshotWarning(
			currentDocument.SourcePath,
			currentDocument.SnapshotRecovered,
		)...,
	)
	warnings = append(warnings,
		"结构化保存会重新序列化整个文档，注释和手工排版可能会丢失；原始 YAML 模式更适合保留注释。",
	)

	return &skillsapi.ConfigDocument{
		Path:                   resolveAbsolutePath(documentPath),
		Format:                 format,
		Raw:                    string(content),
		Parsed:                 parsed,
		Sections:               summarizeConfigSections(parsed),
		SizeBytes:              len(content),
		RestartRequired:        impact != nil && len(impact.RestartRequiredPaths) > 0,
		SupportsStructuredSave: true,
		RuntimeImpact:          impact,
		Warnings:               warnings,
	}, nil
}

func (s *LocalConfigDocumentService) SaveDocument(req skillsapi.ConfigDocumentSaveRequest) (*skillsapi.ConfigDocument, error) {
	documentPath := s.documentPath()
	if s == nil || strings.TrimSpace(documentPath) == "" {
		return nil, fmt.Errorf("config path is required")
	}

	format := detectConfigDocumentFormat(documentPath)
	currentDocument, err := s.loadEffectiveDocument(format)
	if err != nil {
		return nil, err
	}
	currentParsed := currentDocument.Parsed

	content, mergedSparseStructured, err := s.resolveDocumentBytesWithCurrent(
		req,
		format,
		currentDocument.Parsed,
	)
	if err != nil {
		return nil, err
	}

	if err := validateConfigDocument(content, format); err != nil {
		return nil, err
	}
	nextParsed, err := parseConfigDocumentValue(content, format)
	if err != nil {
		return nil, err
	}
	nextCfg, err := decodeConfigDocumentAgentConfig(content, format)
	if err != nil {
		return nil, err
	}
	impact := analyzeConfigDocumentRuntimeImpact(currentParsed, nextParsed)
	if err := writeFilePreserveMode(documentPath, content); err != nil {
		return nil, err
	}

	doc, err := s.LoadDocument()
	if err != nil {
		return nil, err
	}
	doc.RuntimeImpact = impact
	doc.RestartRequired = impact != nil && len(impact.RestartRequiredPaths) > 0

	applyWarnings := make([]string, 0, 1)
	hotReloadPaths := []string(nil)
	if impact != nil {
		hotReloadPaths = impact.HotReloadPaths
	}
	if s.hotReloader != nil {
		hotReloadResult := s.hotReloader.Apply(nextCfg, hotReloadPaths)
		if impact != nil {
			impact.AppliedPaths = hotReloadResult.AppliedPaths
		}
		applyWarnings = append(applyWarnings, hotReloadResult.Warnings...)
	} else if impact != nil && len(impact.HotReloadPaths) > 0 {
		applyWarnings = append(applyWarnings,
			"当前服务未接入热重载执行器，可热重载变更尚未自动应用到运行中进程。")
	}
	var appliedPaths []string
	if impact != nil {
		appliedPaths = impact.AppliedPaths
	}
	doc.Warnings = buildConfigDocumentWarnings(impact, appliedPaths, applyWarnings)
	if mergedSparseStructured {
		doc.Warnings = append(doc.Warnings,
			"检测到本次 structured 保存只包含局部节点，已自动与当前有效配置合并后写入，避免覆盖整份运行时配置。",
		)
	}
	doc.Warnings = append(doc.Warnings,
		s.snapshotWarning(
			currentDocument.SourcePath,
			currentDocument.SnapshotRecovered,
		)...,
	)
	if !strings.EqualFold(strings.TrimSpace(req.Mode), "raw") {
		doc.Warnings = append(doc.Warnings,
			"结构化保存会重新序列化整个文档，注释和手工排版可能会丢失；原始 YAML 模式更适合保留注释。",
		)
	}
	return doc, nil
}

func (s *LocalConfigDocumentService) documentPath() string {
	if s == nil {
		return ""
	}
	if strings.TrimSpace(s.snapshotPath) != "" {
		return strings.TrimSpace(s.snapshotPath)
	}
	return strings.TrimSpace(s.baseConfigPath)
}

func (s *LocalConfigDocumentService) currentSourcePath() string {
	if s == nil {
		return ""
	}
	if strings.TrimSpace(s.snapshotPath) != "" && fileExists(s.snapshotPath) {
		return strings.TrimSpace(s.snapshotPath)
	}
	return strings.TrimSpace(s.baseConfigPath)
}

func (s *LocalConfigDocumentService) loadEffectiveDocument(
	format string,
) (*effectiveConfigDocument, error) {
	if s == nil {
		return nil, fmt.Errorf("config path is required")
	}
	return loadEffectiveConfigDocument(s.baseConfigPath, s.snapshotPath, format)
}

func (s *LocalConfigDocumentService) loadCurrentDocumentBytes() ([]byte, string, error) {
	sourcePath := s.currentSourcePath()
	if strings.TrimSpace(sourcePath) == "" {
		return nil, "", fmt.Errorf("config path is required")
	}

	raw, err := os.ReadFile(sourcePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("read config document: %w", err)
	}
	return raw, sourcePath, nil
}

func (s *LocalConfigDocumentService) snapshotWarning(sourcePath string, recovered bool) []string {
	if s == nil || strings.TrimSpace(s.snapshotPath) == "" {
		return nil
	}

	warnings := []string{
		fmt.Sprintf("运行时配置页面读写的是快照文件，启动配置仅作为初始种子: %s",
			resolveAbsolutePath(s.snapshotPath)),
	}
	if !sameConfigPath(sourcePath, s.snapshotPath) {
		warnings = append(warnings,
			fmt.Sprintf("当前尚未生成运行时快照，展示内容来自启动配置；首次保存后将写入快照文件。源文件: %s",
				resolveAbsolutePath(sourcePath)),
		)
	}
	if recovered {
		warnings = append(warnings,
			fmt.Sprintf("检测到运行时快照只包含局部节点，当前展示的是启动配置与快照自动合成后的有效配置；下次保存会修复快照文件。快照: %s",
				resolveAbsolutePath(s.snapshotPath)),
		)
	}
	return warnings
}

func (s *LocalConfigDocumentService) resolveDocumentBytes(
	req skillsapi.ConfigDocumentSaveRequest,
	format string,
) ([]byte, error) {
	if req.Raw != nil {
		return normalizeDocumentBytes([]byte(*req.Raw), format), nil
	}
	if req.Parsed != nil {
		content, err := marshalConfigDocumentValue(req.Parsed, format)
		if err != nil {
			return nil, err
		}
		return normalizeDocumentBytes(content, format), nil
	}
	return nil, fmt.Errorf("raw or parsed config content is required")
}

func (s *LocalConfigDocumentService) resolveDocumentBytesWithCurrent(
	req skillsapi.ConfigDocumentSaveRequest,
	format string,
	currentParsed interface{},
) ([]byte, bool, error) {
	if req.Raw != nil {
		content, err := s.resolveDocumentBytes(req, format)
		return content, false, err
	}
	if req.Parsed == nil {
		return nil, false, fmt.Errorf("raw or parsed config content is required")
	}

	normalizedParsed := normalizeConfigDocumentValue(req.Parsed)
	mergedSparseStructured := shouldRecoverSparseSnapshot(
		currentParsed,
		normalizedParsed,
	)
	if mergedSparseStructured {
		normalizedParsed = mergeConfigDocumentValues(currentParsed, normalizedParsed)
	}

	content, err := marshalConfigDocumentValue(normalizedParsed, format)
	if err != nil {
		return nil, false, err
	}
	return normalizeDocumentBytes(content, format), mergedSparseStructured, nil
}

func detectConfigDocumentFormat(path string) string {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".json":
		return "json"
	default:
		return "yaml"
	}
}

func parseConfigDocumentValue(raw []byte, format string) (interface{}, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]interface{}{}, nil
	}

	var decoded interface{}
	switch format {
	case "json":
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("parse config json: %w", err)
		}
	default:
		if err := yaml.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("parse config yaml: %w", err)
		}
	}

	normalized := normalizeConfigDocumentValue(decoded)
	if normalized == nil {
		return map[string]interface{}{}, nil
	}
	return normalized, nil
}

func marshalConfigDocumentValue(value interface{}, format string) ([]byte, error) {
	normalized := normalizeConfigDocumentValue(value)
	switch format {
	case "json":
		output, err := json.MarshalIndent(normalized, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode config json: %w", err)
		}
		return output, nil
	default:
		output, err := yaml.Marshal(normalized)
		if err != nil {
			return nil, fmt.Errorf("encode config yaml: %w", err)
		}
		return output, nil
	}
}

func normalizeDocumentBytes(content []byte, format string) []byte {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return []byte{}
	}
	if format == "json" {
		return append(content, '\n')
	}
	return append(content, '\n')
}

func validateConfigDocument(content []byte, format string) error {
	if _, err := parseConfigDocumentValue(content, format); err != nil {
		return err
	}
	if _, err := decodeConfigDocumentAgentConfig(content, format); err != nil {
		return err
	}
	return nil
}

func decodeConfigDocumentAgentConfig(content []byte, format string) (*agentconfig.Config, error) {
	expanded := []byte(expandConfigDocumentEnvVars(string(content)))
	cfg := &agentconfig.Config{}
	switch format {
	case "json":
		if err := json.Unmarshal(expanded, cfg); err != nil {
			return nil, fmt.Errorf("validate runtime config json: %w", err)
		}
	default:
		if err := yaml.Unmarshal(expanded, cfg); err != nil {
			return nil, fmt.Errorf("validate runtime config yaml: %w", err)
		}
	}
	return cfg, nil
}

func expandConfigDocumentEnvVars(content string) string {
	return configDocumentEnvPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := configDocumentEnvPattern.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		if value := os.Getenv(parts[1]); value != "" {
			return value
		}
		if len(parts) >= 4 && parts[3] != "" {
			return parts[3]
		}
		return match
	})
}

func normalizeConfigDocumentValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]interface{}:
		normalized := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			normalized[key] = normalizeConfigDocumentValue(child)
		}
		return normalized
	case map[interface{}]interface{}:
		normalized := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			normalized[fmt.Sprint(key)] = normalizeConfigDocumentValue(child)
		}
		return normalized
	case []interface{}:
		normalized := make([]interface{}, 0, len(typed))
		for _, child := range typed {
			normalized = append(normalized, normalizeConfigDocumentValue(child))
		}
		return normalized
	default:
		return typed
	}
}

func summarizeConfigSections(value interface{}) []skillsapi.ConfigDocumentSection {
	root, ok := value.(map[string]interface{})
	if !ok || len(root) == 0 {
		return nil
	}

	keys := make([]string, 0, len(root))
	for key := range root {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	sections := make([]skillsapi.ConfigDocumentSection, 0, len(keys))
	for _, key := range keys {
		child := root[key]
		section := skillsapi.ConfigDocumentSection{
			Key:  key,
			Kind: configDocumentValueKind(child),
		}
		switch typed := child.(type) {
		case map[string]interface{}:
			section.ItemCount = len(typed)
		case []interface{}:
			section.ItemCount = len(typed)
		}
		sections = append(sections, section)
	}
	return sections
}

func configDocumentValueKind(value interface{}) string {
	switch value.(type) {
	case map[string]interface{}:
		return "object"
	case []interface{}:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int8, int16, int32, int64, float32, float64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

func resolveAbsolutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolutePath, err := filepath.Abs(path); err == nil {
		return absolutePath
	}
	return path
}

func sameConfigPath(left, right string) bool {
	left = resolveAbsolutePath(left)
	right = resolveAbsolutePath(right)
	return left != "" && right != "" && strings.EqualFold(left, right)
}

var _ skillsapi.ConfigDocumentService = (*LocalConfigDocumentService)(nil)
