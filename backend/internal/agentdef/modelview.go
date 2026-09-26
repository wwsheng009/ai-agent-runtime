package agentdef

import (
	"fmt"
	"strings"
)

// ModelVisibleAgent is the compact, model-facing projection of a definition.
// It intentionally carries only what the parent model needs to pick a role:
// name, when-to-delegate description, origin, and the hard boundary summary.
type ModelVisibleAgent struct {
	Name           string
	Description    string
	Source         Source
	ReadOnly       bool
	PermissionMode string
}

// ModelVisibleAgents projects a catalog into a stable, name-sorted list of
// model-facing entries. Nil catalogs yield nil.
func ModelVisibleAgents(catalog *Catalog) []ModelVisibleAgent {
	if catalog == nil {
		return nil
	}
	defs := catalog.List()
	if len(defs) == 0 {
		return nil
	}
	entries := make([]ModelVisibleAgent, 0, len(defs))
	for _, def := range defs {
		if def == nil {
			continue
		}
		entry := ModelVisibleAgent{
			Name:        def.Name,
			Description: def.Description,
			Source:      def.Source,
		}
		if binding, err := BuildBinding(def); err == nil && binding != nil {
			entry.PermissionMode = string(binding.PermissionMode)
			if binding.ReadOnly != nil {
				entry.ReadOnly = *binding.ReadOnly
			}
		} else {
			entry.PermissionMode = def.PermissionMode
			entry.ReadOnly = sandboxImpliesReadOnly(def.Sandbox)
		}
		entries = append(entries, entry)
	}
	return entries
}

func sandboxImpliesReadOnly(sandbox string) bool {
	switch strings.ToLower(strings.TrimSpace(sandbox)) {
	case "read-only", "readonly":
		return true
	default:
		return false
	}
}

// FormatModelVisibleAgents renders entries as a compact "- name: description
// (source[, read-only])" block bounded by maxEntries and maxChars. It is used
// in the spawn tool description so the parent model can see which portable
// roles exist without a separate discovery call.
func FormatModelVisibleAgents(entries []ModelVisibleAgent, maxEntries, maxChars int) string {
	if len(entries) == 0 {
		return ""
	}
	if maxEntries <= 0 {
		maxEntries = 8
	}
	if maxChars <= 0 {
		maxChars = 800
	}
	lines := make([]string, 0, minInt(len(entries), maxEntries)+1)
	used := 0
	omitted := 0
	for i, entry := range entries {
		if i >= maxEntries {
			omitted = len(entries) - i
			break
		}
		line := "- " + strings.TrimSpace(entry.Name)
		if desc := strings.TrimSpace(entry.Description); desc != "" {
			line += ": " + truncateRunes(desc, 90)
		}
		meta := []string{string(entry.Source)}
		if entry.ReadOnly {
			meta = append(meta, "read-only")
		} else if mode := strings.TrimSpace(entry.PermissionMode); mode != "" && mode != "default" {
			meta = append(meta, "permission="+mode)
		}
		line += " (" + strings.Join(meta, ", ") + ")"
		if used+len(line)+1 > maxChars {
			omitted = len(entries) - i
			break
		}
		lines = append(lines, line)
		used += len(line) + 1
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("... and %d more (use `aicli agents list`)", omitted))
	}
	return strings.Join(lines, "\n")
}

// ModelVisibleSummary discovers definitions (best effort) and renders the
// model-facing block. Discovery errors degrade to an empty string so tool
// availability never depends on filesystem state.
func ModelVisibleSummary(opts DiscoverOptions, maxEntries, maxChars int) string {
	catalog, err := Discover(opts)
	if err != nil || catalog == nil {
		return ""
	}
	return FormatModelVisibleAgents(ModelVisibleAgents(catalog), maxEntries, maxChars)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
