package runtimeserver

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
)

func TestLocalConfigDocumentServiceLoadAndSaveRaw(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := []byte("server:\n  host: 127.0.0.1\nproviders:\n  default_provider: test\n")
	require.NoError(t, os.WriteFile(configPath, initial, 0o644))

	service := NewLocalConfigDocumentService(configPath)
	require.NotNil(t, service)

	document, err := service.LoadDocument()
	require.NoError(t, err)
	require.Equal(t, "yaml", document.Format)
	require.NotEmpty(t, document.Sections)
	require.Equal(t, resolveAbsolutePath(configPath), document.Path)

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

	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "0.0.0.0")

	backupPath := requireSingleBackupFile(t, configPath)
	backupRaw, err := os.ReadFile(backupPath)
	require.NoError(t, err)
	require.Equal(t, string(initial), string(backupRaw))
}

func TestLocalConfigDocumentServiceSaveStructured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  port: 8101\n"), 0o644))

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
	require.Equal(t, resolveAbsolutePath(configPath), document.Path)
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

	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "default_provider: openai")
	require.Contains(t, string(raw), "base_url: https://api.openai.com")
	require.Contains(t, string(raw), "http://127.0.0.1:10810")
}

func TestLocalConfigDocumentServicePreviewDoesNotPersist(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := "server:\n  host: 127.0.0.1\n"
	require.NoError(t, os.WriteFile(configPath, []byte(initial), 0o644))

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
	matches, err := filepath.Glob(configPath + ".*.bak")
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestLocalConfigDocumentServiceIgnoresSnapshotFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  host: base.local\n"), 0o644))

	snapshotPath := filepath.Join(filepath.Dir(configPath), "config.runtime.snapshot.yaml")
	require.NoError(t, os.WriteFile(snapshotPath, []byte("server:\n  host: snapshot.local\n"), 0o644))

	service := NewLocalConfigDocumentService(configPath)
	require.NotNil(t, service)

	document, err := service.LoadDocument()
	require.NoError(t, err)
	require.Equal(t, resolveAbsolutePath(configPath), document.Path)
	require.Contains(t, document.Raw, "base.local")
	require.NotContains(t, document.Raw, "snapshot.local")
}

func ptrToString(value string) *string {
	return &value
}

func requireSingleBackupFile(t *testing.T, configPath string) string {
	t.Helper()
	matches, err := filepath.Glob(configPath + ".*.bak")
	require.NoError(t, err)
	require.NotEmpty(t, matches)
	sort.Strings(matches)
	return matches[len(matches)-1]
}
