package ui

import (
	"context"
	"io"
)

// Test-only wrappers around the context-aware editor entry point. Production
// interactive reads go through readPromptWithHooksContext.
func readInteractiveLine(reader io.Reader, writer io.Writer, prompt string, history []string, onChange func(string)) (string, error) {
	return readInteractiveLineWithOptions(reader, writer, prompt, history, onChange, true, defaultPasteBurstHoldFirstRune())
}

func readInteractiveLineWithOptions(reader io.Reader, writer io.Writer, prompt string, history []string, onChange func(string), echoSubmit bool, holdFirstRune bool) (string, error) {
	return readInteractiveLineWithHooks(reader, writer, prompt, history, onChange, nil, echoSubmit, holdFirstRune)
}

func readInteractiveLineWithHooks(reader io.Reader, writer io.Writer, prompt string, history []string, onChange func(string), hooks *LineEditorHooks, echoSubmit bool, holdFirstRune bool) (string, error) {
	return readInteractiveLineWithHooksContext(context.Background(), reader, writer, prompt, history, onChange, hooks, echoSubmit, holdFirstRune)
}
