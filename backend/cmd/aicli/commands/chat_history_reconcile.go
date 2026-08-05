package commands

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
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
	identity string
	kind     persistedHistorySeedKind
	content  string

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
	for _, unit := range units {
		if _, alreadySeeded := b.historySeedSeen[unit.identity]; alreadySeeded {
			continue
		}
		if persistedHistoryUnitPresent(snapshot, unit, matched) {
			b.historySeedSeen[unit.identity] = struct{}{}
			continue
		}
		unit.apply(b)
		b.historySeedSeen[unit.identity] = struct{}{}
	}
	b.renderMu.Unlock()
	b.sessionInteractionSnapshot()
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
			if reasoning := finalReasoningBlock(&message); reasoning != nil {
				if display := reasoning.RawDisplayText(); strings.TrimSpace(display) != "" {
					appendUnit(persistedHistorySeedUnit{kind: persistedHistorySeedSupplement, content: display})
				}
			}
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{kind: persistedHistorySeedAssistant, content: content})
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

func persistedHistoryUnitPresent(snapshot *encoding.RenderModel, unit persistedHistorySeedUnit, matched map[string]struct{}) bool {
	if snapshot == nil {
		return false
	}
	for _, item := range snapshot.Items {
		if item == nil || item.ID == "" {
			continue
		}
		if _, used := matched[item.ID]; used || !unit.matches(item) {
			continue
		}
		matched[item.ID] = struct{}{}
		return true
	}
	return false
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
		b.applyChangeSet(b.renderEncoder.SubmitAssistant(u.content))
	case persistedHistorySeedSupplement:
		b.applyChangeSet(b.renderEncoder.SubmitSupplement(u.content))
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
