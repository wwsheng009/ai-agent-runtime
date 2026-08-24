package commands

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// persistedHistorySeedKind is deliberately narrower than encoding.ItemKind.
// It describes the canonical persisted transcript projection before it is
// merged with the best-effort runtime event log.
type persistedHistorySeedKind uint8

const (
	persistedHistorySeedUser persistedHistorySeedKind = iota
	persistedHistorySeedAssistant
	persistedHistorySeedSupplement
	persistedHistorySeedTool
)

type persistedHistorySeedUnit struct {
	identity         string
	kind             persistedHistorySeedKind
	content          string
	boundaryGroupKey string

	toolCallID string
	toolName   string
	toolOutput string
	toolError  string
	success    bool
}

// seedPersistedHistory reconciles canonical persisted history with the Scene
// model rebuilt from the runtime event log. The log is not durable transcript
// authority: a non-empty Scene can legitimately contain only part of the
// canonical conversation. Every unit therefore has a deterministic identity
// and is either matched to an existing semantic item or imported once.
func (b *chatRuntimeEventBridge) seedPersistedHistory(messages []runtimetypes.Message, header string) {
	if b == nil || b.renderEncoder == nil || len(messages) == 0 {
		return
	}

	units := buildPersistedHistorySeedUnits(messages)
	if len(units) == 0 {
		return
	}

	b.renderMu.Lock()
	b.seedPersistedHistoryLocked(units, header)
	b.renderMu.Unlock()
	b.sessionInteractionSnapshot()
}

// seedPersistedHistoryLocked is the render-transaction half of history seed.
// It exists so destructive transcript replacement can rebuild the encoder and
// Scene under the same renderMu ownership before publishing one new snapshot.
func (b *chatRuntimeEventBridge) seedPersistedHistoryLocked(units []persistedHistorySeedUnit, header string) {
	if b == nil || b.renderEncoder == nil ||
		(len(units) == 0 && strings.TrimSpace(header) == "") {
		return
	}
	if b.historySeedSeen == nil {
		b.historySeedSeen = make(map[string]struct{})
	}
	snapshot := b.renderEncoder.Snapshot()
	matchedItemIDs := make(map[string]struct{})
	headerSeededNow := false
	// A history header must lead the imported transcript. Once a partial event
	// log already owns the prefix, appending the header would put it in the
	// middle of canonical history, so preserve order and reconcile rows only.
	if (snapshot == nil || len(snapshot.Items) == 0) && strings.TrimSpace(header) != "" {
		headerUnit := persistedHistorySeedUnit{
			identity: persistedHistoryHeaderIdentity(header, units),
			kind:     persistedHistorySeedSupplement,
			content:  header,
		}
		if _, alreadySeeded := b.historySeedSeen[headerUnit.identity]; !alreadySeeded {
			headerUnit.apply(b)
			b.historySeedSeen[headerUnit.identity] = struct{}{}
			headerSeededNow = true
		}
	}

	// Reconcile all canonical units before importing any missing one. Besides
	// preserving occurrence order, the first pass lets a missing reasoning or
	// assistant section adopt the exact runtime request identity of its matched
	// sibling. A one-pass append would assign the deterministic persisted key
	// before discovering that the other section already has a live/replayed key.
	if strings.TrimSpace(header) != "" {
		headerUnit := persistedHistorySeedUnit{
			identity: persistedHistoryHeaderIdentity(header, units),
			kind:     persistedHistorySeedSupplement,
			content:  header,
		}
		if _, seeded := b.historySeedSeen[headerUnit.identity]; seeded && !headerSeededNow {
			persistedHistoryUnitMatch(snapshot, headerUnit, matchedItemIDs)
		}
	}
	matchedUnits := make(map[string]*encoding.Item, len(units))
	groupAliases := make(map[string]string)
	for _, unit := range units {
		item := persistedHistoryUnitMatch(snapshot, unit, matchedItemIDs)
		if item == nil {
			continue
		}
		matchedUnits[unit.identity] = item
		if unit.boundaryGroupKey != "" && item.BoundaryGroupKey != "" {
			if _, exists := groupAliases[unit.boundaryGroupKey]; !exists {
				groupAliases[unit.boundaryGroupKey] = item.BoundaryGroupKey
			}
		}
	}
	for _, unit := range units {
		if _, alreadySeeded := b.historySeedSeen[unit.identity]; alreadySeeded {
			continue
		}
		if matchedUnits[unit.identity] != nil {
			b.historySeedSeen[unit.identity] = struct{}{}
			continue
		}
		if alias := groupAliases[unit.boundaryGroupKey]; alias != "" {
			unit.boundaryGroupKey = alias
		}
		unit.apply(b)
		b.historySeedSeen[unit.identity] = struct{}{}
	}
}

