// Command tool-schema-measure prints the baseline size of the model-facing tool
// surface: builtin toolkit tools plus broker (orchestration) definitions,
// grouped by source and taxonomy kind.
//
// Metric mirrors the reference project (dsh-ops scripts/measure-schemas.mjs):
// compact JSON {name,description,parameters} bytes; tokens ≈ ceil(bytes/4).
// MCP tools are excluded because this command runs without an MCP runtime.
//
// Usage: go run ./cmd/tool-schema-measure
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type row struct {
	name         string
	source       string
	kind         string
	bytes        int
	descripBytes int
	paramsBytes  int
}

func definitionBytes(def types.ToolDefinition) int {
	raw, err := json.Marshal(map[string]interface{}{
		"name":        def.Name,
		"description": def.Description,
		"parameters":  def.Parameters,
	})
	if err != nil {
		return 0
	}
	return len(raw)
}

func descriptionBytes(def types.ToolDefinition) int {
	raw, err := json.Marshal(def.Description)
	if err != nil {
		return 0
	}
	return len(raw)
}

func parametersBytes(def types.ToolDefinition) int {
	raw, err := json.Marshal(def.Parameters)
	if err != nil {
		return 0
	}
	return len(raw)
}

func kindOf(name string) string {
	if tax, ok := policy.LookupToolTaxonomy(name); ok {
		if kind := strings.TrimSpace(tax.Kind); kind != "" {
			return kind
		}
	}
	return "unclassified"
}

