package prompt

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const renderedLayoutSummaryMaxSources = 4

type RenderedLayoutSummary struct {
	Summary string   `json:"summary,omitempty"`
	Length  int      `json:"length,omitempty"`
	Layers  []string `json:"layers,omitempty"`
	Sources []string `json:"sources,omitempty"`
}

// InstructionMessagesSummary carries a summary of the instruction (system/developer)
// prefix and the total size of all messages that will be sent to the model.
type InstructionMessagesSummary struct {
	Summary           string   `json:"summary,omitempty"`
	InstructionChars  int      `json:"instruction_chars,omitempty"`
	TotalChars        int      `json:"total_chars,omitempty"`
	InstructionTokens int      `json:"instruction_tokens,omitempty"`
	TotalTokens       int      `json:"total_tokens,omitempty"`
	Layers            []string `json:"layers,omitempty"`
	Sources           []string `json:"sources,omitempty"`
}

// SummarizeInstructionMessages extracts layer/source information directly from
// structured messages, avoiding the fragile render→parse round-trip that
// SummarizeRenderedLayout requires.  It also records both the instruction
// prefix length and the total message content length so that callers can
// distinguish "system prompt size" from "full prompt size".
func SummarizeInstructionMessages(messages []types.Message) InstructionMessagesSummary {
	if len(messages) == 0 {
		return InstructionMessagesSummary{}
	}

	layers := make([]string, 0, 4)
	sources := make([]string, 0, 4)
	instructionChars := 0
	totalChars := 0

	for _, msg := range messages {
		contentLen := len(strings.TrimSpace(msg.Content))
		totalChars += contentLen

		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != RoleSystem && role != RoleDeveloper {
			continue
		}
		instructionChars += contentLen

		layer := "unknown"
		if msg.Metadata != nil {
			if v, ok := msg.Metadata["prompt_layer"].(string); ok && strings.TrimSpace(v) != "" {
				layer = strings.TrimSpace(v)
			}
		}
		layerLabel := layer + "/" + role
		layers = appendUniqueRenderedLayoutValue(layers, layerLabel)

		if msg.Metadata != nil {
			if v, ok := msg.Metadata["prompt_source"].(string); ok && strings.TrimSpace(v) != "" {
				sources = appendUniqueRenderedLayoutValue(sources, filepath.Base(strings.TrimSpace(v)))
			}
			if v, ok := msg.Metadata["prompt_sources"].([]string); ok {
				for _, s := range v {
					if s = strings.TrimSpace(s); s != "" {
						sources = appendUniqueRenderedLayoutValue(sources, filepath.Base(s))
					}
				}
			}
		}
	}

	summary := buildRenderedLayoutSummaryTextFromParts(layers, sources)
	return InstructionMessagesSummary{
		Summary:          summary,
		InstructionChars: instructionChars,
		TotalChars:       totalChars,
		Layers:           layers,
		Sources:          sources,
	}
}

// SummarizeInstructionMessagesWithTokens is like SummarizeInstructionMessages
// but also estimates token counts using the provided tokenCountFunc.
// tokenCountFunc should return the estimated token count for a given text string.
// If tokenCountFunc is nil, the token fields are left as zero.
func SummarizeInstructionMessagesWithTokens(messages []types.Message, tokenCountFunc func(string) int) InstructionMessagesSummary {
	result := SummarizeInstructionMessages(messages)
	if tokenCountFunc == nil {
		return result
	}

	instructionTokens := 0
	totalTokens := 0
	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		tokens := tokenCountFunc(content)
		totalTokens += tokens
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == RoleSystem || role == RoleDeveloper {
			instructionTokens += tokens
		}
	}
	result.InstructionTokens = instructionTokens
	result.TotalTokens = totalTokens
	return result
}

// SummarizeRenderedLayout remains for backwards-compatible layout rendering.
// Prefer SummarizeInstructionMessages when the original messages are available.
func SummarizeRenderedLayout(layout string) RenderedLayoutSummary {
	layout = strings.TrimSpace(strings.ReplaceAll(layout, "\r\n", "\n"))
	if layout == "" {
		return RenderedLayoutSummary{}
	}

	result := RenderedLayoutSummary{
		Length:  len(layout),
		Layers:  make([]string, 0, 4),
		Sources: make([]string, 0, 4),
	}

	blocks := strings.Split(layout, "\n\n")
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 {
			continue
		}
		header := strings.TrimSpace(lines[0])
		if strings.HasPrefix(header, "[") && strings.HasSuffix(header, "]") {
			result.Layers = appendUniqueRenderedLayoutValue(result.Layers, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(header, "["), "]")))
		}
		for _, line := range lines[1:] {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "Source:") {
				continue
			}
			source := strings.TrimSpace(strings.TrimPrefix(line, "Source:"))
			if source == "" {
				continue
			}
			result.Sources = appendUniqueRenderedLayoutValue(result.Sources, filepath.Base(source))
		}
	}

	result.Summary = buildRenderedLayoutSummaryTextFromParts(result.Layers, result.Sources)
	return result
}

func buildRenderedLayoutSummaryTextFromParts(layers, sources []string) string {
	parts := make([]string, 0, 2)
	if len(layers) > 0 {
		parts = append(parts, "layers="+strings.Join(layers, " -> "))
	}
	if len(sources) > 0 {
		preview := append([]string(nil), sources...)
		extraCount := 0
		if len(preview) > renderedLayoutSummaryMaxSources {
			extraCount = len(preview) - renderedLayoutSummaryMaxSources
			preview = preview[:renderedLayoutSummaryMaxSources]
		}
		sourceText := strings.Join(preview, ", ")
		if extraCount > 0 {
			sourceText += fmt.Sprintf(", +%d more", extraCount)
		}
		parts = append(parts, "sources="+sourceText)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " | ")
	}
	return ""
}

func appendUniqueRenderedLayoutValue(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
