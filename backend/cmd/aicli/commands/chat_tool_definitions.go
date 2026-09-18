package commands

import (
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolschema"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func toolDefinitionsFromSelection(selection *aicliFunctionSelection) []runtimetypes.ToolDefinition {
	if selection == nil || len(selection.Schemas) == 0 {
		return nil
	}
	definitions := make([]runtimetypes.ToolDefinition, 0, len(selection.Schemas))
	for _, schema := range selection.Schemas {
		if definition, ok := toolDefinitionFromSchema(schema); ok {
			definitions = append(definitions, definition)
		}
	}
	sortToolDefinitions(definitions)
	return definitions
}

// toolDefinitionFromSchema 把函数目录 schema（name/description/parameters/metadata）
// 转换为运行时工具定义；缺少 name 的 schema 视为无效。
func toolDefinitionFromSchema(schema map[string]interface{}) (runtimetypes.ToolDefinition, bool) {
	if len(schema) == 0 {
		return runtimetypes.ToolDefinition{}, false
	}
	name, _ := schema["name"].(string)
	if strings.TrimSpace(name) == "" {
		return runtimetypes.ToolDefinition{}, false
	}
	description, _ := schema["description"].(string)
	parameters, _ := schema["parameters"].(map[string]interface{})
	metadata, _ := schema["metadata"].(map[string]interface{})
	return runtimetypes.ToolDefinition{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Parameters:  cloneToolParametersSchema(parameters),
		Metadata:    cloneFunctionSchema(metadata),
	}, true
}

func toolDefinitionsToSchemas(defs []runtimetypes.ToolDefinition) []map[string]interface{} {
	if len(defs) == 0 {
		return nil
	}
	orderedDefs := append([]runtimetypes.ToolDefinition(nil), defs...)
	sortToolDefinitions(orderedDefs)

	schemas := make([]map[string]interface{}, 0, len(orderedDefs))
	for _, def := range orderedDefs {
		schema := map[string]interface{}{
			"name":        def.Name,
			"description": def.Description,
			"parameters":  cloneToolParametersSchema(def.Parameters),
		}
		if len(def.Metadata) > 0 {
			schema["metadata"] = cloneFunctionSchema(def.Metadata)
		}
		schemas = append(schemas, schema)
	}
	return schemas
}

func sortToolDefinitions(definitions []runtimetypes.ToolDefinition) {
	sort.SliceStable(definitions, func(i, j int) bool {
		leftName := strings.TrimSpace(definitions[i].Name)
		rightName := strings.TrimSpace(definitions[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		leftDescription := strings.TrimSpace(definitions[i].Description)
		rightDescription := strings.TrimSpace(definitions[j].Description)
		return leftDescription < rightDescription
	})
}

func cloneToolParametersSchema(parameters map[string]interface{}) map[string]interface{} {
	canonical, _, err := toolschema.Canonicalize(parameters)
	if err == nil {
		return canonical
	}
	return cloneFunctionSchema(parameters)
}
