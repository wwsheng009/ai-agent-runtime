package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

var ErrRemoteCompactUnsupported = errors.New("remote compact unsupported")

type RemoteCompactRequest struct {
	SessionID          string
	TaskID             string
	Provider           string
	Model              string
	History            []types.Message
	KeepRecentMessages int
	Phase              string
	TriggerTokenLimit  int
	MaxContextTokens   int
}

type RemoteCompactResponse struct {
	ReplacementHistory []types.Message
	CompactedMessages  int
	CheckpointIDs      []string
	Usage              *types.TokenUsage
	UsageSource        string
}

type RemoteCompactionProvider interface {
	RemoteCompact(ctx context.Context, req RemoteCompactRequest) (*RemoteCompactResponse, error)
}

const remoteCompactPlaceholder = "Compacted context stored remotely."

func buildCodexRemoteCompactRequest(model string, history []types.Message) map[string]interface{} {
	codexAdapter := &adapter.CodexAdapter{}
	built := codexAdapter.BuildRequest(adapter.RequestConfig{
		Model:    strings.TrimSpace(model),
		Messages: RuntimeMessagesToProtocolMessages(history, "codex", model),
	})

	request := map[string]interface{}{
		"model":               built["model"],
		"input":               built["input"],
		"tools":               []interface{}{},
		"parallel_tool_calls": false,
	}
	if instructions, ok := built["instructions"].(string); ok && strings.TrimSpace(instructions) != "" {
		request["instructions"] = instructions
	}
	if tools, ok := built["tools"]; ok && tools != nil {
		request["tools"] = tools
	}
	if parallel, ok := built["parallel_tool_calls"]; ok {
		request["parallel_tool_calls"] = parallel
	}
	if reasoning, ok := built["reasoning"]; ok && reasoning != nil {
		request["reasoning"] = reasoning
	}
	if text, ok := built["text"]; ok && text != nil {
		request["text"] = text
	}
	return request
}

func decodeCodexRemoteCompactResponse(history []types.Message, body []byte) (*RemoteCompactResponse, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to decode remote compact response: %w", err)
	}

	output := canonicalizeCodexOutputItems(payload["output"])
	if len(output) == 0 {
		return nil, fmt.Errorf("remote compact response missing output items")
	}
	replacement := cloneRuntimeMessages(filterSystemMessages(history))
	addedMessages := 0
	for _, item := range output {
		if message := codexRemoteOutputItemToMessage(item); message != nil {
			replacement = append(replacement, *message)
			addedMessages++
		}
	}
	if addedMessages == 0 {
		return nil, fmt.Errorf("remote compact response produced no replayable messages")
	}
	var usage *types.TokenUsage
	if rawUsage, ok := payload["usage"]; ok {
		usage = normalizeUsageValue(rawUsage)
	}
	usageSource := ""
	if rawSource, ok := payload["usage_source"]; ok {
		usageSource = strings.TrimSpace(fmt.Sprintf("%v", rawSource))
	}
	return &RemoteCompactResponse{
		ReplacementHistory: replacement,
		CompactedMessages:  maxRemoteCompactedMessages(len(history), len(replacement)),
		Usage:              usage,
		UsageSource:        usageSource,
	}, nil
}

func codexRemoteOutputItemToMessage(item map[string]interface{}) *types.Message {
	if len(item) == 0 {
		return nil
	}
	itemType, _ := item["type"].(string)
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "message":
		return codexRemoteMessageItemToMessage(item)
	case "compaction":
		return codexOpaqueOutputItemMessage(item)
	default:
		return nil
	}
}

func codexRemoteMessageItemToMessage(item map[string]interface{}) *types.Message {
	role := strings.ToLower(strings.TrimSpace(stringValue(item["role"])))
	content := codexOutputText(item["content"])
	switch role {
	case "assistant":
		return types.NewAssistantMessage(content)
	case "user":
		return types.NewUserMessage(content)
	case "developer", "system":
		return types.NewSystemMessage(content)
	default:
		return nil
	}
}

func codexOpaqueOutputItemMessage(item map[string]interface{}) *types.Message {
	message := types.NewAssistantMessage(remoteCompactPlaceholder)
	if message == nil {
		return nil
	}
	message.Metadata["context_stage"] = "compaction"
	message.Metadata["compact_mode"] = "remote"
	types.SetReasoningBlock(message.Metadata, &types.ReasoningBlock{
		Format:         "openai_responses",
		Streamable:     true,
		ReplayRequired: true,
		Visibility:     types.ReasoningVisibilityOpaque,
		Metadata: map[string]interface{}{
			reasoningMetadataCodexOutputItemsKey: []map[string]interface{}{item},
		},
	})
	return message
}

func codexOutputText(raw interface{}) string {
	parts := decodeSliceOfMaps(raw)
	if len(parts) == 0 {
		return strings.TrimSpace(stringValue(raw))
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == nil {
			continue
		}
		if text, ok := part["text"].(string); ok && strings.TrimSpace(text) != "" {
			texts = append(texts, strings.TrimSpace(text))
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

func filterSystemMessages(history []types.Message) []types.Message {
	if len(history) == 0 {
		return nil
	}
	filtered := make([]types.Message, 0, len(history))
	for _, message := range history {
		if strings.EqualFold(strings.TrimSpace(message.Role), "system") {
			filtered = append(filtered, *message.Clone())
		}
	}
	return filtered
}

func cloneRuntimeMessages(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]types.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func maxRemoteCompactedMessages(before, after int) int {
	if before <= after {
		return 0
	}
	return before - after
}

func resolveCompactAPIPath(configuredPath, defaultPath string) string {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return defaultPath
	}
	normalized := "/" + strings.Trim(strings.TrimSpace(configuredPath), "/")
	if strings.HasSuffix(normalized, "/compact") {
		return normalized
	}
	if strings.HasSuffix(strings.TrimSpace(defaultPath), "/compact") {
		return normalized + "/compact"
	}
	return normalized
}

func resolveCompactURL(baseURL, configuredPath, defaultPath string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	apiPath := resolveCompactAPIPath(configuredPath, defaultPath)
	return agentconfig.JoinBaseURLAndPath(baseURL, apiPath)
}

func sendRemoteCompactRequest(
	ctx context.Context,
	client *http.Client,
	url string,
	headers map[string]string,
	requestBody map[string]interface{},
) ([]byte, int, error) {
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal remote compact request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create remote compact request: %w", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send remote compact request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read remote compact response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseBody, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(responseBody))
	}
	return responseBody, resp.StatusCode, nil
}

func buildCodexRemoteCompactHeaders(apiKey string, timeout time.Duration, requestBody map[string]interface{}, headers map[string]string) map[string]string {
	codexAdapter := &adapter.CodexAdapter{}
	return codexAdapter.BuildHeaders(adapter.AdapterConfig{
		Type:        "codex",
		APIKey:      apiKey,
		Timeout:     timeout,
		Model:       stringValue(requestBody["model"]),
		RequestBody: requestBody,
		Headers:     headers,
	})
}
