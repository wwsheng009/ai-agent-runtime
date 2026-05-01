package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

var (
	exitCleanupMu sync.Mutex
	exitCleanupFn func()
)

func registerExitCleanup(cleanup func()) {
	exitCleanupMu.Lock()
	defer exitCleanupMu.Unlock()
	exitCleanupFn = cleanup
}

func runExitCleanup() {
	exitCleanupMu.Lock()
	cleanup := exitCleanupFn
	exitCleanupFn = nil
	exitCleanupMu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

func normalizeOutputFormat(raw string, fallback string, allowed ...string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(fallback))
	}

	allowedSet := make(map[string]struct{}, len(allowed))
	for _, item := range allowed {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		allowedSet[item] = struct{}{}
	}

	if _, ok := allowedSet[value]; !ok {
		return "", fmt.Errorf("无效的 output: %s（可选值: %s）", raw, strings.Join(allowed, "|"))
	}
	return value, nil
}

func marshalCompactJSONLine(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

type commandErrorPayload struct {
	OK      bool                   `json:"ok"`
	Command string                 `json:"command,omitempty"`
	Error   string                 `json:"error"`
	Details map[string]interface{} `json:"details,omitempty"`
}

type commandSuccessPayload struct {
	OK      bool        `json:"ok"`
	Command string      `json:"command,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

type structuredOutputOptions struct {
	Format   string
	Envelope bool
}

func executeStructuredCommand[T any](command string, outputOptions structuredOutputOptions, run func() (T, map[string]interface{}, error), renderJSON func(T) interface{}, renderText func(T)) {
	result, details, err := run()
	if err != nil {
		exitCommandError(command, outputOptions.Format, err, details)
	}
	if isJSONOutputFormat(outputOptions.Format) {
		payload := interface{}(result)
		if renderJSON != nil {
			payload = renderJSON(result)
		}
		printCommandJSONOutput(command, outputOptions.Envelope, payload)
		return
	}
	if renderText != nil {
		renderText(result)
	}
}

func executeCommand[T any](command string, outputOptions structuredOutputOptions, run func() (T, map[string]interface{}, error), onSuccess func(T, structuredOutputOptions)) {
	result, details, err := run()
	if err != nil {
		exitCommandError(command, outputOptions.Format, err, details)
	}
	if onSuccess != nil {
		onSuccess(result, outputOptions)
	}
}

func isJSONOutputFormat(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "json")
}

func useJSONEnvelope(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	value, err := cmd.Flags().GetBool("envelope")
	if err != nil {
		return false
	}
	return value
}

func resolveStructuredOutputOptions(cmd *cobra.Command, fallback string, allowed ...string) (structuredOutputOptions, error) {
	var rawOutput string
	if cmd != nil && cmd.Flags().Lookup("output") != nil {
		value, err := cmd.Flags().GetString("output")
		if err != nil {
			return structuredOutputOptions{}, err
		}
		rawOutput = value
	}

	if strings.TrimSpace(rawOutput) == "" && cmd != nil && cmd.Flags().Lookup("json") != nil {
		jsonAlias, err := cmd.Flags().GetBool("json")
		if err != nil {
			return structuredOutputOptions{}, err
		}
		if jsonAlias {
			rawOutput = "json"
		}
	}

	format, err := normalizeOutputFormat(rawOutput, fallback, allowed...)
	if err != nil {
		return structuredOutputOptions{}, err
	}
	return structuredOutputOptions{
		Format:   format,
		Envelope: useJSONEnvelope(cmd),
	}, nil
}

func formatCommandJSONOutput(command string, envelope bool, data interface{}) string {
	if !envelope {
		return marshalCompactJSONLine(data)
	}
	return marshalCompactJSONLine(commandSuccessPayload{
		OK:      true,
		Command: strings.TrimSpace(command),
		Data:    data,
	})
}

func printCommandJSONOutput(command string, envelope bool, data interface{}) {
	fmt.Fprintln(os.Stdout, formatCommandJSONOutput(command, envelope, data))
}

func printCommandActionJSON(command string, envelope bool, action string, data interface{}) {
	if envelope {
		printCommandJSONOutput(command, envelope, mergeActionIntoPayload(data, action, false))
		return
	}
	printCommandJSONOutput(command, envelope, mergeActionIntoPayload(data, action, true))
}

func mergeActionIntoPayload(data interface{}, action string, includeOK bool) map[string]interface{} {
	payload := make(map[string]interface{})
	if data != nil {
		bytes, err := json.Marshal(data)
		if err == nil {
			_ = json.Unmarshal(bytes, &payload)
		}
	}
	if strings.TrimSpace(action) != "" {
		payload["action"] = strings.TrimSpace(action)
	}
	if includeOK {
		payload["ok"] = true
	}
	return payload
}

func emitCommandError(command, outputFormat string, err error, details map[string]interface{}) {
	if err == nil {
		return
	}
	if isJSONOutputFormat(outputFormat) {
		fmt.Fprintln(os.Stdout, marshalCompactJSONLine(commandErrorPayload{
			OK:      false,
			Command: strings.TrimSpace(command),
			Error:   err.Error(),
			Details: details,
		}))
		return
	}
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func exitCommandError(command, outputFormat string, err error, details map[string]interface{}) {
	emitCommandError(command, outputFormat, err, details)
	runExitCleanup()
	os.Exit(1)
}

func extractUsageFromAnyResponseBody(raw []byte) map[string]interface{} {
	return runtimellm.TokenUsageToMap(runtimellm.ExtractTokenUsageFromResponseBody(raw))
}
