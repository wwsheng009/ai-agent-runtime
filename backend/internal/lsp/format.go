package lsp

import (
	"fmt"
	"sort"
	"strings"
)

// RenderOptions is the rendering contract of the inline block. Every threshold
// comes from DiagnosticsConfig; nothing here is hard-coded (docs/lsp I6).
type RenderOptions struct {
	File     string
	Servers  []string
	MaxItems int
	MaxChars int
	Scope    DiagnosticScope
}

// RenderResult is the append-only text plus the truncation facts so callers can
// log/verify policy (A7: truncation must be visible and explainable).
type RenderResult struct {
	Text           string
	Shown          int
	OmittedItems   int
	OmittedByChars int
}

// SortDiagnostics applies the fixed, documentable order: severity first
// (error → hint), then position, then server. Identical items from different
// servers collapse to one (A9 dedupe by Diagnostic.Key).
func SortDiagnostics(items []Diagnostic) []Diagnostic {
	deduped := make(map[string]Diagnostic, len(items))
	for _, item := range items {
		key := item.Key()
		if existing, exists := deduped[key]; !exists || existing.Server == "" {
			deduped[key] = item
		}
	}
	sorted := make([]Diagnostic, 0, len(deduped))
	for _, item := range deduped {
		sorted = append(sorted, item)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		if left.Severity != right.Severity {
			return left.Severity < right.Severity
		}
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line < right.Range.Start.Line
		}
		if left.Range.Start.Column != right.Range.Start.Column {
			return left.Range.Start.Column < right.Range.Start.Column
		}
		if left.Server != right.Server {
			return left.Server < right.Server
		}
		return left.Message < right.Message
	})
	return sorted
}

// RenderDiagnostics formats the inline block. The block is designed to be
// machine-parseable, stable and append-only (I1): callers concatenate it after
// the untouched tool output.
func RenderDiagnostics(items []Diagnostic, opts RenderOptions) RenderResult {
	items = SortDiagnostics(items)
	maxItems := opts.MaxItems
	if maxItems <= 0 {
		maxItems = DefaultMaxItems
	}
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}

	shown := items
	omittedItems := 0
	if len(shown) > maxItems {
		omittedItems = len(shown) - maxItems
		shown = shown[:maxItems]
	}

	scope := opts.Scope
	if scope == "" {
		scope = ScopeAll
	}
	servers := strings.Join(opts.Servers, ",")

	header := fmt.Sprintf(
		`<lsp_diagnostics file="%s" count="%d" shown="%d" scope="%s" servers="%s"`,
		escapeAttr(opts.File), len(items), len(shown), scope, escapeAttr(servers),
	)
	if omittedItems > 0 {
		header += fmt.Sprintf(` truncated="%d"`, omittedItems)
	}
	header += ">\n"

	var builder strings.Builder
	builder.WriteString(header)
	budget := maxChars - len(header)
	written := 0
	for _, item := range shown {
		line := formatDiagnosticLine(item)
		if budget-len(line) < 0 {
			break
		}
		builder.WriteString(line)
		budget -= len(line)
		written++
	}
	omittedByChars := len(shown) - written
	if omittedByChars > 0 {
		note := fmt.Sprintf("[lsp] %d more diagnostics omitted (max_chars=%d)\n", omittedByChars, maxChars)
		if budget-len(note) >= 0 || written == 0 {
			builder.WriteString(note)
		}
	}
	if omittedItems > 0 {
		note := fmt.Sprintf("[lsp] %d more diagnostics omitted (max_items=%d)\n", omittedItems, maxItems)
		if budget-len(note) >= 0 || written == 0 {
			builder.WriteString(note)
		}
	}
	builder.WriteString("</lsp_diagnostics>\n")

	return RenderResult{
		Text:           builder.String(),
		Shown:          written,
		OmittedItems:   omittedItems,
		OmittedByChars: omittedByChars,
	}
}

func formatDiagnosticLine(item Diagnostic) string {
	line := fmt.Sprintf(
		"%s %d:%d [%s] %s\n",
		item.Severity.String(),
		item.Range.Start.Line+1,
		item.Range.Start.Column+1,
		fallbackServer(item),
		sanitizeMessage(item.Message),
	)
	return line
}

func fallbackServer(item Diagnostic) string {
	if strings.TrimSpace(item.Server) != "" {
		return item.Server
	}
	if strings.TrimSpace(item.Source) != "" {
		return item.Source
	}
	return "lsp"
}

// sanitizeMessage keeps diagnostics one line each so the block stays parseable.
func sanitizeMessage(message string) string {
	message = strings.ReplaceAll(message, "\r\n", " ")
	message = strings.ReplaceAll(message, "\n", " ")
	message = strings.ReplaceAll(message, "\r", " ")
	message = strings.TrimSpace(message)
	if message == "" {
		message = "(no message)"
	}
	return message
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}

// DegradeNote renders the configured degradation signal. `mode` comes from
// config (A5/A8) and never fails the edit itself.
func DegradeNote(path string, reason string, mode DegradeMode) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unavailable"
	}
	switch mode {
	case DegradeNone:
		return ""
	case DegradeError:
		return fmt.Sprintf("<lsp_error file=\"%s\"> LSP diagnostics unavailable: %s</lsp_error>\n", escapeAttr(path), reason)
	default:
		return fmt.Sprintf("<lsp_note file=\"%s\"> LSP diagnostics unavailable: %s</lsp_note>\n", escapeAttr(path), reason)
	}
}
