package viewcore

// Schema returns the one canonical input schema for the view tool. Every
// surface publishes this schema; a runtime that cannot honor an optional
// parameter removes it with toolcore.FilterSchemaParams instead of declaring a
// second, weaker schema. `symbol` is optional: it needs a code index, so
// surfaces without one (for example the remote runtime) simply omit it.
func Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "单个文件路径。也可改用 files 在一次调用中读取多个文件。",
			},
			"symbol": map[string]interface{}{
				"type":        "string",
				"description": "可选：按符号名读取（Phase 3）。索引可用时自动定位符号所在文件与行范围；索引不可用时若同时给了 file_path 则按行范围读取并附 note，否则报错并提示改用 grep 或 file_path+offset/limit。",
			},
			"files": map[string]interface{}{
				"type":        "array",
				"description": "批量文件读取请求；适合一次获取多个独立文件或不同区间，减少 LLM 往返。批内未显式指定 limit 的项默认最多读取 200 行，避免合并输出过大被回显截断。",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string", "description": "文件路径。"},
						"offset":    map[string]interface{}{"type": "integer", "description": "0-based 起始行，默认 0。"},
						"limit":     map[string]interface{}{"type": "integer", "description": "读取行数，默认 400。"},
					},
					"required":             []string{"file_path"},
					"additionalProperties": false,
				},
			},
			"compact": map[string]interface{}{
				"type":        "boolean",
				"description": "批量扫描模式（仅 files 数组生效）：每个文件只输出前 10 行 + 元数据摘要（lines_read/is_truncated/suggested_next_offset/total_lines），适合先扫描多个文件再定点细读。默认 false。",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "开始读取的行号（0-based，默认为 0）。大文件请配合 limit 分段查看；不要反复全量 view 同一路径。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "读取行数，默认 400；结果会标记 eof 和 is_truncated，以及 suggested_next_offset，便于按需继续。大文件优先更小 limit。",
			},
		},
		"required": []string{},
	}
}
