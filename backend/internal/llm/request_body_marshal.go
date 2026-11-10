package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// requestBodyStablePrefixKeys 是上游请求体里「内容主体」字段的稳定前缀序。
//
// prompt caching 以请求序列化后的 token 前缀为键：同一次调用内的重试只要
// 改动了前缀区的字节，provider 就无法复用上一次 attempt（乃至上一轮请求）
// 已写入的缓存前缀。max_tokens / max_output_tokens 会随「退化回复扩容 /
// provider 上限回收」在 attempt 之间变化，不能排在内容主体之前——
// encoding/json 对 map 按字典序输出时它们恰好排在内容键前面，重试加宽/收窄
// 会直接改写请求体头部，整段前缀失配
// （session_20261028063206_ZW5KbyLq seq4→7/7→8 抓包）。
//
// 前缀区覆盖四个协议适配器的内容键：
//   - openai:    messages, tools
//   - anthropic: messages, system, tools
//   - gemini:    contents, systemInstruction, tools
//   - codex:     input, instructions, tools
//
// 这些字段在一次会话内只追加不改写；其余字段（含易变的预算参数与
// prompt_cache_key 等标量）随后按字典序输出，不落在可复用的缓存前缀内。
var requestBodyStablePrefixKeys = []string{
	"messages",
	"input",
	"contents",
	"system",
	"systemInstruction",
	"instructions",
	"tools",
	"functions",
}

// MarshalProviderRequestBody 以「前缀缓存友好」的字段序序列化上游请求体：
// 先输出 requestBodyStablePrefixKeys 中存在的字段（保持声明顺序），再按
// 字典序输出其余字段。除字段顺序外与 json.Marshal 语义一致（同样的 HTML
// 转义、数值格式与错误传播）。provider / gateway 以及 cmd 侧的对话请求体
// 都经由它序列化，保证重试只改变易变参数、不破坏可复用的内容前缀。
func MarshalProviderRequestBody(body map[string]interface{}) ([]byte, error) {
	if len(body) == 0 {
		return json.Marshal(body)
	}

	ordered := make([]string, 0, len(body))
	seen := make(map[string]bool, len(body))
	for _, key := range requestBodyStablePrefixKeys {
		if _, ok := body[key]; !ok || seen[key] {
			continue
		}
		seen[key] = true
		ordered = append(ordered, key)
	}
	rest := make([]string, 0, len(body)-len(ordered))
	for key := range body {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	ordered = append(ordered, rest...)

	var buf bytes.Buffer
	buf.WriteByte('{')
	encoder := json.NewEncoder(&buf)
	for i, key := range ordered {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		// 直接把值编码进最终 buffer。旧实现先 json.Marshal 出一份完整
		// []byte 再 Write 复制一次：长会话的 messages 字段可达数十 MB，
		// 每次请求都会制造一份同尺寸的临时缓冲。json.Encoder 的默认
		// 选项与 json.Marshal 一致（compact + HTML 转义），只多一个
		// 结尾换行，Truncate 掉即可保持逐字节等价。
		if err := encoder.Encode(body[key]); err != nil {
			return nil, err
		}
		if buf.Len() == 0 || buf.Bytes()[buf.Len()-1] != '\n' {
			return nil, fmt.Errorf("json encoder did not terminate value for key %q", key)
		}
		buf.Truncate(buf.Len() - 1)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
