package memorystore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var (
	memoryCitationBlockRE = regexp.MustCompile(`(?is)<agent-memory-citation(?:\s[^>]*)?>(.*?)</agent-memory-citation\s*>`)
	memoryCitationEntryRE = regexp.MustCompile(`(?i)<entry\b[^>]*\bid\s*=\s*["']([^"']+)["'][^>]*/?>`)
)

type CitationResult struct {
	CleanText string
	Cited     []string
	Invalid   []string
	ParseErr  string
}

type AnswerExposureInput struct {
	Answer      string
	SessionID   string
	TurnID      string
	MessageID   string
	InjectedIDs []string
	// CoreInjectedIDs contains synthetic Core identities exposed in the
	// context. They share the citation allowlist but are written only to the
	// dedicated memory_core_usage ledger, never memory_usage.
	CoreInjectedIDs []string
	ReadIDs         []string
	// ReadUsageRecorded is set when memory_read already wrote UsageRead rows
	// for ReadIDs. The ids still participate in the citation allowlist and
	// injected_only exclusion, but are not counted twice.
	ReadUsageRecorded bool
}

// ParseMemoryCitations strips hidden citation blocks from the user-visible
// answer and extracts only explicitly cited ids that are present in the
// caller-provided allowlist.
func ParseMemoryCitations(answer string, allowedIDs []string) CitationResult {
	result := CitationResult{CleanText: answer}
	allowed := make(map[string]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			allowed[id] = struct{}{}
		}
	}
	blocks := memoryCitationBlockRE.FindAllStringSubmatchIndex(answer, -1)
	if len(blocks) > 0 {
		var clean strings.Builder
		last := 0
		for _, indexes := range blocks {
			clean.WriteString(answer[last:indexes[0]])
			inner := answer[indexes[2]:indexes[3]]
			for _, entry := range memoryCitationEntryRE.FindAllStringSubmatch(inner, -1) {
				id := strings.TrimSpace(entry[1])
				if id == "" {
					continue
				}
				if _, ok := allowed[id]; !ok {
					result.Invalid = appendUniqueCitationID(result.Invalid, id)
					continue
				}
				result.Cited = appendUniqueCitationID(result.Cited, id)
			}
			last = indexes[1]
		}
		clean.WriteString(answer[last:])
		result.CleanText = strings.TrimSpace(clean.String())
	}
	if strings.Contains(strings.ToLower(result.CleanText), "<agent-memory-citation") {
		if cut := strings.Index(strings.ToLower(result.CleanText), "<agent-memory-citation"); cut >= 0 {
			result.CleanText = strings.TrimSpace(result.CleanText[:cut])
		}
		result.ParseErr = "unclosed citation block"
	}
	return result
}

