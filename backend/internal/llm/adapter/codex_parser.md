# Codex Responses API 协议解析器

本文档描述 OpenAI Codex/Responses API 的解析实现，与传统的 Chat Completions API 有重要区别。

## 协议概述

### 关键区别

| 特性 | OpenAI Chat Completions | Codex Responses API |
|------|------------------------|---------------------|
| API 路径 | `/v1/chat/completions` | `/v1/responses` |
| 请求字段 | `messages` | `input` |
| Token 限制 | `max_tokens` | `max_output_tokens` |
| 流式格式 | `data: {JSON}` | `event: xxx\ndata: {JSON}` |
| 结束标记 | `data: [DONE]` | `response.completed` |

### 核心特点

1. **事件驱动 SSE**: 使用 `event:` 和 `data:` 组合格式
2. **input/output 模型**: 请求使用 `input`，响应使用 `output`
3. **原生推理支持**: 通过 `reasoning` 配置和事件
4. **工具名称限制**: 最长 64 字符

---

## SSE 事件类型

### 响应生命周期事件

| 事件 | 触发时机 | 关键字段 |
|-----|---------|---------|
| `response.created` | 响应开始 | `{id, model, status}` |
| `response.completed` | 响应结束 | `{id, status, stop_reason, usage}` |
| `response.done` | 响应结束（增量） | `{usage}` |
| `response.failed` | 响应失败 | `{error}` |
| `response.incomplete` | 响应不完整 | `{incomplete_details}` |

### 输出项事件

| 事件 | 说明 | item.type |
|-----|------|-----------|
| `response.output_item.added` | 输出项开始 | `message`, `function_call`, `reasoning` |
| `response.output_item.done` | 输出项结束 | `message`, `function_call`, `reasoning` |

### 内容增量事件

| 事件 | 说明 | 关键字段 |
|-----|------|---------|
| `response.output_text.delta` | 文本增量 | `delta` |
| `response.function_call_arguments.delta` | 函数参数增量 | `index`, `delta` |

### 推理事件

| 事件 | 说明 | 关键字段 |
|-----|------|---------|
| `response.reasoning_summary_part.added` | 推理摘要块开始 | `summary_index` ⚠️ |
| `response.reasoning_summary_text.delta` | 推理摘要文本增量 | `summary_index`, `delta` ⚠️ |
| `response.reasoning_summary_part.done` | 推理摘要块结束 | `summary_index` ⚠️ |
| `response.reasoning_text.delta` | 推理内容增量 | `content_index`, `delta` ⚠️ |

⚠️ **注意索引字段差异**:
- `reasoning_summary_*` 事件使用 `summary_index`
- `reasoning_text.delta` 事件使用 `content_index`
- 其他事件使用 `index`

---

## Go 实现

### 流式状态管理

```go
// CodexStreamState Codex 流式状态管理
type CodexStreamState struct {
    ResponseID   string
    Model        string
    Content      strings.Builder
    Reasoning    strings.Builder
    ToolCalls    map[int]*CodexToolCall // index -> tool call
    FinishReason string
    Usage        map[string]int64

    // 追踪当前 output item
    CurrentItemIndex   int
    CurrentItemType    string // "message", "function_call", "reasoning"
    CurrentItemStarted bool

    // 追踪 reasoning summary
    SummaryIndex      int
    SummaryStarted    bool
    SummaryContent    strings.Builder
}

// CodexToolCall 工具调用状态
type CodexToolCall struct {
    CallID    string
    Name      string
    Arguments strings.Builder
}
```

### SSE 解析核心

```go
// handleCodexStreamResponse 处理 Codex 流式响应
func (a *CodexAdapter) handleCodexStreamResponse(respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
    state := NewCodexStreamState()

    scanner := bufio.NewScanner(respBody)
    var currentEvent string

    for scanner.Scan() {
        line := scanner.Text()

        // 解析 SSE 格式
        if strings.HasPrefix(line, "event: ") {
            currentEvent = strings.TrimPrefix(line, "event: ")
            continue
        }

        if strings.HasPrefix(line, "data: ") {
            data := strings.TrimPrefix(line, "data: ")
            if data == "" {
                continue
            }

            var event map[string]interface{}
            if err := json.Unmarshal([]byte(data), &event); err != nil {
                continue
            }

            a.processCodexEvent(state, currentEvent, event, onContent)
        }
    }

    return state.ToMap(), nil
}
```

### 事件处理

```go
// processCodexEvent 处理单个 Codex 事件
func (a *CodexAdapter) processCodexEvent(state *CodexStreamState, eventType string, event map[string]interface{}, onContent func(string)) {
    switch eventType {
    case "response.created":
        // 提取 response.id 和 model
        if resp, ok := event["response"].(map[string]interface{}); ok {
            state.ResponseID = resp["id"].(string)
            state.Model = resp["model"].(string)
        }

    case "response.output_item.added":
        // 初始化 output item
        index := int(event["index"].(float64))
        item := event["item"].(map[string]interface{})
        itemType := item["type"].(string)
        
        state.CurrentItemIndex = index
        state.CurrentItemType = itemType
        
        // 如果是 function_call，初始化 ToolCall
        if itemType == "function_call" {
            tc := &CodexToolCall{
                CallID: item["call_id"].(string),
                Name:   item["name"].(string),
            }
            state.ToolCalls[index] = tc
        }

    case "response.output_text.delta":
        // 文本增量
        delta := event["delta"].(string)
        state.Content.WriteString(delta)
        if onContent != nil {
            onContent(delta)
        }

    case "response.function_call_arguments.delta":
        // 工具参数增量
        index := int(event["index"].(float64))
        delta := event["delta"].(string)
        state.ToolCalls[index].Arguments.WriteString(delta)

    case "response.reasoning_summary_part.added":
        // 推理块开始（注意使用 summary_index）
        state.SummaryIndex = int(event["summary_index"].(float64))
        state.SummaryStarted = true

    case "response.reasoning_summary_text.delta":
        // 推理增量（注意使用 summary_index）
        delta := event["delta"].(string)
        state.Reasoning.WriteString(delta)

    case "response.completed":
        // 响应完成
        resp := event["response"].(map[string]interface{})
        state.FinishReason = resp["stop_reason"].(string)
        // 提取 usage...
    }
}
```

