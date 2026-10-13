package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// EnsureFile 对 .json 路径必须生成合法 JSON 空配置（而不是 YAML 文本），
// 否则自动发现的 mcp.json 首次写入后再加载会直接解析失败。
func TestEnsureFileCreatesValidJSONForJSONPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := EnsureFile(path); err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var document struct {
		MCPServers map[string]config.MCPConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("生成的 mcp.json 必须是合法 JSON: %v\n%s", err, raw)
	}
	if len(document.MCPServers) != 0 {
		t.Fatalf("空配置不应包含 server: %#v", document.MCPServers)
	}

	// 走真实加载路径再验一遍（LoadFile 内部复用同一解析器）。
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Fatalf("空配置不应包含 server: %#v", cfg.MCPServers)
	}
}
