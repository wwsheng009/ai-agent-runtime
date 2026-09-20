package acp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// MCP 传输类型标签。stdio 是 ACP v1 联合类型的「无标签」变体：条目不带 type
// 字段（部分客户端会显式发送 "stdio"，解析时同样接受）。
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"
	MCPTransportSSE   = "sse"
)

// MCPKeyValue 是 ACP v1 的 EnvVariable / HttpHeader 结构：两者字段一致，
// 都是 {name, value} 列表而非 map，保持声明顺序与重复项可见性。
type MCPKeyValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// McpServer 是 session/new、session/load、session/resume 的 mcpServers 条目。
//
// ACP v1 把它定义成 tagged union：
//   - stdio：无 type 标签，必填 name + command，可选 args / env
//   - http ：type="http"，必填 name + url，可选 headers
//   - sse  ：type="sse"，必填 name + url，可选 headers
//
// 用一个结构体承载三种变体，避免调用方对 interface 做类型断言；Type 字段是
// 判别式（空值按 stdio 处理，与规范一致）。
type McpServer struct {
	Name    string
	Type    string
	Command string
	Args    []string
	Env     []MCPKeyValue
	URL     string
	Headers []MCPKeyValue
}

// TransportKind 返回归一化后的传输类型（空 type 视为 stdio）。
func (s McpServer) TransportKind() string {
	kind := strings.ToLower(strings.TrimSpace(s.Type))
	if kind == "" {
		return MCPTransportStdio
	}
	return kind
}

// MCPDecodeIssue 描述一条被跳过的 mcpServers 条目。
//
// ACP v1 规定客户端下发的列表按「逐条容错」处理（x-deserialize-skip-invalid-items）：
// 单条非法不得让整个 session/new 失败，但必须留下可诊断的痕迹。
type MCPDecodeIssue struct {
	// Index 是条目在原始数组中的下标；-1 表示整个字段级错误（非数组 / 非法 JSON）。
	Index int
	// Name 是能解析出的条目名，解析不出时为空。
	Name string
	// Reason 是跳过原因（面向日志与 stderr 诊断，不含凭据值）。
	Reason string
}

func (i MCPDecodeIssue) String() string {
	prefix := "mcpServers"
	if i.Index >= 0 {
		prefix = fmt.Sprintf("mcpServers[%d]", i.Index)
	}
	if name := strings.TrimSpace(i.Name); name != "" {
		prefix += fmt.Sprintf(" (name=%q)", name)
	}
	return prefix + ": " + i.Reason
}

// DecodeMCPServers 逐条解析 mcpServers。
//
// 返回可用的服务器列表与被跳过的条目诊断；两者都可能非空（部分成功是常态）。
// 任何情况下都不返回 error：字段级错误降级为一条 Index=-1 的 issue，调用方
// 记录诊断后继续建会话。
func DecodeMCPServers(raw json.RawMessage) ([]McpServer, []MCPDecodeIssue) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] != '[' {
		return nil, []MCPDecodeIssue{{Index: -1, Reason: "must be an array of server entries"}}
	}

	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, []MCPDecodeIssue{{Index: -1, Reason: fmt.Sprintf("invalid JSON: %v", err)}}
	}

	servers := make([]McpServer, 0, len(items))
	var issues []MCPDecodeIssue
	for index, item := range items {
		server, issue := decodeMCPServerEntry(index, item)
		if issue != nil {
			issues = append(issues, *issue)
			continue
		}
		servers = append(servers, server)
	}
	return servers, issues
}

func decodeMCPServerEntry(index int, raw json.RawMessage) (McpServer, *MCPDecodeIssue) {
	var probe struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Command string `json:"command"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return McpServer{}, &MCPDecodeIssue{
			Index:  index,
			Reason: fmt.Sprintf("entry is not a JSON object: %v", err),
		}
	}

	name := strings.TrimSpace(probe.Name)
	if name == "" {
		return McpServer{}, &MCPDecodeIssue{
			Index:  index,
			Reason: `missing required field "name"`,
		}
	}

	kind := strings.ToLower(strings.TrimSpace(probe.Type))
	switch kind {
	case "", MCPTransportStdio:
		if strings.TrimSpace(probe.Command) == "" {
			return McpServer{}, &MCPDecodeIssue{
				Index:  index,
				Name:   name,
				Reason: `stdio server is missing required field "command"`,
			}
		}
		var payload struct {
			Args []string      `json:"args"`
			Env  []MCPKeyValue `json:"env"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return McpServer{}, &MCPDecodeIssue{
				Index:  index,
				Name:   name,
				Reason: fmt.Sprintf("invalid stdio server fields: %v", err),
			}
		}
		env, err := decodeKeyValueList(raw, "env", payload.Env)
		if err != nil {
			return McpServer{}, &MCPDecodeIssue{Index: index, Name: name, Reason: err.Error()}
		}
		return McpServer{
			Name:    name,
			Type:    MCPTransportStdio,
			Command: strings.TrimSpace(probe.Command),
			Args:    payload.Args,
			Env:     env,
		}, nil
	case MCPTransportHTTP, MCPTransportSSE:
		if strings.TrimSpace(probe.URL) == "" {
			return McpServer{}, &MCPDecodeIssue{
				Index:  index,
				Name:   name,
				Reason: fmt.Sprintf("%s server is missing required field \"url\"", kind),
			}
		}
		var payload struct {
			Headers []MCPKeyValue `json:"headers"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return McpServer{}, &MCPDecodeIssue{
				Index:  index,
				Name:   name,
				Reason: fmt.Sprintf("invalid %s server fields: %v", kind, err),
			}
		}
		headers, err := decodeKeyValueList(raw, "headers", payload.Headers)
		if err != nil {
			return McpServer{}, &MCPDecodeIssue{Index: index, Name: name, Reason: err.Error()}
		}
		return McpServer{
			Name:    name,
			Type:    kind,
			URL:     strings.TrimSpace(probe.URL),
			Headers: headers,
		}, nil
	default:
		return McpServer{}, &MCPDecodeIssue{
			Index:  index,
			Name:   name,
			Reason: fmt.Sprintf("unsupported transport type %q", probe.Type),
		}
	}
}

// decodeKeyValueList 解析 env / headers。
//
// 规范形态是 [{name,value}]。少数客户端发送 map 形态（{"KEY":"VALUE"}），
// 这里作为兼容路径接受，但两种形态的字段名都按规范保留大小写。
func decodeKeyValueList(raw json.RawMessage, field string, list []MCPKeyValue) ([]MCPKeyValue, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return list, nil
	}
	value, ok := fields[field]
	if !ok {
		return list, nil
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || trimmed[0] == '[' {
		return list, nil
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("field %q must be an array of {name,value} entries", field)
	}
	var asMap map[string]string
	if err := json.Unmarshal(trimmed, &asMap); err != nil {
		return nil, fmt.Errorf("field %q must be an array of {name,value} entries", field)
	}
	converted := make([]MCPKeyValue, 0, len(asMap))
	names := make([]string, 0, len(asMap))
	for name := range asMap {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		converted = append(converted, MCPKeyValue{Name: name, Value: asMap[name]})
	}
	return converted, nil
}
