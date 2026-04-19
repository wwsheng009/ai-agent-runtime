package runtimeserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
)

func TestLocalConfigDocumentServiceLoadAndSaveRaw(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := []byte("server:\n  host: 127.0.0.1\nproviders:\n  default_provider: test\n")
	require.NoError(t, os.WriteFile(configPath, initial, 0o644))
	snapshotPath := ResolveAgentConfigSnapshotInfo(configPath).SnapshotPath

	service := NewLocalConfigDocumentService(configPath)
	require.NotNil(t, service)

	document, err := service.LoadDocument()
	require.NoError(t, err)
	require.Equal(t, "yaml", document.Format)
	require.NotEmpty(t, document.Sections)

	updated := "server:\n  host: 0.0.0.0\nproviders:\n  default_provider: updated\n"
	document, err = service.SaveDocument(skillsapi.ConfigDocumentSaveRequest{
		Raw:  &updated,
		Mode: "raw",
	})
	require.NoError(t, err)

	root, ok := document.Parsed.(map[string]interface{})
	require.True(t, ok)
	server, ok := root["server"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "0.0.0.0", server["host"])

	raw, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "0.0.0.0")

	baseRaw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Equal(t, string(initial), string(baseRaw))
}

func TestLocalConfigDocumentServiceSaveStructured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  port: 8101\n"), 0o644))
	snapshotPath := ResolveAgentConfigSnapshotInfo(configPath).SnapshotPath

	service := NewLocalConfigDocumentService(configPath)
	require.NotNil(t, service)

	document, err := service.SaveDocument(skillsapi.ConfigDocumentSaveRequest{
		Mode: "structured",
		Parsed: map[string]interface{}{
			"server": map[string]interface{}{
				"host": "127.0.0.1",
				"port": 8102,
			},
			"providers": map[string]interface{}{
				"default_provider": "codex_fox",
			},
		},
	})
	require.NoError(t, err)
	require.Contains(t, document.Raw, "codex_fox")
	require.Contains(t, document.Raw, "127.0.0.1")
	require.Equal(t, resolveAbsolutePath(snapshotPath), document.Path)
}

func TestLocalConfigDocumentServiceSaveStructuredMergesSparseParsedPayload(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := `
server:
  host: 127.0.0.1
providers:
  default_provider: openai
  items:
    openai:
      base_url: https://api.openai.com
`
	require.NoError(t, os.WriteFile(configPath, []byte(initial), 0o644))
	snapshotPath := ResolveAgentConfigSnapshotInfo(configPath).SnapshotPath

	service := NewLocalConfigDocumentService(configPath)
	require.NotNil(t, service)

	document, err := service.SaveDocument(skillsapi.ConfigDocumentSaveRequest{
		Mode: "structured",
		Parsed: map[string]interface{}{
			"providers": map[string]interface{}{
				"proxy": map[string]interface{}{
					"enabled":  true,
					"http":     "http://127.0.0.1:10810",
					"https":    "http://127.0.0.1:10810",
					"no_proxy": "localhost,127.0.0.1",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Contains(t, document.Raw, "127.0.0.1")
	require.Contains(t, document.Raw, "default_provider: openai")
	require.Contains(t, document.Raw, "items:")
	require.Contains(t, document.Raw, "base_url: https://api.openai.com")
	require.Contains(t, document.Raw, "http://127.0.0.1:10810")
	require.Condition(t, func() bool {
		for _, warning := range document.Warnings {
			if strings.Contains(warning, "structured 保存只包含局部节点") {
				return true
			}
		}
		return false
	})

	raw, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "default_provider: openai")
	require.Contains(t, string(raw), "base_url: https://api.openai.com")
	require.Contains(t, string(raw), "http://127.0.0.1:10810")
}

func TestLocalConfigDocumentServicePreviewDoesNotPersist(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := "server:\n  host: 127.0.0.1\n"
	require.NoError(t, os.WriteFile(configPath, []byte(initial), 0o644))
	snapshotPath := ResolveAgentConfigSnapshotInfo(configPath).SnapshotPath

	service := NewLocalConfigDocumentService(configPath)
	require.NotNil(t, service)

	preview, err := service.PreviewDocument(skillsapi.ConfigDocumentSaveRequest{
		Raw:  ptrToString("server:\n  host: 0.0.0.0\n"),
		Mode: "raw",
	})
	require.NoError(t, err)
	require.Contains(t, preview.Raw, "0.0.0.0")

	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Equal(t, initial, string(raw))
	_, err = os.Stat(snapshotPath)
	require.True(t, os.IsNotExist(err))
}

func TestLocalConfigDocumentServiceLoadDocumentRecoversSparseSnapshot(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	baseConfig := `
server:
  host: 127.0.0.1
providers:
  default_provider: openai
  items:
    openai:
      base_url: https://api.openai.com
`
	require.NoError(t, os.WriteFile(configPath, []byte(baseConfig), 0o644))

	snapshotPath := ResolveAgentConfigSnapshotInfo(configPath).SnapshotPath
	snapshotConfig := `
providers:
  proxy:
    enabled: true
    http: http://127.0.0.1:10810
`
	require.NoError(t, os.WriteFile(snapshotPath, []byte(snapshotConfig), 0o644))

	service := NewLocalConfigDocumentService(configPath)
	require.NotNil(t, service)

	document, err := service.LoadDocument()
	require.NoError(t, err)
	require.Equal(t, resolveAbsolutePath(snapshotPath), document.Path)
	require.Contains(t, document.Raw, "127.0.0.1")
	require.Contains(t, document.Raw, "openai")
	require.Contains(t, document.Raw, "http://127.0.0.1:10810")
	require.Contains(t, document.Warnings, "结构化保存会重新序列化整个文档，注释和手工排版可能会丢失；原始 YAML 模式更适合保留注释。")
	require.Condition(t, func() bool {
		for _, warning := range document.Warnings {
			if strings.Contains(warning, "检测到运行时快照只包含局部节点") {
				return true
			}
		}
		return false
	})

	root, ok := document.Parsed.(map[string]interface{})
	require.True(t, ok)
	server, ok := root["server"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "127.0.0.1", server["host"])
	providers, ok := root["providers"].(map[string]interface{})
	require.True(t, ok)
	items, ok := providers["items"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, items, "openai")
	proxy, ok := providers["proxy"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "http://127.0.0.1:10810", proxy["http"])
}

func ptrToString(value string) *string {
	return &value
}
