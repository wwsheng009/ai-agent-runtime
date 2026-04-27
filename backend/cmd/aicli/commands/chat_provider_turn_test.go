package commands

import "testing"

func TestResponseHasTruncatedToolCalls_DetectsIncompleteMarkupWithoutLengthFinish(t *testing.T) {
	msg := map[string]interface{}{
		"content":       "<tool_call>write<arg_key>file_path</arg_key><arg_value>E:/tmp/demo.txt</arg_value>",
		"finish_reason": "stop",
	}

	if !responseHasTruncatedToolCalls(msg) {
		t.Fatal("expected incomplete tool call markup to be detected")
	}
}

func TestResponseHasTruncatedToolCalls_IgnoresCompleteToolCalls(t *testing.T) {
	msg := map[string]interface{}{
		"content":       "<tool_call>write<arg_key>file_path</arg_key><arg_value>E:/tmp/demo.txt</arg_value></tool_call>",
		"finish_reason": "stop",
	}

	if responseHasTruncatedToolCalls(msg) {
		t.Fatal("did not expect complete tool call markup to be treated as truncated")
	}
}
