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