// replaceCanonicalHistoryProjection rebuilds the owned transcript from the
// post-mutation canonical history. Backtrack removes durable conversation
// content, so append-only reconciliation is incorrect: old Scene cells must
// disappear before the surviving canonical cells and follow-up command result
// are committed. The caller has already completed the domain mutation and no
// active model run may be rendering into this bridge.
//
// The history-reset marker is persisted in the runtime event log. On a later
// startup replay it discards pre-backtrack event rows before reseeding this
// canonical snapshot, preventing deleted turns from returning to the Scene.
func (b *chatRuntimeEventBridge) replaceCanonicalHistoryProjection(messages []runtimetypes.Message, header string) bool {
	if b == nil {
		return false
	}
	units := buildPersistedHistorySeedUnits(messages)
	b.renderMu.Lock()
	if b.runActive {
		b.renderMu.Unlock()
		return false
	}
	b.resetCanonicalHistoryProjectionLocked()
	b.seedPersistedHistoryLocked(units, header)
	b.appendHistoryResetLog(messages, header)
	b.renderMu.Unlock()
	b.sessionInteractionSnapshot()
	return true
}

// resetCanonicalHistoryProjectionLocked clears all derived render state. It
// intentionally does not infer a suffix from display rows: the caller supplies
// canonical source and the next seed recreates stable semantic identities.
// Caller must hold renderMu.
func (b *chatRuntimeEventBridge) resetCanonicalHistoryProjectionLocked() {
	if b == nil {
		return
	}
	b.renderEncoder = encoding.NewEventEncoder()
	// bridge 构造时启用了 reasoning ordering barrier（chat_runtime_events.go
	// newChatRuntimeEventBridge）；重建 encoder 必须恢复同一配置，否则
	// replayEventLog 重放出的 Scene 与 live 路径（assistant.message 的
	// barrier 解除 upsert）不一致，破坏 replay 等价（live/replay cell
	// Revision 漂移）。
	b.renderEncoder.EnableReasoningOrderingBarrier(true)
	b.historySeedSeen = make(map[string]struct{})
	b.interactionAnchorMu.Lock()
	b.interactionAnchor = nil
	b.interactionAnchorAt = time.Time{}
	b.interactionAnchorSource = ""
	b.pendingInteractionSource = ""
	b.pendingInteractionTail = nil
	b.interactionAnchorMu.Unlock()

	b.sceneMu.Lock()
	b.renderScene = scene.New()
	b.renderMapper = scene.NewChangeSetMapper(b.renderScene)
	b.sceneApplyFailures = 0
	b.sceneLastError = ""
	b.sceneMu.Unlock()
}

func buildPersistedHistorySeedUnits(messages []runtimetypes.Message) []persistedHistorySeedUnit {
	toolCalls := indexChatHistoryToolCalls(messages)
	units := make([]persistedHistorySeedUnit, 0, len(messages))
	occurrences := make(map[string]uint64)
	assistantRequestOccurrences := make(map[string]uint64)
	appendUnit := func(unit persistedHistorySeedUnit) {
		base := unit.stableKey()
		occurrences[base]++
		unit.identity = fmt.Sprintf("persisted-history:%s:%d", base, occurrences[base])
		units = append(units, unit)
	}

	for index := range messages {
		message := messages[index]
		role := strings.ToLower(strings.TrimSpace(message.Role))
		content := message.Content
		switch role {
		case "user":
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{kind: persistedHistorySeedUser, content: content})
			}
		case "assistant":
			var reasoningDisplay string
			if reasoning := finalReasoningBlock(&message); reasoning != nil {
				reasoningDisplay = reasoning.RawDisplayText()
			}
			requestBase := persistedAssistantRequestStableKey(content, reasoningDisplay)
			assistantRequestOccurrences[requestBase]++
			groupKey := fmt.Sprintf("persisted-assistant-request:%s:%d",
				requestBase, assistantRequestOccurrences[requestBase])
			if strings.TrimSpace(reasoningDisplay) != "" {
				appendUnit(persistedHistorySeedUnit{
					kind: persistedHistorySeedSupplement, content: reasoningDisplay,
					boundaryGroupKey: groupKey,
				})
			}
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{
					kind: persistedHistorySeedAssistant, content: content,
					boundaryGroupKey: groupKey,
				})
			}
		case "tool":
			callID := strings.TrimSpace(message.ToolCallID)
			call := toolCalls[callID]
			output, toolErr := splitChatHistoryToolResult(message)
			if output == "" && toolErr == "" {
				output = content
			}
			name := firstNonEmptyChatValue(strings.TrimSpace(call.Name), callID, "tool")
			if callID == "" {
				fallback := firstNonEmptyChatValue(output, toolErr, content)
				if strings.TrimSpace(fallback) != "" {
					appendUnit(persistedHistorySeedUnit{
						kind:    persistedHistorySeedSupplement,
						content: fmt.Sprintf("[tool] %s", fallback),
					})
				}
				continue
			}
			appendUnit(persistedHistorySeedUnit{
				kind:       persistedHistorySeedTool,
				toolCallID: callID,
				toolName:   name,
				toolOutput: output,
				toolError:  toolErr,
				success:    strings.TrimSpace(toolErr) == "",
			})
		case "system":
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{kind: persistedHistorySeedSupplement, content: content})
			}
		default:
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{
					kind:    persistedHistorySeedSupplement,
					content: fmt.Sprintf("[%s] %s", role, content),
				})
			}
		}
	}
	return units
}

