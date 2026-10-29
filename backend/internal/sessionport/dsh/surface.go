package dsh

import (
	"encoding/json"
	"fmt"
	"strings"
)

func isSurfaceType(value string) bool {
	switch value {
	case "system/message", "developer/message", "user/message", "assistant/message", "tool/result":
		return true
	default:
		return false
	}
}

type surfaceOperation struct {
	append  bool
	replace bool
	start   int
	end     int
}

func parseSurfaceOperation(event Event) (surfaceOperation, bool, error) {
	if !isSurfaceType(event.Type) {
		return surfaceOperation{}, false, nil
	}
	if len(event.SurfaceOp) == 0 {
		// Be tolerant on import: malformed historical data is better surfaced as
		// a warning than silently dropping a message. Native DSH writers always
		// emit this field.
		return surfaceOperation{append: true}, true, nil
	}
	var value string
	if err := json.Unmarshal(event.SurfaceOp, &value); err == nil {
		if value == "append" {
			return surfaceOperation{append: true}, true, nil
		}
		return surfaceOperation{}, true, fmt.Errorf("未知 surfaceOp: %q", value)
	}
	var replace surfaceReplace
	if err := json.Unmarshal(event.SurfaceOp, &replace); err != nil {
		return surfaceOperation{}, true, fmt.Errorf("解析 surfaceOp 失败: %w", err)
	}
	if replace.Op != "replace" || replace.StartSeq < 0 || replace.EndSeq < 0 {
		return surfaceOperation{}, true, fmt.Errorf("非法 replace surfaceOp")
	}
	return surfaceOperation{replace: true, start: replace.StartSeq, end: replace.EndSeq}, true, nil
}

// foldSurface follows DSH's current model-visible surface. It intentionally
// only implements the first-party surface semantics needed by import; plugin
// message projections are treated as opaque log-only events.
func foldSurface(events []Event) ([]int, []string) {
	warnings := make([]string, 0)
	nodes := make([]int, 0)
	for index, event := range events {
		if !isSurfaceType(event.Type) {
			continue
		}
		operation, present, err := parseSurfaceOperation(event)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		if !present {
			continue
		}
		if len(event.SurfaceOp) == 0 {
			warnings = append(warnings, fmt.Sprintf("surface 事件 %s seq=%d 缺少 surfaceOp，按 append 处理", event.Type, event.Seq))
		}
		if operation.append {
			nodes = append(nodes, index)
			continue
		}
		startIndex := indexOfSeq(nodes, operation.start)
		endIndex := indexOfSeq(nodes, operation.end)
		if startIndex < 0 || endIndex < 0 || startIndex > endIndex {
			warnings = append(warnings, fmt.Sprintf("surface replace seq=%d 的范围 [%d,%d] 不在当前 surface 中，已忽略", event.Seq, operation.start, operation.end))
			continue
		}
		shadowed := append([]int(nil), nodes[startIndex:endIndex+1]...)
		if len(event.SourceEventSeqs) == 0 {
			warnings = append(warnings, fmt.Sprintf("surface replace seq=%d 未声明 sourceEventSeqs", event.Seq))
		} else {
			for _, seq := range shadowed {
				if !containsInt(event.SourceEventSeqs, seq) {
					warnings = append(warnings, fmt.Sprintf("surface replace seq=%d 未覆盖被替换节点 seq=%d", event.Seq, seq))
				}
			}
		}
		nodes = append(nodes[:startIndex], append([]int{index}, nodes[endIndex+1:]...)...)
	}
	return nodes, warnings
}

// transcriptNodes returns append-origin surface nodes. It is the human-readable
// transcript view: replacement copies (for example compaction checkpoints) are
// intentionally excluded.
func transcriptNodes(events []Event) ([]int, []string) {
	warnings := make([]string, 0)
	nodes := make([]int, 0)
	for index, event := range events {
		if !isSurfaceType(event.Type) {
			continue
		}
		operation, present, err := parseSurfaceOperation(event)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		if present && operation.append {
			nodes = append(nodes, index)
		}
	}
	return nodes, warnings
}

func indexOfSeq(nodes []int, seq int) int {
	for index, value := range nodes {
		if value == seq {
			return index
		}
	}
	return -1
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func eventSourceKind(event Event) string {
	switch event.Type {
	case "user/message":
		var data userMessageData
		if json.Unmarshal(event.Data, &data) == nil {
			return strings.TrimSpace(data.Source.Kind)
		}
	case "assistant/message":
		var data assistantMessageData
		if json.Unmarshal(event.Data, &data) == nil {
			return strings.TrimSpace(data.Message.Source.Kind)
		}
	case "tool/result":
		var data toolResultData
		if json.Unmarshal(event.Data, &data) == nil {
			return strings.TrimSpace(data.Message.Source.Kind)
		}
	}
	return ""
}
