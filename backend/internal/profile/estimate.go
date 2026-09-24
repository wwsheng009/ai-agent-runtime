package profile

import (
	"encoding/json"
	"math"
)

// 本文件是 profile 场景 token 估算的**唯一实现点**（实施方案 Batch 2 任务 3、
// 设计文档 D6 / R13）：`aicli profile show`、chat 启动摘要与前端只消费这里的
// 结果，不各自实现换算。
//
// 估算口径：序列化字节数 / 4（向上取整）。这是明示的"估算"，不是精确分词；
// 展示层必须带"估算"标注。

// estimatedBytesPerToken is the fixed bytes-per-token heuristic used by every
// profile estimate. It is intentionally a single constant so the estimation
// 口径 cannot drift between callers.
const estimatedBytesPerToken = 4

// EstimateTokensFromBytes converts a serialized payload size (bytes) into an
// estimated token count using the fixed bytes/4 heuristic.
func EstimateTokensFromBytes(sizeBytes int) int {
	if sizeBytes <= 0 {
		return 0
	}
	return int(math.Ceil(float64(sizeBytes) / float64(estimatedBytesPerToken)))
}

// EstimateJSONTokens marshals v to JSON and estimates the token cost of the
// serialized payload.
func EstimateJSONTokens(v interface{}) (int, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	return EstimateTokensFromBytes(len(raw)), nil
}

// EstimateSchemaTokens estimates the token cost of a tool schema surface, i.e.
// the function definitions that would be sent to the model. Schemas that fail
// to marshal are skipped: the result is an estimate, never an error path.
func EstimateSchemaTokens(schemas []map[string]interface{}) int {
	totalBytes := 0
	for _, schema := range schemas {
		if len(schema) == 0 {
			continue
		}
		raw, err := json.Marshal(schema)
		if err != nil {
			continue
		}
		totalBytes += len(raw)
	}
	return EstimateTokensFromBytes(totalBytes)
}

// EstimateFileTokens estimates the token cost of a text file payload (prompt
// files etc.) from its byte size.
func EstimateFileTokens(sizeBytes int) int {
	return EstimateTokensFromBytes(sizeBytes)
}
