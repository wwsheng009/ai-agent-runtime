package llm

import (
	"fmt"
	"testing"
)

func TestIsUnsupportedRequestParameter(t *testing.T) {
	for _, message := range []string{
		"HTTP 400: unsupported parameter: parallel_tool_calls",
		"unknown parameter 'parallel_tool_calls'",
		"parallel_tool_calls is not supported by this model",
		"extra inputs are not permitted: parallel_tool_calls [type=extra_forbidden]",
		"unknown field parallel_tool_calls",
	} {
		if !IsUnsupportedRequestParameter(fmt.Errorf("%s", message), MetadataKeyParallelToolCalls) {
			t.Fatalf("expected unsupported parameter match for %q", message)
		}
	}
	if IsUnsupportedRequestParameter(fmt.Errorf("HTTP 502: upstream unavailable"), MetadataKeyParallelToolCalls) {
		t.Fatal("did not expect unrelated provider error to match")
	}
	if IsUnsupportedRequestParameter(fmt.Errorf("unsupported parameter: temperature"), MetadataKeyParallelToolCalls) {
		t.Fatal("did not expect a different parameter to match")
	}
}
