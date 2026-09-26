package importers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// json_test.go 覆盖 `--from json` 的四种文件形态、错误信息与 Options 过滤。
// 与其它来源不同，显式文件由用户指定，因此形态宽松但错误必须可执行。

func writeJSONImportFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestParseJSONImportContainerForms(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"mcpServers", `{"mcpServers": {"notion": {"type": "http", "url": "https://mcp.notion.com/mcp"}}}`},
		{"servers", `{"servers": {"notion": {"url": "https://mcp.notion.com/mcp"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			servers, _, err := parseJSONImportDocument([]byte(tc.content), "sample.json")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(servers) != 1 || servers[0].Name != "notion" {
				t.Fatalf("servers = %#v", servers)
			}
			if servers[0].Config.URL != "https://mcp.notion.com/mcp" || servers[0].Config.Type == "" {
				t.Fatalf("config = %#v", servers[0].Config)
			}
		})
	}
}

// 单对象形态：name 必填；也接受 `mcp get --json` 的 {name, config} 导出（含 BOM）。
func TestParseJSONImportSingleObject(t *testing.T) {
	servers, _, err := parseJSONImportDocument(
		[]byte("\uFEFF"+`{"name":"local-fs","type":"stdio","command":"npx","args":["-y","server-filesystem"]}`), "single.json")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "local-fs" || servers[0].Config.Command != "npx" {
		t.Fatalf("servers = %#v", servers)
	}

	// get --json 导出：外层 name + config 内层。
	exported, _, err := parseJSONImportDocument(
		[]byte(`{"name":"context7","config":{"type":"streamable","url":"https://mcp.context7.com/mcp"},"configSource":"user"}`),
		"export.json")
	if err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if len(exported) != 1 || exported[0].Name != "context7" || exported[0].Config.URL == "" {
		t.Fatalf("exported = %#v", exported)
	}

	// 单对象缺 name：报错里要给出可复制的写法。
	_, _, err = parseJSONImportDocument([]byte(`{"url":"https://x.example.com/mcp"}`), "noname.json")
	if err == nil || !strings.Contains(err.Error(), "name") || !strings.Contains(err.Error(), "mcpServers") {
		t.Fatalf("缺 name 应给出修复建议: %v", err)
	}
}

func TestParseJSONImportArray(t *testing.T) {
	content := `[
	  {"name":"alpha","url":"https://a.example.com/mcp"},
	  {"name":"beta","command":"npx","args":["-y","pkg"]},
	  {"name":"alpha","url":"https://dup.example.com/mcp"}
	]`
	servers, warnings, err := parseJSONImportDocument([]byte(content), "list.json")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(servers) != 3 {
		t.Fatalf("servers = %#v", servers)
	}
	if servers[0].Config.Type != "streamable" || servers[1].Config.Type != "stdio" {
		t.Fatalf("类型推断错误: %#v %#v", servers[0].Config.Type, servers[1].Config.Type)
	}
	if !containsWarning(warnings, "同名") {
		t.Fatalf("重复 name 应告警: %#v", warnings)
	}

	// 数组元素缺 name / 不是对象：错误要带序号。
	if _, _, err := parseJSONImportDocument([]byte(`[{"url":"https://x.example.com/mcp"}]`), "list.json"); err == nil ||
		!strings.Contains(err.Error(), "第 1 项") || !strings.Contains(err.Error(), "name") {
		t.Fatalf("缺 name 的元素应报序号: %v", err)
	}
	if _, _, err := parseJSONImportDocument([]byte(`[1,2]`), "list.json"); err == nil ||
		!strings.Contains(err.Error(), "第 1 项") {
		t.Fatalf("非对象元素应报序号: %v", err)
	}
}

func TestParseJSONImportRejectsBadDocuments(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "   \n", "文件为空"},
		{"bad json", "{not-json", "解析 JSON 失败"},
		{"scalar", `"just-a-string"`, "顶层需要是对象或数组"},
		{"empty array", `[]`, "数组为空"},
		{"no known keys", `{"hello":"world"}`, "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseJSONImportDocument([]byte(tc.content), "bad.json")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want 含 %q", err, tc.want)
			}
		})
	}
}

func TestImportJSONFileWithNamesFilter(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONImportFile(t, dir, "mcp.json", `{"mcpServers":{
	  "keep": {"url":"https://keep.example.com/mcp"},
	  "drop": {"command":"drop-cmd"}
	}}`)

	results, err := Import(VendorJSON, Options{Root: dir, Home: dir, JSONFile: path, Names: []string{"KEEP"}})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(results) != 1 || results[0].Vendor != VendorJSON {
		t.Fatalf("results = %#v", results)
	}
	if len(results[0].Servers) != 1 || results[0].Servers[0].Name != "keep" {
		t.Fatalf("--only 过滤失效: %#v", results[0].Servers)
	}
	if len(results[0].Scanned) != 1 || !results[0].Scanned[0].Exists {
		t.Fatalf("Scanned 记录缺失: %#v", results[0].Scanned)
	}

	// 支持列表里要包含 json，否则 --from json 会被判为不支持的来源。
	if !containsString(SupportedVendors(), VendorJSON) {
		t.Fatalf("SupportedVendors 缺 json: %#v", SupportedVendors())
	}
}

func TestImportJSONVendorRequiresPath(t *testing.T) {
	if _, err := Import(VendorJSON, Options{Root: t.TempDir(), Home: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "--file") {
		t.Fatalf("缺少文件路径应报错: %v", err)
	}
}

func containsWarning(warnings []string, fragment string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, fragment) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
