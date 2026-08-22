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
	matched := make(map[string]struct{})
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
		}
	}
	// Match the whole persisted projection before importing any missing unit.
	// This lets a missing reasoning predecessor adopt the exact request key from
	// an already-restored assistant (and vice versa), independent of unit order.
	present := make(map[string]*encoding.Item)
	groupAliases := make(map[string]string)
	for _, unit := range units {
		if _, alreadySeeded := b.historySeedSeen[unit.identity]; alreadySeeded {
			continue
		}
		if item := persistedHistoryUnitMatch(snapshot, unit, matched); item != nil {
			present[unit.identity] = item
			if unit.boundaryGroupKey != "" && item.BoundaryGroupKey != "" {
				groupAliases[unit.boundaryGroupKey] = item.BoundaryGroupKey
			}
		}
	}
	for _, unit := range units {
		if _, alreadySeeded := b.historySeedSeen[unit.identity]; alreadySeeded {
			continue
		}
		if present[unit.identity] != nil {
			b.historySeedSeen[unit.identity] = struct{}{}
			continue
		}
		if adopted := groupAliases[unit.boundaryGroupKey]; adopted != "" {
			unit.boundaryGroupKey = adopted
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
			groupKey := persistedAssistantBoundaryGroupKey(index, &message)
			if reasoning := finalReasoningBlock(&message); reasoning != nil {
				if display := reasoning.RawDisplayText(); strings.TrimSpace(display) != "" {
					appendUnit(persistedHistorySeedUnit{
						kind: persistedHistorySeedSupplement, content: display, boundaryGroupKey: groupKey,
					})
				}
			}
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{
					kind: persistedHistorySeedAssistant, content: content, boundaryGroupKey: groupKey,
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

func persistedAssistantBoundaryGroupKey(index int, message *runtimetypes.Message) string {
	if message == nil {
		return ""
	}
	reasoning := ""
	if block := finalReasoningBlock(message); block != nil {
		reasoning = block.RawDisplayText()
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", index, message.Content, reasoning)))
	return fmt.Sprintf("persisted-assistant-request:%x", sum[:])
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
		return (item.Kind == encoding.KindSupplement || item.Kind == encoding.KindReasoning || item.Kind == encoding.KindSystem) && item.Head == u.content
	case persistedHistorySeedTool:
		return item.Kind == encoding.KindToolCall && item.Head == u.toolHead()
	default:
		return false
	}
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
		b.applyChangeSet(b.renderEncoder.SubmitSupplementWithBoundaryGroup(u.content, u.boundaryGroupKey))
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
