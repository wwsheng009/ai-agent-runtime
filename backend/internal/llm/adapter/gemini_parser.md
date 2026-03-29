Gemini 的 SSE 和 OpenAI **表面类似，但协议结构完全不同**。如果你直接用 OpenAI 的解析逻辑去处理 Gemini，**tool call 和 content 都会解析错**。

核心区别只有三点，但非常关键。

---

# 一、Gemini SSE 数据结构（真实格式）

Gemini（`/v1beta/models/...:streamGenerateContent` 或 gateway）返回的数据通常是：

```
data: {
  "candidates":[
    {
      "content":{
        "parts":[
          {"text":"Hello "}
        ]
      }
    }
  ]
}

data: {
  "candidates":[
    {
      "content":{
        "parts":[
          {"text":"world"}
        ]
      }
    }
  ]
}
```

最终：

```
data: {
  "candidates":[
    {
      "finishReason":"STOP"
    }
  ]
}
```

---

## Tool Call 时

Gemini 的 function call 在 **parts 中返回**

```
data: {
 "candidates":[
  {
   "content":{
    "parts":[
     {
      "functionCall":{
        "name":"get_weather",
        "args":{
           "city":"Shanghai"
        }
     }
    ]
   }
  }
 ]
}
```

和 OpenAI 的区别：

|           | OpenAI           | Gemini               |
| --------- | ---------------- | -------------------- |
| 文本        | delta.content    | parts[].text         |
| tool call | delta.tool_calls | parts[].functionCall |
| arguments | string流式拼接       | 完整JSON               |
| 结束        | finish_reason    | finishReason         |

**Gemini 不需要拼接 arguments**。

---

# 二、Gemini Streaming Parser（Go 实现）

下面是一个 **稳定版本的 Gemini SSE Parser**。

```go
package gemini

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

type ToolCall struct {
	Name string
	Args map[string]interface{}
}

type Message struct {
	Content   string
	ToolCalls []ToolCall
}

func ParseGeminiStream(
	reader io.Reader,
	onContent func(string),
) (*Message, error) {

	msg := &Message{}

	scanner := bufio.NewScanner(reader)

	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)

	for scanner.Scan() {

		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		if data == "" {
			continue
		}

		var chunk map[string]interface{}

		err := json.Unmarshal([]byte(data), &chunk)
		if err != nil {
			continue
		}

		parseChunk(msg, chunk, onContent)
	}

	return msg, scanner.Err()
}

func parseChunk(
	msg *Message,
	chunk map[string]interface{},
	onContent func(string),
) {

	candidates, ok := chunk["candidates"].([]interface{})

	if !ok || len(candidates) == 0 {
		return
	}

	cand := candidates[0].(map[string]interface{})

	content, ok := cand["content"].(map[string]interface{})
	if !ok {
		return
	}

	parts, ok := content["parts"].([]interface{})
	if !ok {
		return
	}

	for _, p := range parts {

		part := p.(map[string]interface{})

		parseText(msg, part, onContent)

		parseFunction(msg, part)
	}
}

func parseText(
	msg *Message,
	part map[string]interface{},
	onContent func(string),
) {

	text, ok := part["text"].(string)

	if !ok || text == "" {
		return
	}

	msg.Content += text

	if onContent != nil {
		onContent(text)
	}
}

func parseFunction(
	msg *Message,
	part map[string]interface{},
) {

	fn, ok := part["functionCall"].(map[string]interface{})

	if !ok {
		return
	}

	name, _ := fn["name"].(string)

	args, _ := fn["args"].(map[string]interface{})

	msg.ToolCalls = append(msg.ToolCalls, ToolCall{
		Name: name,
		Args: args,
	})
}
```

---

# 三、Gemini Tool Executor

和 OpenAI 完全一样：

```go
type ToolExecutor interface {
	Call(name string, args map[string]interface{}) (string, error)
}

func RunGeminiAgent(
	reader io.Reader,
	exec ToolExecutor,
) error {

	msg, err := ParseGeminiStream(reader, nil)

	if err != nil {
		return err
	}

	if len(msg.ToolCalls) == 0 {
		println(msg.Content)
		return nil
	}

	for _, tc := range msg.ToolCalls {

		result, err := exec.Call(tc.Name, tc.Args)

		if err != nil {
			return err
		}

		println("tool result:", result)
	}

	return nil
}
```

---

# 四、OpenAI vs Gemini SSE 对比

| 能力        | OpenAI           | Gemini               |
| --------- | ---------------- | -------------------- |
| 文本        | delta.content    | parts[].text         |
| tool call | delta.tool_calls | parts[].functionCall |
| arguments | 流式拼接             | string JSON          |
| index     | 有                | 没有                   |
| finish    | finish_reason    | finishReason         |

所以：

**Gemini parser 会简单很多。**

---

# 五、真正的最佳架构（推荐）

如果你的 gateway 同时支持：

* OpenAI
* Gemini
* Claude
* DeepSeek

最好统一成：

```
SSE Parser
      ↓
Provider Adapter
      ↓
Unified Message
      ↓
Tool Executor
```

统一结构：

```
AssistantMessage
{
  content
  tool_calls[]
}
```

Provider 只负责 **协议转换**。

---

# 六、建议你做一个统一 Adapter

```
adapter/
   openai.go
   gemini.go
   claude.go
```

统一输出：

```go
type AssistantMessage struct {
    Content   string
    ToolCalls []ToolCall
}
```

这样你的 Agent Runtime **完全不用关心 provider**。

---

# 七、如果你要做 AI Gateway（强烈建议）

我可以给你一套 **完整的 Go Gateway 实现**：

支持：

* OpenAI SSE
* Gemini SSE
* Claude SSE
* DeepSeek SSE

统一成：

```
OpenAI API format
```

也就是：

```
Anthropic SSE → OpenAI SSE
Gemini SSE → OpenAI SSE
```

这样所有客户端：

```
Codex
OpenAI SDK
LangChain
```

都可以直接用。

整个实现大约 **800 行 Go**，性能比 LangChain 高很多。
