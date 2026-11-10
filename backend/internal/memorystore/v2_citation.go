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
	ReadIDs     []string
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
// answer path. Cited/read ids increment use_count; all other allowed injected
// ids are recorded as injected_only exposure. When parsing fails, every
// injected id is recorded as injected_only.
func (s *Store) RecordAnswerExposure(ctx context.Context, input AnswerExposureInput) (CitationResult, error) {
	if s == nil {
		return CitationResult{CleanText: input.Answer}, fmt.Errorf("memorystore: store is nil")
	}
	allowed := append([]string(nil), input.InjectedIDs...)
	allowed = append(allowed, input.ReadIDs...)
	result := ParseMemoryCitations(input.Answer, allowed)
	if strings.TrimSpace(input.SessionID) == "" {
		return result, fmt.Errorf("memorystore: exposure session id is required")
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
	exposed := make([]string, 0, len(input.InjectedIDs))
	for _, id := range input.InjectedIDs {
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
	if len(exposed) > 0 {
		if _, err := s.RecordExposure(ctx, Exposure{
			MemoryIDs: exposed, SessionID: input.SessionID, TurnID: input.TurnID,
			MessageID: input.MessageID, UseKind: UsageInjectedOnly,
		}); err != nil {
			return result, err
		}
	}
	if result.ParseErr == "" && len(result.Cited) > 0 {
		if _, err := s.RecordExposure(ctx, Exposure{
			MemoryIDs: result.Cited, SessionID: input.SessionID, TurnID: input.TurnID,
			MessageID: input.MessageID, UseKind: UsageCited,
		}); err != nil {
			return result, err
		}
	}
	if len(input.ReadIDs) > 0 {
		if _, err := s.RecordExposure(ctx, Exposure{
			MemoryIDs: input.ReadIDs, SessionID: input.SessionID, TurnID: input.TurnID,
			MessageID: input.MessageID, UseKind: UsageRead,
		}); err != nil {
			return result, err
		}
	}
	return result, nil
}

func appendUniqueCitationID(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}
