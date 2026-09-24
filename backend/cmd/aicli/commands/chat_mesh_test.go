package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// 本文件覆盖 S3 的会话绑定写侧：进程级 loopback 端点 → mesh/bindings/<sid>.json。
// 读侧（resume 复用端口）由 cmd/aicli/pprof_port_reuse_test.go 覆盖。

func TestPersistChatMeshBinding(t *testing.T) {
	t.Setenv("AICLI_MESH_DIR", t.TempDir())
	mesh.ClearProcessEndpoint()
	t.Cleanup(mesh.ClearProcessEndpoint)

	// 没有 loopback 服务器：不能凭空写一个端口（否则 resume 会去连一个不存在的地址）。
	persistChatMeshBinding("session_no_server", "")
	if _, ok := mesh.LoadBinding(mesh.ResolvePaths(), "session_no_server"); ok {
		t.Fatal("binding must not be written without a loopback endpoint")
	}

	// 有端点：写入 preferred 地址 + 会话工作区。
	mesh.SetProcessEndpoint("127.0.0.1", 45871)
	persistChatMeshBinding("session_with_server", `E:\work\demo`)

	binding, ok := mesh.LoadBinding(mesh.ResolvePaths(), "session_with_server")
	if !ok {
		t.Fatal("LoadBinding() ok = false after persistChatMeshBinding")
	}
	if binding.Preferred == nil || binding.Preferred.Port != 45871 || binding.Preferred.Host != "127.0.0.1" {
		t.Fatalf("Preferred = %+v, want 127.0.0.1:45871", binding.Preferred)
	}
	if binding.LastWorkspacePath != `E:\work\demo` {
		t.Fatalf("LastWorkspacePath = %q, want E:\\work\\demo", binding.LastWorkspacePath)
	}
	// 没有网格节点（--mesh=false / 未加入网格）：last_node_id 留空，不编造。
	if binding.LastNodeID != "" {
		t.Fatalf("LastNodeID = %q, want empty without a mesh node", binding.LastNodeID)
	}

	// 空会话 ID 静默跳过。
	persistChatMeshBinding("", "")
	if _, ok := mesh.LoadBinding(mesh.ResolvePaths(), "session_no_server"); ok {
		t.Fatal("empty session id must not create a binding")
	}
}

// TestPersistChatMeshBindingRecordsCurrentNode 覆盖网格启用时的 last_node_id：
// 绑定是「上次服务者」的来源，S5/S6 的 ls/peers 依赖它。
func TestPersistChatMeshBindingRecordsCurrentNode(t *testing.T) {
	t.Setenv("AICLI_MESH_DIR", t.TempDir())
	mesh.ClearProcessEndpoint()
	t.Cleanup(mesh.ClearProcessEndpoint)

	host := mesh.NewHost(mesh.HostConfig{Kind: "chat", Origin: "test"})
	mesh.SetCurrent(host)
	t.Cleanup(func() {
		host.Close()
		mesh.SetCurrent(nil)
	})
	if err := host.Start(); err != nil {
		t.Fatalf("mesh host Start() error = %v", err)
	}

	mesh.SetProcessEndpoint("127.0.0.1", 45900)
	persistChatMeshBinding("session_node", "")

	binding, ok := mesh.LoadBinding(mesh.ResolvePaths(), "session_node")
	if !ok {
		t.Fatal("LoadBinding() ok = false after persistChatMeshBinding")
	}
	if binding.LastNodeID == "" || binding.LastNodeID != host.NodeID() {
		t.Fatalf("LastNodeID = %q, want the current node id %q", binding.LastNodeID, host.NodeID())
	}
}
