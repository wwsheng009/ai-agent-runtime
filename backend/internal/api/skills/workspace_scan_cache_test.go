package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
)

// TestScanWorkspaceCachedTTL 验证工作区扫描结果按 TTL 缓存：
// TTL 内重复请求复用同一次扫描（文件变更不反映），过期后重新扫描（变更反映）。
func TestScanWorkspaceCachedTTL(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("// version 1\npackage main\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	h := &Handler{}
	first, symbols1, refs1, err := h.scanWorkspaceCached(dir, nil)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(first.Files) != 1 || !scanTextContains(first, "version 1") {
		t.Fatalf("first scan unexpected: files=%d text=%q", len(first.Files), scanText(first))
	}
	if symbols1 == nil || refs1 == nil {
		t.Fatalf("expected symbol index and reference graph from first scan")
	}

	// TTL 内修改文件再请求：应命中缓存，内容仍是旧版本
	if err := os.WriteFile(file, []byte("// version 2\npackage main\n"), 0o644); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}
	cached, symbols2, refs2, err := h.scanWorkspaceCached(dir, nil)
	if err != nil {
		t.Fatalf("cached scan: %v", err)
	}
	if cached != first || symbols2 != symbols1 || refs2 != refs1 {
		t.Fatalf("expected cached result reuse, got a fresh scan")
	}
	if scanTextContains(cached, "version 2") {
		t.Fatalf("cached result should not reflect changed file")
	}

	// 篡改缓存时间戳强制过期：应重新扫描并反映变更
	h.workspaceScanMu.Lock()
	key := dir + "|" + workspaceConfigFingerprint(nil)
	h.workspaceScanCache[key].at = time.Now().Add(-workspaceScanCacheTTL - time.Second)
	h.workspaceScanMu.Unlock()

	refreshed, _, _, err := h.scanWorkspaceCached(dir, nil)
	if err != nil {
		t.Fatalf("refreshed scan: %v", err)
	}
	if refreshed == first {
		t.Fatalf("expected a fresh scan after TTL expiry")
	}
	if !scanTextContains(refreshed, "version 2") {
		t.Fatalf("refreshed scan should reflect changed file, got %q", scanText(refreshed))
	}
}

func scanTextContains(scan *workspace.ScanResult, text string) bool {
	return strings.Contains(scanText(scan), text)
}

func scanText(scan *workspace.ScanResult) string {
	var sb strings.Builder
	for _, chunk := range scan.CodeChunks {
		sb.WriteString(chunk.Content)
	}
	return sb.String()
}