---

## 完整流程示例

### 普通文本响应

```
event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-4.1","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","index":0,"item":{"type":"message","role":"assistant","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":" world"}

event: response.output_item.done
data: {"type":"response.output_item.done","index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-4.1","status":"completed","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}
```

### 工具调用响应

```
event: response.created
data: {"type":"response.created","response":{"id":"resp_2","model":"gpt-4.1","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","index":0,"item":{"type":"function_call","call_id":"call_1","name":"get_weather"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","index":0,"delta":"{\"location\":\""}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","index":0,"delta":"Beijing\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","index":0,"item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"location\":\"Beijing\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_2","model":"gpt-4.1","status":"completed","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}
```

### 推理 + 文本响应

```
event: response.created
data: {"type":"response.created","response":{"id":"resp_3","model":"gpt-4.1","status":"in_progress"}}

event: response.reasoning_summary_part.added
data: {"type":"response.reasoning_summary_part.added","summary_index":0}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","summary_index":0,"delta":"Let me think..."}

event: response.reasoning_summary_part.done
data: {"type":"response.reasoning_summary_part.done","summary_index":0}

event: response.output_item.added
data: {"type":"response.output_item.added","index":0,"item":{"type":"message","role":"assistant","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"The answer is 42."}

event: response.output_item.done
data: {"type":"response.output_item.done","index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The answer is 42."}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_3","model":"gpt-4.1","status":"completed","stop_reason":"end_turn"}}
```

---

## 非流式响应格式

```json
{
  "id": "resp_123",
  "object": "response",
  "created_at": 1234567890,
  "model": "gpt-4.1",
  "status": "completed",
  "stop_reason": "end_turn",
  "output": [
    {
      "type": "message",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "Hello, world!"}
      ]
    },
    {
      "type": "function_call",
      "call_id": "call_123",
      "name": "get_weather",
      "arguments": "{\"location\":\"Beijing\"}"
    },
    {
      "type": "reasoning",
      "summary": [
        {"type": "summary_text", "text": "Let me think..."}
      ]
    }
  ],
  "usage": {
    "input_tokens": 100,
    "output_tokens": 50,
    "total_tokens": 150
  }
}
```

---

## 注意事项

### 1. 索引字段差异 ⚠️

```go
// ❌ 错误: reasoning 事件使用 index
event["index"]

// ✅ 正确: reasoning_summary 事件使用 summary_index
event["summary_index"]

// ✅ 正确: reasoning_text 事件使用 content_index
event["content_index"]
```

### 2. output_item.added 发送时机

| 场景 | 是否发送 message output_item.added |
|------|----------------------------------|
| 纯 reasoning (无 content，无 tool_call) | ✅ 是（在 finish_reason 时发送空 message） |
| reasoning + content | ✅ 是（在第一次 content 时发送） |
| reasoning + tool_call | ❌ 否（只发送 function_call item） |
| 纯 content (无 reasoning) | ✅ 是（在第一次 content 时发送） |
| 纯 tool_call (无 reasoning) | ❌ 否（只发送 function_call item） |

### 3. 工具参数拼接

与 Anthropic 类似，`function_call_arguments.delta` 需要流式拼接：

```go
// 工具参数是增量传输的
state.ToolCalls[index].Arguments.WriteString(delta)
```

### 4. 不支持的事件类型

Codex 客户端不处理以下事件：
- `response.content_part.added`
- `response.content_part.done`

必须使用 `response.output_item.added/done` 替代。

---

## 测试要点

1. **普通文本生成**: 验证 `output_text.delta` 累积
2. **工具调用**: 验证 `function_call_arguments.delta` 拼接
3. **推理内容**: 验证 `summary_index` 正确使用
4. **多工具调用**: 验证 `index` 正确映射
5. **失败/不完整**: 验证 `response.failed/incomplete` 处理
6. **非流式响应**: 验证 `output` 数组解析

---

## 与其他协议对比

| 特性 | OpenAI Chat | Codex Responses | Anthropic | Gemini |
|------|-------------|-----------------|-----------|--------|
| 流式格式 | delta | event-based | event-based | delta |
| 文本字段 | `delta.content` | `output_text.delta` | `text_delta` | `parts[].text` |
| 工具调用 | `tool_calls` | `function_call` | `tool_use` | `functionCall` |
| Arguments | 流式拼接 | 流式拼接 | 流式拼接 | **完整 JSON** |
| 推理字段 | `reasoning_content` | `reasoning_summary` | `thinking` | `thought` |

---

## 实现代码位置

- 类型定义: `internal/runtime/llm/adapter/codex.go`
- 测试文件: `internal/runtime/llm/adapter/codex_request_test.go`
- 详细协议文档: `docs/protocol/codex/`
