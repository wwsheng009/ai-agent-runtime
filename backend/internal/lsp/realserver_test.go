package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealRustAnalyzerRoundTrip 是真机端到端冒烟（docs/lsp 03 验收）：
// 它不注入假 server，而是走生产 DialFunc（SpawnProcess），验证
// spawn → initialize 握手 → didOpen/didChange/didSave → publishDiagnostics
// 等待 → 追加式渲染（I1/I2）整条链路。
//
// 机器上没有 rust-analyzer 或 -short 时跳过：无语言服务器的环境保持原有
// 快速单测，不会因此变红。
func TestRealRustAnalyzerRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping real language-server integration")
	}
	binary, err := exec.LookPath("rust-analyzer")
	if err != nil {
		t.Skip("rust-analyzer not installed; skipping real language-server integration")
	}

	// Windows：子进程退出后文件句柄释放可能滞后数毫秒，t.TempDir 的清理会
	// 偶发 sharing violation（ADR-0005 子进程生命周期）。用显式目录 + 带重试的
	// 清理，避免偶发红灯。
	dir, err := os.MkdirTemp("", "lsp-rust-e2e-")
	if err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	mustWriteFile(t, filepath.Join(dir, "Cargo.toml"),
		"[package]\nname = \"lsp-e2e\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\n")
	sourcePath := filepath.Join(dir, "src", "main.rs")
	mustWriteFile(t, sourcePath,
		"fn main() {\n    let x: i32 = \"not a number\";\n    println!(\"{}\", x);\n}\n")

	cfg := Config{
		Enabled: true,
		Servers: []ServerSpec{{
			Name:           "rust-analyzer",
			Command:        binary,
			Languages:      []string{"rust"},
			StartupTimeout: 60 * time.Second,
		}},
		Diagnostics: DiagnosticsConfig{
			Scope:       ScopeAll,
			MaxItems:    20,
			MaxChars:    4000,
			WaitMS:      60000,
			DegradeMode: DegradeHint,
		},
	}.Normalize()
	bridge := NewBridge(cfg, dir, nil, SpawnProcess)
	ctx := context.Background()
	t.Cleanup(func() {
		bridge.Stop(context.Background())
		removeAllWithRetry(dir)
	})

	// 模拟编辑类工具刚返回的文本：追加语义要求它逐字节保留（I1）。
	output := "patch applied to src/main.rs"
	appended := bridge.AppendToResult(ctx, output, []string{sourcePath})

	if !strings.HasPrefix(appended, output) {
		t.Fatalf("append-only contract (I1) violated; got:\n%s", appended)
	}
	if !strings.Contains(appended, "<lsp_diagnostics") {
		t.Fatalf("expected an inline diagnostics block from rust-analyzer, got:\n%s", appended)
	}
	if strings.Contains(appended, "<lsp_note") {
		t.Fatalf("diagnostics degraded instead of arriving from the real server:\n%s", appended)
	}
	if !strings.Contains(appended, "main.rs") {
		t.Fatalf("diagnostics block does not name the edited file:\n%s", appended)
	}
	if !strings.Contains(appended, "rust-analyzer") {
		t.Fatalf("diagnostics block lost server attribution (A9):\n%s", appended)
	}
	if !hasSeverityLine(appended, "error ") {
		t.Fatalf("expected the type-mismatch error in the block, got:\n%s", appended)
	}

	statuses := bridge.Statuses()
	if len(statuses) != 1 || statuses[0].Name != "rust-analyzer" {
		t.Fatalf("unexpected server statuses: %+v", statuses)
	}
	if statuses[0].State != StateReady {
		t.Fatalf("expected rust-analyzer to be ready after a successful round trip, got: %+v", statuses[0])
	}
}

// hasSeverityLine reports whether the rendered block contains a diagnostic row
// with the given severity prefix (rows are `<severity> <line>:<col> [server] …`).
func hasSeverityLine(block, prefix string) bool {
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// removeAllWithRetry 等子进程完全释放工作区句柄后再删除目录：Stop 已等待进程
// 退出，但 Windows 上文件句柄的释放可能再滞后数毫秒。
func removeAllWithRetry(dir string) {
	for attempt := 0; attempt < 20; attempt++ {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.RemoveAll(dir)
}
