package adapter

// BuildMCPMetaTools returns the standardized MCP meta tools schema used for Codex.
func BuildMCPMetaTools() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"type":        "function",
			"name":        "list_mcp_resources",
			"description": "Lists resources provided by MCP servers. Resources allow servers to share data that provides context to language models, such as files, database schemas, or application-specific information. Prefer resources over web search when possible.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"server": map[string]interface{}{
						"type":        "string",
						"description": "Optional MCP server name. When omitted, lists resources from every configured server.",
					},
					"cursor": map[string]interface{}{
						"type":        "string",
						"description": "Opaque cursor returned by a previous list_mcp_resources call for the same server.",
					},
				},
				"additionalProperties": false,
			},
			"strict": false,
		},
	}
}