func persistedAssistantRequestStableKey(content, reasoning string) string {
	var builder strings.Builder
	for _, value := range []string{content, reasoning} {
		fmt.Fprintf(&builder, "%d:%s|", len(value), value)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", sum[:])
}

func (u persistedHistorySeedUnit) stableKey() string {
	var builder strings.Builder
	for _, value := range []string{
		strconv.Itoa(int(u.kind)), u.content, u.toolCallID, u.toolName,
		u.toolOutput, u.toolError, strconv.FormatBool(u.success),
	} {
		fmt.Fprintf(&builder, "%d:%s|", len(value), value)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", sum[:])
}

func persistedHistoryHeaderIdentity(header string, units []persistedHistorySeedUnit) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%d:%s|", len(header), header)
	for _, unit := range units {
		fmt.Fprintf(&builder, "%d:%s|", len(unit.identity), unit.identity)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("persisted-history-header:%x", sum[:])
}

func persistedHistoryUnitMatch(snapshot *encoding.RenderModel, unit persistedHistorySeedUnit, matched map[string]struct{}) *encoding.Item {
	if snapshot == nil {
		return nil
	}
	for _, item := range snapshot.Items {
		if item == nil || item.ID == "" {
			continue
		}
		if _, used := matched[item.ID]; used || !unit.matches(item) {
			continue
		}
		matched[item.ID] = struct{}{}
		return item
	}
	return nil
}

func (u persistedHistorySeedUnit) matches(item *encoding.Item) bool {
	if item == nil || !item.Status.Terminal() {
		return false
	}
	switch u.kind {
	case persistedHistorySeedUser:
		return item.Kind == encoding.KindUser && item.Head == u.content
	case persistedHistorySeedAssistant:
		return item.Kind == encoding.KindAssistant && item.Head == u.content
	case persistedHistorySeedSupplement:
		if item.Kind == encoding.KindReasoning && u.boundaryGroupKey != "" {
			return persistedReasoningContentMatches(item.Head, u.content)
		}
		return (item.Kind == encoding.KindSupplement || item.Kind == encoding.KindReasoning || item.Kind == encoding.KindSystem) && item.Head == u.content
	case persistedHistorySeedTool:
		return item.Kind == encoding.KindToolCall && item.Head == u.toolHead()
	default:
		return false
	}
}

func persistedReasoningContentMatches(head, content string) bool {
	if head == content {
		return true
	}
	head = strings.ReplaceAll(head, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	firstLF := strings.IndexByte(head, '\n')
	if firstLF < 0 {
		return false
	}
	body := strings.TrimLeft(head[firstLF+1:], "\n")
	if lastLF := strings.LastIndexByte(body, '\n'); lastLF >= 0 &&
		strings.Contains(strings.ToLower(body[lastLF+1:]), "end reasoning") {
		body = body[:lastLF]
	}
	return body == strings.TrimLeft(content, "\n")
}

func (u persistedHistorySeedUnit) toolHead() string {
	result := u.toolOutput
	if strings.TrimSpace(result) == "" {
		result = u.toolError
	}
	if result == "" {
		return u.toolName
	}
	return u.toolName + "\n" + result
}

func (u persistedHistorySeedUnit) apply(b *chatRuntimeEventBridge) {
	if b == nil || b.renderEncoder == nil {
		return
	}
	switch u.kind {
	case persistedHistorySeedUser:
		b.applyChangeSet(b.renderEncoder.SubmitUserInput(u.content))
	case persistedHistorySeedAssistant:
		b.applyChangeSet(b.renderEncoder.SubmitAssistantWithBoundaryGroup(u.content, u.boundaryGroupKey))
	case persistedHistorySeedSupplement:
		if u.boundaryGroupKey != "" {
			// 带请求身份的 supplement 是重建的 reasoning（persisted-
			// assistant-request:*）。必须以与 live 路径一致的
			// KindReasoning + divider Head 导入，否则恢复会话后
			// reasoning 正文会退化为普通 supplement 文本，丢失
			// "…… reasoning ……" 与 "…… end reasoning ……" 分隔线。
			b.applyChangeSet(b.renderEncoder.SubmitReasoningWithBoundaryGroup(u.content, u.boundaryGroupKey))
		} else {
			b.applyChangeSet(b.renderEncoder.SubmitSupplementWithBoundaryGroup(u.content, u.boundaryGroupKey))
		}
	case persistedHistorySeedTool:
		// A persisted tool result is final history, not a viewport-only running
		// row. Establish the stable call identity before the result so the
		// encoder maps both mutations to one committed tool-chain Scene cell.
		b.applyChangeSet(b.renderEncoder.SubmitToolCall(u.toolCallID, u.toolName, nil))
		b.applyChangeSet(b.renderEncoder.SubmitToolResult(
			u.toolCallID, u.toolName, u.toolOutput, u.toolError, u.success,
		))
	}
}
