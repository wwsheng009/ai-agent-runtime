package configaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLayer 在 dir 下写一份最小可解析的连接器配置层（config.yaml +
// connectors.secrets.yaml），返回 config.yaml 路径。
func writeLayer(t *testing.T, dir, secretsYAML string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	connectorsYAML := "connectors:\n" +
		"    - id: qq-bot1\n" +
		"      provider: qqbot\n" +
		"      name: qq-bot1\n" +
		"      enabled: true\n" +
		"      mode: gateway\n"
	if err := os.WriteFile(configPath, []byte(connectorsYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if secretsYAML != "" {
		secretsPath := filepath.Join(dir, "connectors.secrets.yaml")
		if err := os.WriteFile(secretsPath, []byte(secretsYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return configPath
}

func secretsYAML(secret string) string {
	return "qq-bot1:\n    app_secret: " + secret + "\n"
}

func TestDetectSecretsDivergence(t *testing.T) {
	root := t.TempDir()
	layerA := writeLayer(t, filepath.Join(root, "layerA"), secretsYAML("secret-A"))
	layerB := writeLayer(t, filepath.Join(root, "layerB"), secretsYAML("secret-B"))
	same := writeLayer(t, filepath.Join(root, "same"), secretsYAML("secret-A"))
	empty := writeLayer(t, filepath.Join(root, "empty"), "")

	// 两层不同密钥 → 分叉（含层路径与指纹数）。
	got := DetectSecretsDivergence([]string{layerA, layerB})
	if len(got) != 1 {
		t.Fatalf("divergence = %+v, want 1 条", got)
	}
	if got[0].ConnectorID != "qq-bot1" || got[0].DistinctSecrets != 2 || len(got[0].Layers) != 2 {
		t.Fatalf("divergence = %+v", got[0])
	}

	// 重复路径去重：只算一层，不算分叉。
	if dup := DetectSecretsDivergence([]string{layerA, layerA}); len(dup) != 0 {
		t.Fatalf("duplicate path must dedupe: %+v", dup)
	}
	// 单层 / 同一密钥 / 不存在的路径 → 无分叉。
	for name, paths := range map[string][]string{
		"single":  {layerA},
		"same":    {layerA, same},
		"missing": {layerA, filepath.Join(root, "nope", "config.yaml")},
		"blank":   {""},
	} {
		if out := DetectSecretsDivergence(paths); len(out) != 0 {
			t.Fatalf("%s: unexpected divergence: %+v", name, out)
		}
	}
	// 一层有密钥、另一层为空（密钥文件缺失）也算分叉。
	if out := DetectSecretsDivergence([]string{layerA, empty}); len(out) != 1 || out[0].DistinctSecrets != 2 {
		t.Fatalf("empty-vs-nonempty must diverge: %+v", out)
	}
}

// TestWarnSecretsDivergenceRedactsValues 锁定告警只报 id/层路径，绝不带密钥值；
// logf 为 nil 时静默（只计算不输出）。
func TestWarnSecretsDivergenceRedactsValues(t *testing.T) {
	root := t.TempDir()
	layerA := writeLayer(t, filepath.Join(root, "layerA"), secretsYAML("secret-A"))
	layerB := writeLayer(t, filepath.Join(root, "layerB"), secretsYAML("secret-B"))

	var lines []string
	WarnSecretsDivergence([]string{layerA, layerB}, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	if len(lines) != 1 {
		t.Fatalf("warnings = %+v, want 1 条", lines)
	}
	text := lines[0]
	if !strings.Contains(text, "qq-bot1") || !strings.Contains(text, layerA) || !strings.Contains(text, layerB) {
		t.Fatalf("告警应含 id 与两层路径：%s", text)
	}
	if strings.Contains(text, "secret-A") || strings.Contains(text, "secret-B") {
		t.Fatalf("告警不得包含密钥值：%s", text)
	}

	WarnSecretsDivergence([]string{layerA, layerB}, nil) // nil logf 不得 panic。
}
