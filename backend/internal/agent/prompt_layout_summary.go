package agent

import (
	"fmt"
	"path/filepath"
	"strings"
)

const promptLayoutSummaryMaxSources = 4

func summarizePromptLayoutForEvent(layout string) (string, int) {
	layout = strings.TrimSpace(strings.ReplaceAll(layout, "\r\n", "\n"))
	if layout == "" {
		return "", 0
	}

	blocks := strings.Split(layout, "\n\n")
	layers := make([]string, 0, len(blocks))
	sources := make([]string, 0, len(blocks))
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 {
			continue
		}
		header := strings.TrimSpace(lines[0])
		if strings.HasPrefix(header, "[") && strings.HasSuffix(header, "]") {
			layers = appendUniquePromptLayoutPart(layers, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(header, "["), "]")))
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
			sources = appendUniquePromptLayoutPart(sources, filepath.Base(source))
		}
	}

	parts := make([]string, 0, 2)
	if len(layers) > 0 {
		parts = append(parts, "layers="+strings.Join(layers, " -> "))
	}
	if len(sources) > 0 {
		preview := sources
		extraCount := 0
		if len(preview) > promptLayoutSummaryMaxSources {
			extraCount = len(preview) - promptLayoutSummaryMaxSources
			preview = preview[:promptLayoutSummaryMaxSources]
		}
		sourceText := strings.Join(preview, ", ")
		if extraCount > 0 {
			sourceText += fmt.Sprintf(", +%d more", extraCount)
		}
		parts = append(parts, "sources="+sourceText)
	}
	if len(parts) == 0 {
		compact := strings.Join(strings.Fields(layout), " ")
		if compact == "" {
			return "", len(layout)
		}
		return compact, len(layout)
	}
	return strings.Join(parts, " | "), len(layout)
}

func appendUniquePromptLayoutPart(values []string, value string) []string {
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
