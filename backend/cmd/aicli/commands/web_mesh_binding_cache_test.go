package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

func writeMeshBindingForTest(t *testing.T, paths mesh.Paths, sessionID string, port int) {
	t.Helper()
	if err := os.MkdirAll(paths.Bindings, 0o755); err != nil {
		t.Fatalf("mkdir bindings: %v", err)
	}
	binding := mesh.SessionBinding{
		SchemaVersion: mesh.SchemaVersion,
		SessionID:     sessionID,
		Preferred:     &mesh.BindingAddr{Host: "127.0.0.1", Port: port},
		UpdatedAt:     time.Now(),
	}
	data, err := json.Marshal(binding)
	if err != nil {
		t.Fatalf("marshal binding: %v", err)
	}
	if err := os.WriteFile(paths.BindingPath(sessionID), data, 0o644); err != nil {
		t.Fatalf("write binding: %v", err)
	}
}

// binding 缓存必须按文件状态失效：重写文件（端口变化）后不得再返回旧值。
// 这是「内容寻址缓存」与「TTL 缓存」的分界——TTL 会在窗口内读到过期端点。
func TestLoadMeshBindingCachedRefreshesOnFileChange(t *testing.T) {
	root := t.TempDir()
	paths := mesh.Paths{Root: root, Bindings: filepath.Join(root, "bindings")}
	sessionID := "sess-binding-cache"

	writeMeshBindingForTest(t, paths, sessionID, 41001)
	first, ok := loadMeshBindingCached(paths, sessionID)
	if !ok || first.Preferred == nil || first.Preferred.Port != 41001 {
		t.Fatalf("first load = %#v ok=%t, want port 41001", first.Preferred, ok)
	}
	// 同一文件状态的重复读取：命中缓存，值不变。
	second, ok := loadMeshBindingCached(paths, sessionID)
	if !ok || second.Preferred == nil || second.Preferred.Port != 41001 {
		t.Fatalf("cached load = %#v ok=%t, want port 41001", second.Preferred, ok)
	}

	// 端口号长度相同（41001 → 41002），文件大小不变，只有 mtime 前进：
	// 缓存 key 必须随 mtime 变化而刷新。
	time.Sleep(20 * time.Millisecond)
	writeMeshBindingForTest(t, paths, sessionID, 41002)
	third, ok := loadMeshBindingCached(paths, sessionID)
	if !ok || third.Preferred == nil || third.Preferred.Port != 41002 {
		t.Fatalf("stale binding served from cache: %#v ok=%t", third.Preferred, ok)
	}
}

// 缺文件 → ok=false（缓存也要如实记录缺失，之后文件出现必须能被读到）。
func TestLoadMeshBindingCachedMissingThenAppears(t *testing.T) {
	root := t.TempDir()
	paths := mesh.Paths{Root: root, Bindings: filepath.Join(root, "bindings")}
	sessionID := "sess-binding-late"

	if _, ok := loadMeshBindingCached(paths, sessionID); ok {
		t.Fatal("missing binding must report ok=false")
	}
	writeMeshBindingForTest(t, paths, sessionID, 41003)
	binding, ok := loadMeshBindingCached(paths, sessionID)
	if !ok || binding.Preferred == nil || binding.Preferred.Port != 41003 {
		t.Fatalf("binding after creation = %#v ok=%t, want port 41003", binding.Preferred, ok)
	}
}
