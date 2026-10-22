//go:build ignore

// Command skill_tool_binding_audit measures how many installed skills declare
// tool dependencies, and what happens to those dependencies when the MCP surface
// changes (which is exactly what `--remote` does).
//
// The question it answers: "if skills must become text-only, how much is actually
// tool-bound today, and what breaks if the bound tools disappear?"
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func main() {
	roots := []string{
		filepath.Join("..", ".agents", "skills"),
		filepath.Join(os.Getenv("USERPROFILE"), ".aicli", ".agents", "skills"),
	}

	total, textOnly, toolBound, withWorkflow := 0, 0, 0, 0

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		fmt.Printf("=== %s ===\n", root)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)

		for _, name := range names {
			dir := filepath.Join(root, name)
			item, err := loadSkill(dir)
			if err != nil {
				fmt.Printf("  %-28s (skip: %v)\n", name, err)
				continue
			}
			total++
			hasTools := len(item.Tools) > 0
			hasWorkflow := item.Workflow != nil
			if hasTools {
				toolBound++
			} else {
				textOnly++
			}
			if hasWorkflow {
				withWorkflow++
			}

			kind := "TEXT"
			if hasTools {
				kind = "TOOL"
			}
			fmt.Printf("  %-28s %-5s tools=%v workflow=%v\n   tools: %v\n",
				name, kind, hasTools, hasWorkflow, item.Tools)
		}
		fmt.Println()
	}

	fmt.Println("=== summary ===")
	fmt.Printf("total=%d  text-only=%d  tool-bound=%d  with-workflow=%d\n",
		total, textOnly, toolBound, withWorkflow)
	if total > 0 {
		fmt.Printf("tool-bound share: %d%%\n", toolBound*100/total)
	}
}

// loadSkill parses one skill.yaml into the registry's Skill shape.
func loadSkill(dir string) (*skill.Skill, error) {
	candidates := []string{"skill.yaml", "SKILL.md"}
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.HasSuffix(name, ".md") {
			// Codex-style: treated as a document skill (no tool binding by design).
			return &skill.Skill{Name: strings.TrimSpace(readName(raw))}, nil
		}
		var parsed skill.Skill
		if err := yaml.Unmarshal(raw, &parsed); err != nil {
			return nil, err
		}
		return &parsed, nil
	}
	return nil, fmt.Errorf("no skill manifest")
}

// readName pulls the `name:` line out of a SKILL.md front matter block.
func readName(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
	}
	return ""
}
