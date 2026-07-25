package skills

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextpack"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
)

const agentContextSummaryMaxBytes = 4096

func buildAgentContextMessages(contextValues map[string]interface{}, workspaceCtx *workspace.WorkspaceContext) []types.Message {
	messages := make([]types.Message, 0, 4)
	if environment := buildAgentEnvironmentContextMessage(contextValues); environment != nil {
		messages = append(messages, *environment)
	}
	if guidance := buildAgentShellGuidanceMessage(contextValues); guidance != nil {
		messages = append(messages, *guidance)
	}
	if guidance := buildAgentFileEditingGuidanceMessage(); guidance != nil {
		messages = append(messages, *guidance)
	}
	if workspaceCtx != nil && strings.TrimSpace(workspaceCtx.Summary) != "" {
		messages = append(messages, *types.NewSystemMessage("Workspace context: "+strings.TrimSpace(workspaceCtx.Summary)))
	}
	if summary := buildAgentContextSummary(contextValues); summary != "" {
		messages = append(messages, *types.NewSystemMessage("Runtime context summary:\n"+summary))
	}
	return messages
}

func buildAgentEnvironmentContextMessage(contextValues map[string]interface{}) *types.Message {
	workspacePath := agentContextWorkspacePath(contextValues)
	block := strings.TrimSpace(agentContextEnvironmentBlock(contextValues, workspacePath))
	if block == "" {
		return nil
	}
	return types.NewSystemMessage("Environment context:\n" + block)
}

func buildAgentShellGuidanceMessage(contextValues map[string]interface{}) *types.Message {
	capability := ""
	if len(contextValues) > 0 {
		if value, ok := contextValues[sessionmeta.EnvironmentCapabilityGuidance].(string); ok {
			capability = strings.TrimSpace(value)
		}
	}
	guidance := strings.TrimSpace(runtimeprompt.RenderShellExecutionGuidanceWithCapability(capability))
	if guidance == "" {
		return nil
	}
	return types.NewSystemMessage(guidance)
}

func buildAgentFileEditingGuidanceMessage() *types.Message {
	guidance := strings.TrimSpace(runtimeprompt.RenderFileEditingGuidance())
	if guidance == "" {
		return nil
	}
	return types.NewSystemMessage(guidance)
}

func agentContextWorkspacePath(contextValues map[string]interface{}) string {
	if len(contextValues) == 0 {
		return ""
	}
	if value, ok := contextValues["workspace_path"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func agentContextEnvironmentBlock(contextValues map[string]interface{}, workspacePath string) string {
	if len(contextValues) > 0 {
		if value, ok := contextValues[sessionmeta.EnvironmentContextBlock].(string); ok {
			if block := strings.TrimSpace(value); block != "" {
				return block
			}
		}
	}
	return strings.TrimSpace(runtimeprompt.RenderEnvironmentContextBlock(workspacePath))
}

// ensureSessionEnvironmentSnapshot freezes measured environment facts onto the
// durable chat session once. Subsequent turns reuse the stored snapshot so
// multi-turn history is not rewritten when host PATH/date facts change.
func ensureSessionEnvironmentSnapshot(session *chat.Session, workspacePath string) runtimeprompt.EnvironmentSnapshot {
	if session == nil {
		return runtimeprompt.CaptureEnvironmentSnapshot(workspacePath)
	}
	if snap, ok := loadSessionEnvironmentSnapshot(session); ok {
		return snap
	}
	snap := runtimeprompt.CaptureEnvironmentSnapshot(workspacePath)
	storeSessionEnvironmentSnapshot(session, snap)
	return snap
}

func loadSessionEnvironmentSnapshot(session *chat.Session) (runtimeprompt.EnvironmentSnapshot, bool) {
	if session == nil || session.Metadata.Context == nil {
		return runtimeprompt.EnvironmentSnapshot{}, false
	}
	contextBlock := strings.TrimSpace(sessionmeta.String(session.Metadata.Context, sessionmeta.EnvironmentContextBlock))
	if contextBlock == "" {
		return runtimeprompt.EnvironmentSnapshot{}, false
	}
	snap := runtimeprompt.EnvironmentSnapshot{
		ContextBlock:       contextBlock,
		CapabilityGuidance: strings.TrimSpace(sessionmeta.String(session.Metadata.Context, sessionmeta.EnvironmentCapabilityGuidance)),
		Values:             loadEnvironmentValuesMap(session.Metadata.Context),
	}
	if probedAt := strings.TrimSpace(sessionmeta.String(session.Metadata.Context, sessionmeta.EnvironmentProbedAt)); probedAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, probedAt); err == nil {
			snap.ProbedAt = parsed
		} else if parsed, err := time.Parse(time.RFC3339, probedAt); err == nil {
			snap.ProbedAt = parsed
		}
	}
	return snap, true
}

func storeSessionEnvironmentSnapshot(session *chat.Session, snap runtimeprompt.EnvironmentSnapshot) {
	if session == nil {
		return
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = make(map[string]interface{})
	}
	sessionmeta.Set(session.Metadata.Context, sessionmeta.EnvironmentContextBlock, strings.TrimSpace(snap.ContextBlock))
	sessionmeta.Set(session.Metadata.Context, sessionmeta.EnvironmentCapabilityGuidance, strings.TrimSpace(snap.CapabilityGuidance))
	if len(snap.Values) > 0 {
		sessionmeta.Set(session.Metadata.Context, sessionmeta.EnvironmentValues, cloneEnvironmentValuesMap(snap.Values))
	}
	probedAt := snap.ProbedAt
	if probedAt.IsZero() {
		probedAt = time.Now().UTC()
	}
	sessionmeta.Set(session.Metadata.Context, sessionmeta.EnvironmentProbedAt, probedAt.UTC().Format(time.RFC3339Nano))
}

