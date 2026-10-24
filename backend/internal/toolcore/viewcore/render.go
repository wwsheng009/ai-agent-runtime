package viewcore

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// contentRenderer returns the surface's non-text renderer, if it has one. A
// runtime without one reads everything as text, which is what a host without a
// document converter has always done.
func contentRenderer(call *toolcore.Call) toolcore.ContentRenderer {
	if call == nil || call.Runtime == nil {
		return nil
	}
	renderer, _ := call.Runtime.(toolcore.ContentRenderer)
	return renderer
}

// renderedRead closes the bookkeeping for a result the surface rendered itself
// (a document or notebook converted to Markdown).
//
// A rendered window publishes the same metadata the text path does, so it is
// remembered under the same contract: an unchanged window may answer with a
// stub, and only a window that was really delivered is written to the host's
// read ledger. A result without window metadata - an image attachment, a
// degradation note - is not a window and is not recorded at all.
func (h *handler) renderedRead(call *toolcore.Call, abs string, result *toolcore.Result) (*toolcore.Result, *DeliveredRead) {
	observer := readObserver(call)
	if observer == nil || result == nil || !result.Success {
		return result, nil
	}
	window, isWindow := windowFromMetadata(result.Metadata)
	if !isWindow {
		return result, nil
	}
	if stub, consume, hit := observer.RememberedStub(h.def.Name, abs, window.Offset, window.Limit); hit {
		delivered := &DeliveredRead{}
		delivered.Defer(consume)
		return stub, delivered
	}
	delivered := &DeliveredRead{}
	delivered.Defer(func() {
		observer.ReadDelivered(h.def.Name, abs, window)
	})
	// Derived renders have always been stricter than the text path here: a
	// rendered window that stopped before EOF is never cached, while a text
	// window stopped at its limit is (a repeat of the same offset+limit still
	// returns the same bytes). Both rules are the local truth; this keeps them
	// instead of silently unifying two behaviours that were never the same.
	if !window.Tail && window.LinesRead > 0 && !window.More && !window.Empty && window.LongLinesTruncated == 0 {
		delivered.Defer(func() {
			observer.ReadRemembered(h.def.Name, abs, window)
		})
	}
	return result, delivered
}

// windowFromMetadata rebuilds the delivered-window description of a rendered
// result from the metadata it published. ok=false means the result is not a
// window at all (an image attachment declares no lines_read).
func windowFromMetadata(metadata map[string]any) (toolcore.ReadWindow, bool) {
	linesRead, isWindow := metadataIntValue(metadata["lines_read"])
	if !isWindow {
		return toolcore.ReadWindow{}, false
	}
	offset := metadataInt(metadata, "offset")
	limit := metadataInt(metadata, "limit")
	totalLines := metadataInt(metadata, "total_lines")
	longLines := metadataInt(metadata, "long_lines_truncated")
	eof, _ := metadata["eof"].(bool)
	truncated, _ := metadata["is_truncated"].(bool)
	tail, _ := metadata["tail"].(bool)
	empty, _ := metadata["empty"].(bool)
	byteBudget, _ := metadata["byte_budget_applied"].(bool)
	return toolcore.ReadWindow{
		Offset:             offset,
		Limit:              limit,
		LinesRead:          linesRead,
		TotalLines:         totalLines,
		EOF:                eof,
		More:               truncated,
		ByteBudgetApplied:  byteBudget,
		LongLinesTruncated: longLines,
		Tail:               tail,
		Empty:              empty,
		FullRead:           !tail && offset == 0 && !truncated && linesRead > 0 && !empty && longLines == 0,
	}, true
}

func metadataIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func metadataInt(metadata map[string]any, key string) int {
	value, _ := metadataIntValue(metadata[key])
	return value
}
