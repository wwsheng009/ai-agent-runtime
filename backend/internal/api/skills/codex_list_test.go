package skills

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestListCodexSkills_ReturnsGroupedSkillsAndErrors(t *testing.T) {
	baseRoot := isolatedCodexSkillsTestRoot(t)
	homeRoot := filepath.Join(baseRoot, "home")
	repoRoot := filepath.Join(baseRoot, "workspace", "repo")
	configRoot := filepath.Join(baseRoot, "config")

	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", homeRoot)

	validSkillDir := filepath.Join(repoRoot, "skills", "alpha")
	invalidSkillDir := filepath.Join(repoRoot, "skills", "broken")
	require.NoError(t, os.MkdirAll(validSkillDir, 0o755))
	require.NoError(t, os.MkdirAll(invalidSkillDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(homeRoot, ".aicli", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(homeRoot, ".aicli", "agents", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(configRoot, "skills"), 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(validSkillDir, "SKILL.md"), []byte(`---
name: alpha
description: alpha skill
metadata:
  short-description: concise alpha
---
Alpha body.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(invalidSkillDir, "SKILL.md"), []byte("not a codex frontmatter"), 0o644))

	configFile := filepath.Join(configRoot, "runtime.yaml")
	outcome := skill.DiscoverCodexSkillLoadOutcome(repoRoot, configFile, nil)
	require.Len(t, outcome.Skills, 1)
	require.Len(t, outcome.Errors, 1)
	assert.Equal(t, skill.CodexSkillScopeRepo, outcome.Skills[0].Scope)
	assert.Equal(t, filepath.Clean(filepath.Join(validSkillDir, "SKILL.md")), outcome.Skills[0].PathToSkillsMD)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	reqBody := map[string]interface{}{
		"cwds":         []string{repoRoot},
		"config_file":  configFile,
		"force_reload": true,
	}
	body, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/list", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Results []struct {
			Cwd    string                   `json:"cwd"`
			Roots  []string                 `json:"roots"`
			Skills []map[string]interface{} `json:"skills"`
			Errors []map[string]interface{} `json:"errors"`
			Count  int                      `json:"count"`
		} `json:"results"`
		Count       int  `json:"count"`
		GroupCount  int  `json:"group_count"`
		ForceReload bool `json:"force_reload"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.True(t, payload.ForceReload)
	require.Equal(t, 1, payload.GroupCount)
	require.Equal(t, 1, payload.Count)
	require.Len(t, payload.Results, 1)

	group := payload.Results[0]
	assert.Equal(t, filepath.Clean(repoRoot), filepath.Clean(group.Cwd))
	assert.Equal(t, 1, group.Count)
	assert.Len(t, group.Skills, 1)
	assert.Len(t, group.Errors, 1)
	assert.Contains(t, group.Roots, filepath.Clean(filepath.Join(repoRoot, "skills")))
	assert.Contains(t, group.Roots, filepath.Clean(filepath.Join(homeRoot, ".aicli", "skills")))
	assert.Contains(t, group.Roots, filepath.Clean(filepath.Join(homeRoot, ".aicli", "agents", "skills")))
	assert.Contains(t, group.Roots, filepath.Clean(filepath.Join(configRoot, "skills")))

	skillItem := group.Skills[0]
	assert.Equal(t, "alpha", skillItem["name"])
	assert.Equal(t, skill.CodexSkillScopeRepo, skillItem["scope"])

	errorItem := group.Errors[0]
	assert.Contains(t, errorItem["path"].(string), filepath.Clean(filepath.Join(invalidSkillDir, "SKILL.md")))
}

func isolatedCodexSkillsTestRoot(t *testing.T) string {
	t.Helper()

	if runtime.GOOS != "windows" {
		return t.TempDir()
	}

	drive := filepath.VolumeName(os.TempDir())
	if drive == "" {
		drive = "C:"
	}

	root := filepath.Join(drive+string(filepath.Separator), "codex-skill-test", sanitizeCodexSkillsTestComponent(t.Name()))
	require.NoError(t, os.RemoveAll(root))
	require.NoError(t, os.MkdirAll(root, 0o755))
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
	})
	return root
}

func sanitizeCodexSkillsTestComponent(name string) string {
	replacer := strings.NewReplacer("\\", "_", "/", "_", ":", "_", " ", "_")
	return replacer.Replace(name)
}
