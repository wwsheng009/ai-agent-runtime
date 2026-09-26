package importers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// json.go 实现 `aicli mcp import --from json <文件>`：把一个显式 JSON 文件解析成
// server 列表。与其它来源不同，这里的文件由用户指定，因此支持多种形态：
//
//	1) 容器对象：{"mcpServers": {...}} / {"servers": {...}} / {"projects": {...}}
//	2) 单 server 对象：{"name":"notion","url":"https://mcp.notion.com/mcp"}
//	3) 数组：[{"name":"a",...},{"name":"b",...}]
//	4) `aicli mcp get --json` 的导出：{"name":"a","config":{...}}
//
// 形态 1 复用外部配置解析（外部容器键与 projects 段语义一致），
// 形态 2/3/4 复用 mapServerEntry 的字段映射（type 别名、url/command 推断、未知字段告警相同）。

// parseJSONImportDocument 解析 --from json 的文件内容。
func parseJSONImportDocument(data []byte, source string) ([]Server, []string, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}))
	if len(trimmed) == 0 {
		return nil, nil, fmt.Errorf("文件为空（%s）", source)
	}

	switch trimmed[0] {
	case '{':
		return parseJSONImportObject(trimmed, source)
	case '[':
		return parseJSONImportArray(trimmed, source)
	default:
		return nil, nil, fmt.Errorf("JSON 顶层需要是对象或数组（%s）", source)
	}
}

func parseJSONImportObject(data []byte, source string) ([]Server, []string, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	// 形态 1：容器对象（与 Claude/Cursor/VS Code 同构）。
	if _, ok := document["mcpServers"]; ok {
		return parseMCPServersJSON(data, source)
	}
	if _, ok := document["servers"]; ok {
		return parseMCPServersJSON(data, source)
	}
	if _, ok := document["projects"]; ok {
		return parseMCPServersJSON(data, source)
	}

	// 形态 4：`mcp get --json` 导出（config 内层 + 外层 name）。
	name := stringFieldFromRaw(document, "name")
	inner := document
	if rawConfig, ok := document["config"]; ok {
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal(rawConfig, &wrapped); err != nil {
			return nil, nil, fmt.Errorf("解析 config 段失败: %w", err)
		}
		if name == "" {
			name = stringFieldFromRaw(wrapped, "name")
		}
		inner = wrapped
	}

	entry, err := decodeJSONObject(inner)
	if err != nil {
		return nil, nil, err
	}
	if name == "" {
		name = strings.TrimSpace(stringValue(entry, "name"))
	}
	if name == "" {
		return nil, nil, fmt.Errorf(
			"单 server JSON 需要 name 字段（例如 {\"name\":\"notion\",\"url\":\"https://mcp.notion.com/mcp\"}）；"+
				"多 server 请用 {\"mcpServers\":{...}} 容器形态（%s）", source)
	}
	server, warnings := mapServerEntry(name, entry, source)
	return []Server{server}, warnings, nil
}

func parseJSONImportArray(data []byte, source string) ([]Server, []string, error) {
	var elements []json.RawMessage
	if err := json.Unmarshal(data, &elements); err != nil {
		return nil, nil, fmt.Errorf("解析 JSON 数组失败: %w", err)
	}
	if len(elements) == 0 {
		return nil, nil, fmt.Errorf("JSON 数组为空（%s）", source)
	}

	servers := make([]Server, 0, len(elements))
	warnings := make([]string, 0)
	seen := map[string]int{}
	for index, raw := range elements {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, nil, fmt.Errorf("第 %d 项不是 JSON 对象: %w", index+1, err)
		}
		entry, err := decodeJSONObject(fields)
		if err != nil {
			return nil, nil, fmt.Errorf("第 %d 项解析失败: %w", index+1, err)
		}
		name := strings.TrimSpace(stringValue(entry, "name"))
		if name == "" {
			return nil, nil, fmt.Errorf("第 %d 项缺少 name 字段（数组形态要求每项自带 name）", index+1)
		}
		if first, ok := seen[strings.ToLower(name)]; ok {
			warnings = append(warnings, fmt.Sprintf(
				"%s: 第 %d 项与第 %d 项同名（%s），后者将按冲突策略处理", source, index+1, first, name))
		} else {
			seen[strings.ToLower(name)] = index + 1
		}
		server, serverWarnings := mapServerEntry(name, entry, source)
		servers = append(servers, server)
		warnings = append(warnings, serverWarnings...)
	}
	return servers, warnings, nil
}

// decodeJSONObject 把已解析的 RawMessage 映射转成 map[string]interface{}。
func decodeJSONObject(fields map[string]json.RawMessage) (map[string]interface{}, error) {
	entry := make(map[string]interface{}, len(fields))
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var value interface{}
		if err := json.Unmarshal(fields[key], &value); err != nil {
			return nil, fmt.Errorf("字段 %q 解析失败: %w", key, err)
		}
		entry[key] = value
	}
	return entry, nil
}

func stringFieldFromRaw(fields map[string]json.RawMessage, key string) string {
	raw, ok := fields[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
