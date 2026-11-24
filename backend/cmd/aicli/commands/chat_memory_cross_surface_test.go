package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	memorytools "github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

func TestMemoryCrossSurfaceSameCorpus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("memory:\n  mode: on\n  tools:\n    enabled: true\n"), 0o644))
	session := &ChatSession{WorkspaceAnchor: root, RuntimeConfigPath: configPath}

	store, err := openChatProjectMemoryStore(session)
	require.NoError(t, err)
	manual, err := store.Append(memorystore.AppendNoteOptions{
		Text: "cross surface memory manual decision", Kind: "decision",
		Tags: []string{"alpha", "beta"},
	})
	require.NoError(t, err)

	activate := func(key, text, kind string, tags []string) string {
		t.Helper()
		candidate, insertErr := store.InsertCandidate(context.Background(), memorystore.Candidate{
			SessionID: "cross-surface", Kind: kind, Text: text, Tags: tags,
			Confidence: 0.9, SourceHash: key, SourceRevision: 1,
		})
		require.NoError(t, insertErr)
		active := memorystore.StatusActive
		record, updateErr := store.UpdateRecord(context.Background(), candidate.ID,
			memorystore.RecordPatch{Status: &active})
		require.NoError(t, updateErr)
		return record.ID
	}
	factBoth := activate("cross-surface-fact", "cross surface memory automatic fact", "fact", []string{"alpha", "beta"})
	decisionBeta := activate("cross-surface-decision", "cross surface memory automatic decision", "decision", []string{"beta"})

	canonical, err := store.SearchRecords(context.Background(), memorystore.RecordSearchQuery{
		Status: memorystore.StatusActive, Text: "cross surface memory",
		Kinds: []string{"fact"}, Tags: []string{"beta"}, Limit: 20,
	})
	require.NoError(t, err)
	expectedIDs := make([]string, 0, len(canonical.Items))
	for _, hit := range canonical.Items {
		expectedIDs = append(expectedIDs, hit.Record.ID)
	}
	require.ElementsMatch(t, []string{factBoth}, expectedIDs)
	require.NoError(t, store.Close())

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = root
	cfg.Memory.Mode = "on"
	cfg.Memory.Injection.Enabled = true
	handler := runtimeapi.NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetRuntimeConfig(cfg, configPath)
	handler.SetAdminAuthRequired(true)
	handler.SetAdminToken("memory-admin")
	t.Cleanup(func() { _ = handler.CloseMemoryRuntime() })
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	values := url.Values{}
	values.Set("q", "cross surface memory")
	values.Add("kind", "fact")
	values.Add("tag", "beta")
	values.Set("limit", "20")
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?"+values.Encode(), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var apiResponse struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&apiResponse))
	apiIDs := make([]string, 0, len(apiResponse.Items))
	for _, item := range apiResponse.Items {
		apiIDs = append(apiIDs, item.ID)
	}
	require.ElementsMatch(t, expectedIDs, apiIDs)

	result, handled, err := tryExecuteStructuredChatCommand(session,
		"/memory search cross surface memory --kind fact --tag beta")
	require.NoError(t, err)
	require.True(t, handled)
	cliText := ui.RenderDocumentPlain(result.Document())
	for _, id := range expectedIDs {
		require.Contains(t, cliText, id)
	}
	require.NotContains(t, cliText, manual.ID)
	require.NotContains(t, cliText, decisionBeta)

	toolCfg := runtimecfg.DefaultRuntimeConfig()
	toolCfg.Workspace.Root = root
	toolCfg.Memory.Mode = "on"
	toolCfg.Memory.Tools.Enabled = true
	toolResult, err := memorytools.NewMemorySearchTool(toolCfg).Execute(
		toolctx.WithSessionID(context.Background(), "cross-surface"),
		map[string]interface{}{
			"query": "cross surface memory",
			"kinds": []interface{}{"fact"},
			"tags":  []interface{}{"beta"},
			"limit": 20,
		})
	require.NoError(t, err)
	require.True(t, toolResult.Success, toolResult.Content)
	var toolResponse struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResult.Content), &toolResponse))
	toolIDs := make([]string, 0, len(toolResponse.Items))
	for _, item := range toolResponse.Items {
		toolIDs = append(toolIDs, item.ID)
	}
	require.ElementsMatch(t, expectedIDs, toolIDs)
}