func tokens(bytes int) int { return (bytes + 3) / 4 }

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedPairs(m map[string][2]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// aliasCollapse mimics the shipped agent-loop optimization (optimizeModelToolSurface):
// keep the highest-ranked definition per alias family (shell > bash >
// execute_shell_command、grep > rg、view > read) so the scenario numbers reflect what
// a real request ships.
//
// 家族与排名全部由 toolregistry 的别名表推导（Canonical + SurfaceRank），不再手写
// 拼写表。旧实现按 rows 顺序"先到先留"，而 rows 是**按 schema 字节数降序**排的，
// 于是同族留下的其实是"schema 最大的那个拼写"——与文档承诺的偏好顺序无关；
// 改用排名后，本命令的压缩后体积与线上 optimizeModelToolSurface 的选择一致。
func aliasCollapse(rows []row) []row {
	best := make(map[string]int, len(rows))
	for _, r := range rows {
		family := toolregistry.Canonical(r.name)
		if rank := toolregistry.SurfaceRank(family, r.name); rank > best[family] {
			best[family] = rank
		}
	}

	emitted := make(map[string]bool, len(best))
	out := make([]row, 0, len(rows))
	for _, r := range rows {
		family := toolregistry.Canonical(r.name)
		rank := toolregistry.SurfaceRank(family, r.name)
		if rank == 0 {
			out = append(out, r) // 不属于任何别名族（或未登记）：原样保留
			continue
		}
		if emitted[family] || rank != best[family] {
			continue // 同族只留排名最高的一份，其余是同工具的另一份 schema
		}
		emitted[family] = true
		out = append(out, r)
	}
	return out
}

func main() {
	cfg := runtimecfg.DefaultRuntimeConfig()

	seen := map[string]bool{}
	var rows []row
	add := func(def types.ToolDefinition, source string) {
		name := strings.TrimSpace(def.Name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		rows = append(rows, row{
			name:         name,
			source:       source,
			kind:         kindOf(name),
			bytes:        definitionBytes(def),
			descripBytes: descriptionBytes(def),
			paramsBytes:  parametersBytes(def),
		})
	}

	manager := runtimetools.NewDefaultManagerWithRuntimeConfig(nil, cfg)
	for _, def := range manager.ListTools() {
		add(types.ToolDefinition{Name: def.Name, Description: def.Description, Parameters: def.Parameters}, "builtin")
	}
	broker := &toolbroker.Broker{}
	for _, def := range broker.Definitions() {
		add(def, "broker")
	}
	for _, def := range broker.DefinitionsForContext(context.Background()) {
		add(def, "broker")
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].bytes != rows[j].bytes {
			return rows[i].bytes > rows[j].bytes
		}
		return rows[i].name < rows[j].name
	})

	total := 0
	for _, r := range rows {
		total += r.bytes
	}

	fmt.Printf("surface: %d tools, %d bytes, ~%d tokens (bytes/4)\n", len(rows), total, tokens(total))
	fmt.Println("note: MCP tools excluded (no MCP runtime here); metric = compact JSON {name,description,parameters}")

	bySource := map[string][2]int{}
	byKind := map[string][2]int{}
	for _, r := range rows {
		s := bySource[r.source]
		bySource[r.source] = [2]int{s[0] + 1, s[1] + r.bytes}
		k := byKind[r.kind]
		byKind[r.kind] = [2]int{k[0] + 1, k[1] + r.bytes}
	}

	fmt.Println("\n## by source")
	fmt.Println("| source | tools | bytes | ~tokens |")
	fmt.Println("| --- | ---: | ---: | ---: |")
	for _, key := range sortedPairs(bySource) {
		v := bySource[key]
		fmt.Printf("| %s | %d | %d | %d |\n", key, v[0], v[1], tokens(v[1]))
	}

	fmt.Println("\n## by taxonomy kind")
	fmt.Println("| kind | tools | bytes | ~tokens |")
	fmt.Println("| --- | ---: | ---: | ---: |")
	for _, key := range sortedPairs(byKind) {
		v := byKind[key]
		fmt.Printf("| %s | %d | %d | %d |\n", key, v[0], v[1], tokens(v[1]))
	}

	fmt.Println("\n## top 15 by bytes")
	fmt.Println("| tool | source | kind | bytes |")
	fmt.Println("| --- | --- | --- | ---: |")
	limit := 15
	if len(rows) < limit {
		limit = len(rows)
	}
	for _, r := range rows[:limit] {
		fmt.Printf("| %s | %s | %s | %d |\n", r.name, r.source, r.kind, r.bytes)
	}

	fmt.Println("\n## all tools by bytes (description vs parameters split)")
	fmt.Println("| tool | bytes | description B | parameters B |")
	fmt.Println("| --- | ---: | ---: | ---: |")
	for _, r := range rows {
		fmt.Printf("| %s | %d | %d | %d |\n", r.name, r.bytes, r.descripBytes, r.paramsBytes)
	}

	type scenario struct {
		label string
		drop  func(row) bool
	}
	scenarios := []scenario{
		{"baseline (all)", func(row) bool { return false }},
		{"alias-collapse only", func(r row) bool {
			for _, kept := range aliasCollapse(rows) {
				if kept.name == r.name {
					return false
				}
			}
			return true
		}},
		{"orchestration off (broker)", func(r row) bool { return r.source == "broker" }},
		{"network off (kind)", func(r row) bool { return r.kind == "network" }},
		{"control off (kind)", func(r row) bool { return r.kind == "control" }},
		{"orchestration+network+control off", func(r row) bool {
			return r.source == "broker" || r.kind == "network" || r.kind == "control"
		}},
	}
	fmt.Println("\n## scenarios (remaining prefix)")
	fmt.Println("| scenario | tools | bytes | ~tokens | saved bytes | saved % |")
	fmt.Println("| --- | ---: | ---: | ---: | ---: | ---: |")
	for _, sc := range scenarios {
		count, sum := 0, 0
		for _, r := range rows {
			if sc.drop(r) {
				continue
			}
			count++
			sum += r.bytes
		}
		saved := total - sum
		pct := 0.0
		if total > 0 {
			pct = float64(saved) * 100 / float64(total)
		}
		fmt.Printf("| %s | %d | %d | %d | %d | %.1f%% |\n", sc.label, count, sum, tokens(sum), saved, pct)
	}

	census := map[string]int{}
	names := policy.KnownToolTaxonomyNames()
	for _, name := range names {
		census[kindOf(name)]++
	}
	fmt.Printf("\n## taxonomy census (KnownToolTaxonomyNames: %d names)\n", len(names))
	fmt.Println("| kind | names |")
	fmt.Println("| --- | ---: |")
	for _, key := range sortedKeys(census) {
		fmt.Printf("| %s | %d |\n", key, census[key])
	}

	// Production-surface estimate: alias collapse plus the shipped per-tool
	// compactions (compactGrepParametersForModel + the aicli_exec/shell/view/
	// todos whitelists). Sizes come from TestOptimizeModelToolSurfaceCompactToolWireBytes
	// ceilings minus a small headroom factor; treat as ±10%.
	compactedWire := map[string]int{
		"aicli_exec": 2500, "shell": 2700, "view": 1600, "todos": 1550, "grep": 3300,
	}
	keptNames := map[string]bool{}
	for _, kept := range aliasCollapse(rows) {
		keptNames[kept.name] = true
	}
	estimate := 0
	for _, r := range rows {
		if !keptNames[r.name] {
			continue
		}
		if wire, ok := compactedWire[r.name]; ok {
			estimate += wire
			continue
		}
		estimate += r.bytes
	}
	aliasCollapsed := 0
	for _, r := range rows {
		if keptNames[r.name] {
			aliasCollapsed += r.bytes
		}
	}
	fmt.Printf("\n## production-surface estimate (alias collapse + shipped compactions)\n")
	fmt.Printf("alias-collapsed raw: %d B | with compactions: ~%d B | extra saved: ~%d B (~%.1f%% of raw face)\n",
		aliasCollapsed, estimate, aliasCollapsed-estimate,
		float64(aliasCollapsed-estimate)*100/float64(aliasCollapsed))
	fmt.Printf("vs untouched face (%d B): total ~%.1f%% reduction\n", total, float64(total-estimate)*100/float64(total))
}
