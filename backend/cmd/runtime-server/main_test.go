package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestRuntimeInfoHandlerExposesBuildProvenance(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	runtimeInfoHandler(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("expected no-store cache policy, got %q", cacheControl)
	}
	var body struct {
		OK            bool                              `json:"ok"`
		Status        string                            `json:"status"`
		Service       string                            `json:"service"`
		ExecutionCore runtimechat.RuntimeCoreDescriptor `json:"execution_core"`
		Backend       struct {
			Version   string `json:"version"`
			GitCommit string `json:"git_commit"`
			BuildTime string `json:"build_time"`
		} `json:"backend"`
		Frontend struct {
			AssetManifestHash string `json:"asset_manifest_hash"`
			BuildTime         string `json:"build_time"`
			EntryAsset        string `json:"entry_asset"`
		} `json:"frontend"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if !body.OK || body.Status != "ok" || body.Service != "ai-agent-runtime" {
		t.Fatalf("unexpected health identity: %+v", body)
	}
	if !runtimechat.IsSessionActorRuntimeCore(body.ExecutionCore) {
		t.Fatalf("unexpected execution core: %+v", body.ExecutionCore)
	}
	if body.Backend.Version == "" || body.Backend.GitCommit == "" || body.Backend.BuildTime == "" {
		t.Fatalf("backend provenance is incomplete: %+v", body.Backend)
	}
	if body.Frontend.AssetManifestHash == "" || body.Frontend.BuildTime == "" || body.Frontend.EntryAsset == "" {
		t.Fatalf("frontend provenance is incomplete: %+v", body.Frontend)
	}
}

func TestResolvePathFromConfigFile(t *testing.T) {
	configFile := filepath.Join("backend", "configs", "runtime.yaml")
	resolved := resolvePathFromConfigFile(configFile, "../data/runtime/sessions")

	expected := filepath.Clean(filepath.Join("backend", "data", "runtime", "sessions"))
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestResolveRuntimeServerSessionDir_UsesAICLIDefaultWhenUnset(t *testing.T) {
	configFile := filepath.Join("backend", "configs", "runtime.yaml")
	resolved := resolveRuntimeServerSessionDir(configFile, "")

	if resolved != aiclipaths.DefaultSessionsDir() {
		t.Fatalf("expected default session dir %q, got %q", aiclipaths.DefaultSessionsDir(), resolved)
	}
}

func TestResolveRuntimeServerSessionDir_ResolvesConfiguredRelativePathFromConfigFile(t *testing.T) {
	configFile := filepath.Join("backend", "configs", "runtime.yaml")
	resolved := resolveRuntimeServerSessionDir(configFile, "../data/runtime/sessions")

	expected := filepath.Clean(filepath.Join("backend", "data", "runtime", "sessions"))
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestBuildSkillsMCPManager_ExposesLocalToolkitWithoutExternalMCP(t *testing.T) {
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Root = t.TempDir()

	adapter, manager, err := buildSkillsMCPManager(context.Background(), &config.Config{}, runtimeConfig)
	if err != nil {
		t.Fatalf("buildSkillsMCPManager failed: %v", err)
	}
	if manager != nil {
		t.Fatalf("expected nil external MCP manager, got %#v", manager)
	}
	if adapter == nil {
		t.Fatal("expected local runtime tool adapter")
	}

	shellTool, err := adapter.FindTool("execute_shell_command")
	if err != nil {
		t.Fatalf("expected execute_shell_command to be exposed: %v", err)
	}
	if shellTool.MCPName != "" {
		t.Fatalf("expected local shell tool without MCP name, got %+v", shellTool)
	}

	readTool, err := adapter.FindTool("grep")
	if err != nil {
		t.Fatalf("expected grep to be exposed: %v", err)
	}
	if readTool.MCPName != "" {
		t.Fatalf("expected local grep tool without MCP name, got %+v", readTool)
	}
}
