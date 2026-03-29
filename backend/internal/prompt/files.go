package prompt

import (
	"fmt"
	"os"
	"strings"
)

// Files describes optional prompt file locations.
type Files struct {
	System string `json:"system,omitempty"`
	Role   string `json:"role,omitempty"`
	Tools  string `json:"tools,omitempty"`
}

// LoadedFiles contains loaded prompt contents together with their source files.
type LoadedFiles struct {
	Files  Files  `json:"files"`
	System string `json:"system,omitempty"`
	Role   string `json:"role,omitempty"`
	Tools  string `json:"tools,omitempty"`
}

// LoadFiles loads prompt contents from optional file paths.
func LoadFiles(files Files) (*LoadedFiles, error) {
	system, err := loadPromptFile(files.System)
	if err != nil {
		return nil, err
	}
	role, err := loadPromptFile(files.Role)
	if err != nil {
		return nil, err
	}
	tools, err := loadPromptFile(files.Tools)
	if err != nil {
		return nil, err
	}
	return &LoadedFiles{
		Files:  files,
		System: system,
		Role:   role,
		Tools:  tools,
	}, nil
}

// HasAny reports whether any prompt content was loaded.
func (l *LoadedFiles) HasAny() bool {
	if l == nil {
		return false
	}
	return strings.TrimSpace(l.System) != "" || strings.TrimSpace(l.Role) != "" || strings.TrimSpace(l.Tools) != ""
}

func loadPromptFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read prompt file %s: %w", path, err)
	}
	return strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), nil
}
