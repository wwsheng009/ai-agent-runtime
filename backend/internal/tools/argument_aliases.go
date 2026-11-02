package tools

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
)

// normalizeToolkitToolArgs accepts common, unambiguous model aliases for the
// built-in tools. External MCP calls keep their provider-defined arguments.
//
// The alias table itself lives in internal/toolargs so the runtime policy can
// inspect exactly the same argument names this executor reads (see
// toolargs.ToolkitArgAliases); promotion order within a pair is preserved.
func normalizeToolkitToolArgs(toolName string, args map[string]interface{}) map[string]interface{} {
	return toolargs.NormalizeToolkitArgs(toolName, args)
}

// toolkitArgRepairNotes reports which input repairs the executor applied so the
// model can see what changed instead of silently relying on a rewritten call.
// Notes carry rule + key only, never values.
func toolkitArgRepairNotes(toolName string, raw, normalized map[string]interface{}) []string {
	if len(raw) == 0 {
		return nil
	}
	notes := make([]string, 0, 2)
	if _, hadRaw := raw["_raw"]; hadRaw {
		if _, stillRaw := normalized["_raw"]; !stillRaw {
			notes = append(notes, "unwrap:_raw")
		}
	}
	aliases, ok := toolargs.ToolkitArgAliasesFor(toolName)
	if !ok {
		return notes
	}
	for _, pair := range aliases.Args {
		canonical := pair.Canonical
		if _, canonicalInRaw := raw[canonical]; canonicalInRaw {
			continue
		}
		if _, canonicalInNormalized := normalized[canonical]; !canonicalInNormalized {
			continue
		}
		for _, alias := range pair.Aliases {
			if _, ok := raw[alias]; ok {
				notes = append(notes, "alias:"+alias+"→"+canonical)
				break
			}
		}
	}
	return notes
}