// RecordAnswerExposure applies the citation contract without blocking the
// answer path. Ordinary cited/read ids update memory_usage and ordinary
// use_count; Core ids are routed to memory_core_usage and never enter
// memory_usage. All other allowed injected ids are recorded as injected_only
// exposure. When parsing fails, every injected id is recorded as
// injected_only.
func (s *Store) RecordAnswerExposure(ctx context.Context, input AnswerExposureInput) (CitationResult, error) {
	if s == nil {
		return CitationResult{CleanText: input.Answer}, fmt.Errorf("memorystore: store is nil")
	}
	allowed := append([]string(nil), input.InjectedIDs...)
	allowed = append(allowed, input.CoreInjectedIDs...)
	allowed = append(allowed, input.ReadIDs...)
	result := ParseMemoryCitations(input.Answer, allowed)
	if strings.TrimSpace(input.SessionID) == "" {
		return result, fmt.Errorf("memorystore: exposure session id is required")
	}
	coreIDs := make(map[string]struct{}, len(input.CoreInjectedIDs))
	for _, id := range input.CoreInjectedIDs {
		if id = strings.TrimSpace(id); id != "" {
			coreIDs[id] = struct{}{}
		}
	}
	cited := make(map[string]struct{}, len(result.Cited))
	for _, id := range result.Cited {
		cited[id] = struct{}{}
	}
	read := make(map[string]struct{}, len(input.ReadIDs))
	for _, id := range input.ReadIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			read[id] = struct{}{}
		}
	}
	exposed := make([]string, 0, len(input.InjectedIDs)+len(input.CoreInjectedIDs))
	for _, id := range append(append([]string(nil), input.InjectedIDs...), input.CoreInjectedIDs...) {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := cited[id]; ok {
			continue
		}
		if _, ok := read[id]; ok {
			continue
		}
		exposed = appendUniqueCitationID(exposed, id)
	}
	memoryExposed, coreExposed := partitionCoreExposureIDs(exposed, coreIDs)
	if len(memoryExposed) > 0 {
		if _, err := s.RecordExposure(ctx, Exposure{
			MemoryIDs: memoryExposed, SessionID: input.SessionID, TurnID: input.TurnID,
			MessageID: input.MessageID, UseKind: UsageInjectedOnly,
		}); err != nil {
			return result, err
		}
	}
	if len(coreExposed) > 0 {
		if _, err := s.RecordCoreExposure(ctx, CoreExposure{
			CoreIDs: coreExposed, SessionID: input.SessionID, TurnID: input.TurnID,
			MessageID: input.MessageID, UseKind: UsageInjectedOnly,
		}); err != nil {
			return result, err
		}
	}
	if result.ParseErr == "" && len(result.Cited) > 0 {
		memoryCited, coreCited := partitionCoreExposureIDs(result.Cited, coreIDs)
		if len(memoryCited) > 0 {
			if _, err := s.RecordExposure(ctx, Exposure{
				MemoryIDs: memoryCited, SessionID: input.SessionID, TurnID: input.TurnID,
				MessageID: input.MessageID, UseKind: UsageCited,
			}); err != nil {
				return result, err
			}
		}
		if len(coreCited) > 0 {
			if _, err := s.RecordCoreExposure(ctx, CoreExposure{
				CoreIDs: coreCited, SessionID: input.SessionID, TurnID: input.TurnID,
				MessageID: input.MessageID, UseKind: UsageCited,
			}); err != nil {
				return result, err
			}
		}
	}
	if len(input.ReadIDs) > 0 && !input.ReadUsageRecorded {
		memoryRead, coreRead := partitionCoreExposureIDs(input.ReadIDs, coreIDs)
		if len(memoryRead) > 0 {
			if _, err := s.RecordExposure(ctx, Exposure{
				MemoryIDs: memoryRead, SessionID: input.SessionID, TurnID: input.TurnID,
				MessageID: input.MessageID, UseKind: UsageRead,
			}); err != nil {
				return result, err
			}
		}
		if len(coreRead) > 0 {
			if _, err := s.RecordCoreExposure(ctx, CoreExposure{
				CoreIDs: coreRead, SessionID: input.SessionID, TurnID: input.TurnID,
				MessageID: input.MessageID, UseKind: UsageRead,
			}); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func partitionCoreExposureIDs(ids []string, coreIDs map[string]struct{}) ([]string, []string) {
	memoryIDs := make([]string, 0, len(ids))
	identifiedCoreIDs := make([]string, 0, len(ids))
	seenMemory := make(map[string]struct{}, len(ids))
	seenCore := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, isCore := coreIDs[id]; isCore {
			if _, exists := seenCore[id]; !exists {
				seenCore[id] = struct{}{}
				identifiedCoreIDs = append(identifiedCoreIDs, id)
			}
			continue
		}
		if _, exists := seenMemory[id]; !exists {
			seenMemory[id] = struct{}{}
			memoryIDs = append(memoryIDs, id)
		}
	}
	return memoryIDs, identifiedCoreIDs
}

func appendUniqueCitationID(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}
