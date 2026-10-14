package main

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// TestRuntimeServerMeshEndpoint 锁定 listener 地址 → node record endpoint 的
// 解析（含通配监听/HTTPS/缺端口拒收）。
func TestRuntimeServerMeshEndpoint(t *testing.T) {
	cases := []struct {
		name       string
		baseURL    string
		wantOK     bool
		wantScheme string
		wantHost   string
		wantPort   int
		wantLoop   bool
	}{
		{"loopback", "http://127.0.0.1:8101", true, "http", "127.0.0.1", 8101, true},
		{"lan", "http://192.168.1.5:8101", true, "http", "192.168.1.5", 8101, false},
		{"wildcard resolved earlier", "http://0.0.0.0:8101", true, "http", "127.0.0.1", 8101, true},
		{"https", "https://10.0.0.2:9443", true, "https", "10.0.0.2", 9443, false},
		{"no port", "http://127.0.0.1", false, "", "", 0, false},
		{"empty", "", false, "", "", 0, false},
	}
	for _, tc := range cases {
		got, ok := runtimeServerMeshEndpoint(tc.baseURL)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok=%v want %v", tc.name, ok, tc.wantOK)
		}
		if !ok {
			continue
		}
		if got.Scheme != tc.wantScheme || got.Host != tc.wantHost || got.Port != tc.wantPort || got.Loopback != tc.wantLoop {
			t.Fatalf("%s: endpoint = %+v", tc.name, got)
		}
		if got.BaseURL == "" || got.WebBaseURL == "" {
			t.Fatalf("%s: BaseURL/WebBaseURL must be set, got %+v", tc.name, got)
		}
	}
}

// TestRuntimeServerWorkspaceName 锁定工作区名提取（与 aicli 同口径）。
func TestRuntimeServerWorkspaceName(t *testing.T) {
	if runtimeServerWorkspaceName() == "" {
		t.Fatal("workspace name must be non-empty (process cwd)")
	}
}

// TestMeshHostCapabilitiesShape 锁定 runtime-server 节点的能力位词表
//（docs/bots/09 §7.5：runner + poller + mesh 基线）。
func TestMeshHostCapabilitiesShape(t *testing.T) {
	caps := []string{"mesh", "runner", "poller"}
	if !mesh.HasCapability(caps, "runner") || !mesh.HasCapability(caps, "poller") {
		t.Fatalf("capabilities = %v, want runner+poller", caps)
	}
}