func loadEnvironmentValuesMap(context map[string]interface{}) map[string]interface{} {
	if context == nil {
		return nil
	}
	value, ok := sessionmeta.Value(context, sessionmeta.EnvironmentValues)
	if !ok || value == nil {
		return nil
	}
	typed, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	return cloneEnvironmentValuesMap(typed)
}

func cloneEnvironmentValuesMap(values map[string]interface{}) map[string]interface{} {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case []string:
			cloned[key] = append([]string(nil), typed...)
		case []interface{}:
			cloned[key] = append([]interface{}(nil), typed...)
		default:
			cloned[key] = value
		}
	}
	return cloned
}

func applyEnvironmentSnapshotToAgentContext(agentContext map[string]interface{}, snap runtimeprompt.EnvironmentSnapshot) {
	if agentContext == nil {
		return
	}
	if block := strings.TrimSpace(snap.ContextBlock); block != "" {
		agentContext[sessionmeta.EnvironmentContextBlock] = block
	}
	if guidance := strings.TrimSpace(snap.CapabilityGuidance); guidance != "" {
		agentContext[sessionmeta.EnvironmentCapabilityGuidance] = guidance
	}
	for key, value := range snap.Values {
		agentContext[key] = value
	}
}

func prependContextMessages(history []types.Message, contextMessages []types.Message) []types.Message {
	if len(contextMessages) == 0 {
		cloned := make([]types.Message, len(history))
		for index := range history {
			cloned[index] = *history[index].Clone()
		}
		return cloned
	}

	merged := make([]types.Message, 0, len(contextMessages)+len(history))
	for _, message := range contextMessages {
		merged = append(merged, *message.Clone())
	}
	for _, message := range history {
		merged = append(merged, *message.Clone())
	}
	return merged
}

// stripLeadingContextMessages removes a leading ephemeral context prefix that
// was only injected for the current model request. Durable session history must
// not accumulate these system messages across multi-turn ReAct writebacks.
func stripLeadingContextMessages(history []types.Message, contextMessages []types.Message) []types.Message {
	if len(history) == 0 {
		return nil
	}
	if len(contextMessages) == 0 {
		return cloneAgentMessages(history)
	}
	if len(history) < len(contextMessages) {
		return cloneAgentMessages(history)
	}
	for index := range contextMessages {
		if !strings.EqualFold(strings.TrimSpace(history[index].Role), strings.TrimSpace(contextMessages[index].Role)) {
			return cloneAgentMessages(history)
		}
		if strings.TrimSpace(history[index].Content) != strings.TrimSpace(contextMessages[index].Content) {
			return cloneAgentMessages(history)
		}
	}
	return cloneAgentMessages(history[len(contextMessages):])
}

func cloneAgentMessages(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]types.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func buildAgentContextSummary(contextValues map[string]interface{}) string {
	if len(contextValues) == 0 {
		return ""
	}

	summary := map[string]interface{}{}
	if workspacePath, ok := contextValues["workspace_path"].(string); ok && strings.TrimSpace(workspacePath) != "" {
		summary["workspace_path"] = strings.TrimSpace(workspacePath)
	}
	profileLayer := false
	if pack, ok := contextValues["context_pack"].(map[string]interface{}); ok {
		if reduced := reduceAgentContextPack(pack); len(reduced) > 0 {
			summary["context_pack"] = reduced
		}
		_, profileLayer = pack["profile"].(map[string]interface{})
	}
	if permissions := agentContextStringSlice(contextValues["permissions"]); len(permissions) > 0 {
		summary["permissions"] = permissions
	}

	for key, value := range contextValues {
		if key == "context_pack" || key == "workspace_path" || key == "permissions" {
			continue
		}
		// Frozen environment prompt fragments are for request assembly only.
		if key == sessionmeta.EnvironmentContextBlock ||
			key == sessionmeta.EnvironmentCapabilityGuidance ||
			key == sessionmeta.EnvironmentProbedAt ||
			key == sessionmeta.EnvironmentValues ||
			isAgentEnvironmentFactKey(key) {
			continue
		}
		if profileLayer && strings.HasPrefix(key, "profile_") {
			continue
		}
		if agentContextScalar(value) {
			summary[key] = value
		}
	}

	if len(summary) == 0 {
		return ""
	}

	raw, err := json.Marshal(summary)
	if err != nil {
		return ""
	}
	if len(raw) > agentContextSummaryMaxBytes {
		raw = append(raw[:agentContextSummaryMaxBytes], []byte("...")...)
	}
	return string(raw)
}

func reduceAgentContextPack(pack map[string]interface{}) map[string]interface{} {
	return contextpack.Reduce(pack)
}

func summarizeAgentContextString(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func agentContextStringSlice(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

func limitAgentContextStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func copyAgentContextString(target map[string]interface{}, key string, value interface{}) {
	if target == nil {
		return
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		target[key] = strings.TrimSpace(text)
	}
}

func isAgentEnvironmentFactKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "os", "shell", "current_date", "timezone", "available_commands", "unavailable_commands":
		return true
	default:
		return false
	}
}

func agentContextScalar(value interface{}) bool {
	switch value.(type) {
	case string, bool, int, int32, int64, float32, float64, uint, uint32, uint64:
		return true
	default:
		return false
	}
}

func agentContextInt(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}
