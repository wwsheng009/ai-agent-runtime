package commands

import (
	"sort"
	"strings"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func toolDefinitionsFromSelection(selection *aicliFunctionSelection) []runtimetypes.ToolDefinition {
	if selection == nil || len(selection.Schemas) == 0 {
		return nil
	}
	definitions := make([]runtimetypes.ToolDefinition, 0, len(selection.Schemas))
	for _, schema := range selection.Schemas {
		name, _ := schema["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		description, _ := schema["description"].(string)
		parameters, _ := schema["parameters"].(map[string]interface{})
		metadata, _ := schema["metadata"].(map[string]interface{})
		definitions = append(definitions, runtimetypes.ToolDefinition{
			Name:        strings.TrimSpace(name),
			Description: strings.TrimSpace(description),
			Parameters:  cloneFunctionSchema(parameters),
			Metadata:    cloneFunctionSchema(metadata),
		})
	}
	sortToolDefinitions(definitions)
	return definitions
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
			"parameters":  cloneFunctionSchema(def.Parameters),
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
