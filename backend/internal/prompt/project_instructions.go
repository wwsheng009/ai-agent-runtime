package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var workspaceInstructionCandidates = []string{
	"AGENTS.override.md",
	"AGENTS.md",
}

const defaultWorkspaceInstructionBudget = 12 * 1024

// LoadWorkspaceInstructions discovers workspace-level instruction files from
// the workspace root toward the current directory and returns them as layered
// prompt fragments.
func LoadWorkspaceInstructions(path string, budgetBytes int) (*Layers, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return NewLayers(), nil
	}
	start, err := resolveWorkspaceInstructionDir(path)
	if err != nil {
		return nil, err
	}
	if budgetBytes <= 0 {
		budgetBytes = defaultWorkspaceInstructionBudget
	}

	dirs := workspaceAncestorDirs(start)
	layers := NewLayers()
	remaining := budgetBytes
	for _, dir := range dirs {
		filePath, ok := firstExistingInstructionFile(dir)
		if !ok {
			continue
		}
		content, err := loadPromptFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read workspace instruction file %s: %w", filePath, err)
		}
		content = strings.TrimSpace(content)
		if content == "" || remaining <= 0 {
			continue
		}

		truncated := false
		if len(content) > remaining {
			content = truncateWorkspaceInstruction(content, remaining)
			truncated = true
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		if truncated {
			content = strings.TrimSpace(content) + "\n\nNote: content truncated for prompt budget."
		}
		layers.AddLayer(LayerUser, workspaceInstructionTitle(start, dir), content, filePath)
		remaining -= len(content)
		if remaining <= 0 {
			break
		}
	}

	return layers, nil
}

func resolveWorkspaceInstructionDir(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return filepath.Clean(path), nil
	}
	return filepath.Dir(filepath.Clean(path)), nil
}

func workspaceAncestorDirs(start string) []string {
	if strings.TrimSpace(start) == "" {
		return nil
	}
	parts := make([]string, 0, 8)
	current := filepath.Clean(start)
	for {
		parts = append(parts, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return parts
}

func firstExistingInstructionFile(dir string) (string, bool) {
	for _, name := range workspaceInstructionCandidates {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func workspaceInstructionTitle(startDir, ownerDir string) string {
	startDir = filepath.Clean(startDir)
	ownerDir = filepath.Clean(ownerDir)
	if sameDirectory(startDir, ownerDir) {
		return "Workspace Instructions"
	}
	rel, err := filepath.Rel(ownerDir, startDir)
	if err != nil || strings.TrimSpace(rel) == "" || rel == "." {
		return "Workspace Instructions"
	}
	return "Workspace Instructions (" + filepath.ToSlash(rel) + ")"
}

func sameDirectory(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func truncateWorkspaceInstruction(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= limit {
		return string(runes)
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}
