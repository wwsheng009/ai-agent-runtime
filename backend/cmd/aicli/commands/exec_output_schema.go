package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func validateExecFinalMessageSchema(schemaRef string, message string) error {
	schemaRef = strings.TrimSpace(schemaRef)
	if schemaRef == "" {
		return nil
	}
	schemaBytes, err := readExecSchema(schemaRef)
	if err != nil {
		return err
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return fmt.Errorf("解析 output schema 失败: %w", err)
	}
	var payload interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(message)), &payload); err != nil {
		return fmt.Errorf("最终 assistant 消息不是合法 JSON: %w", err)
	}
	return validateExecSchemaShape(schema, payload)
}

func readExecSchema(schemaRef string) ([]byte, error) {
	trimmed := strings.TrimSpace(schemaRef)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return []byte(trimmed), nil
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return nil, fmt.Errorf("读取 output schema 失败: %w", err)
	}
	return data, nil
}

func validateExecSchemaShape(schema map[string]interface{}, payload interface{}) error {
	if schema == nil {
		return nil
	}
	if expectedType, _ := schema["type"].(string); expectedType != "" {
		if err := validateExecJSONType(expectedType, payload); err != nil {
			return err
		}
	}
	required, _ := schema["required"].([]interface{})
	if len(required) == 0 {
		return nil
	}
	obj, ok := payload.(map[string]interface{})
	if !ok {
		return fmt.Errorf("schema required 只支持 object 输出")
	}
	for _, item := range required {
		key, _ := item.(string)
		if key == "" {
			continue
		}
		if _, ok := obj[key]; !ok {
			return fmt.Errorf("schema required 字段缺失: %s", key)
		}
	}
	return nil
}

func validateExecJSONType(expected string, payload interface{}) error {
	switch strings.ToLower(strings.TrimSpace(expected)) {
	case "", "any":
		return nil
	case "object":
		if _, ok := payload.(map[string]interface{}); !ok {
			return fmt.Errorf("schema type 期望 object")
		}
	case "array":
		if _, ok := payload.([]interface{}); !ok {
			return fmt.Errorf("schema type 期望 array")
		}
	case "string":
		if _, ok := payload.(string); !ok {
			return fmt.Errorf("schema type 期望 string")
		}
	case "number":
		if _, ok := payload.(float64); !ok {
			return fmt.Errorf("schema type 期望 number")
		}
	case "boolean":
		if _, ok := payload.(bool); !ok {
			return fmt.Errorf("schema type 期望 boolean")
		}
	default:
		return nil
	}
	return nil
}
