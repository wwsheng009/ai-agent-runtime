package mutatecore

import "strings"

// The schemas below are the single source both surfaces publish. A mutation
// tool has one contract: if a parameter is declared here, the handler above
// reads it, and if the handler reads it, it can be declared here. Surfaces that
// cannot honour something must filter it out (toolcore.FilterSchemaParams)
// rather than maintain a weaker schema of their own.

// WriteParameters is the input schema of the write tool.
func WriteParameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要写入的文件路径。若需要写入多个文件，请拆分为多次 write 调用，每次只聚焦一个文件。",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "文件内容。若内容较长，请拆分为多个更小的写入块，按章节或按块逐步写入，避免一次性生成超长参数导致工具调用被截断。",
			},
			"expected_sha256": map[string]interface{}{
				"type":        "string",
				"description": "可选并发保护：写入前文件内容的 SHA-256；新文件使用 absent。重试同一内容会自动识别为幂等回放。",
			},
		},
		"required": []string{"file_path", "content"},
	}
}

// AppendParameters is the input schema of the append_write tool.
func AppendParameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "追加目标文件路径。",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "追加到文件末尾的文本。",
			},
			"create_if_missing": map[string]interface{}{
				"type":        "boolean",
				"description": "文件不存在时是否创建（默认 true；显式传 false 可要求目标必须已存在）。",
			},
			"truncate_first": map[string]interface{}{
				"type":        "boolean",
				"description": "若为 true，则先清空/覆盖文件，再写入本块；适合第一块写入。默认 false。",
			},
			"ensure_trailing_newline": map[string]interface{}{
				"type":        "boolean",
				"description": "若为 true 且本块末尾没有换行，则自动补一个换行。默认 false。",
			},
			"expected_offset": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "可选并发与重试保护：追加前预期文件字节数。相同 offset 和内容的重复请求会作为幂等回放，不会重复追加。",
			},
		},
		"required": []string{"file_path", "content"},
	}
}

// EditParameters is the input schema of the edit tool.
func EditParameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要编辑的文件路径。",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "要被替换的精确文本。除非设置 replace_all/replacement_count，否则必须在文件中恰好出现一次。",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "替换后的文本。",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "替换所有匹配项。与 replacement_count 互斥。",
			},
			"replacement_count": map[string]interface{}{
				"type":        "integer",
				"description": "只替换前 N 个匹配项（必须大于 0）。与 replace_all 互斥。",
			},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}
}

// ApplyPatchParameters is the input schema of the apply_patch tool.
func ApplyPatchParameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"patch": map[string]interface{}{
				"type": "string",
				"description": "Codex-style patch: " + PatchBegin + " / " +
					strings.TrimSuffix(PatchAdd, ": ") + " / " +
					strings.TrimSuffix(PatchUpdate, ": ") +
					" with @@ blocks / " + strings.TrimSuffix(PatchDelete, ": ") + " / " +
					PatchEnd + ".",
			},
		},
		"required": []string{"patch"},
	}
}
