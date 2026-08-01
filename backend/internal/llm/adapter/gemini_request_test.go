package adapter

import (
	"reflect"
	"testing"
)

func TestGeminiBuildRequest_NoneToolChoiceRetainsToolsAndDisablesCalling(t *testing.T) {
	a := &GeminiAdapter{}
	tools := []map[string]interface{}{{
		"functionDeclarations": []map[string]interface{}{{
			"name":       "view",
			"parameters": map[string]interface{}{"type": "object"},
		}},
	}}
	req := a.BuildRequest(RequestConfig{
		Model:      "gemini-2.5-pro",
		Messages:   []map[string]interface{}{{"role": "user", "content": "summarize"}},
		Functions:  tools,
		ToolChoice: "none",
	})

	if got := req["tools"]; !reflect.DeepEqual(got, tools) {
		t.Fatalf("expected frozen tools to be retained, got %#v", got)
	}
	toolConfig, ok := req["toolConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected Gemini toolConfig, got %#v", req["toolConfig"])
	}
	calling, ok := toolConfig["functionCallingConfig"].(map[string]interface{})
	if !ok || calling["mode"] != "NONE" {
		t.Fatalf("expected Gemini function calling mode NONE, got %#v", toolConfig)
	}
}

func TestGeminiBuildRequest_MapsRequiredToolChoiceToAny(t *testing.T) {
	req := (&GeminiAdapter{}).BuildRequest(RequestConfig{
		Messages:   []map[string]interface{}{{"role": "user", "content": "run it"}},
		ToolChoice: "required",
	})
	toolConfig, _ := req["toolConfig"].(map[string]interface{})
	calling, _ := toolConfig["functionCallingConfig"].(map[string]interface{})
	if calling["mode"] != "ANY" {
		t.Fatalf("expected Gemini function calling mode ANY, got %#v", toolConfig)
	}
}

func TestGeminiBuildRequest_PreservesCustomReasoningEffortOutsideBudgetCatalog(t *testing.T) {
	req := (&GeminiAdapter{}).BuildRequest(RequestConfig{
		Model:           "gemini-3.1-pro",
		Messages:        []map[string]interface{}{{"role": "user", "content": "run it"}},
		ReasoningEffort: " Provider-Custom ",
		ReasoningEffortBudgets: map[string]int{
			"high": 16384,
		},
	})

	generationConfig, ok := req["generationConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected generationConfig, got %#v", req)
	}
	thinkingConfig, ok := generationConfig["thinkingConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected thinkingConfig, got %#v", generationConfig)
	}
	if got := thinkingConfig["thinkingLevel"]; got != "Provider-Custom" {
		t.Fatalf("expected trimmed custom thinkingLevel, got %#v", got)
	}
	if _, exists := thinkingConfig["thinkingBudget"]; exists {
		t.Fatalf("expected custom effort not to be rewritten as a budget, got %#v", thinkingConfig)
	}
}
