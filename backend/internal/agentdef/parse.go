package agentdef

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseFile loads an agent definition from a .md (frontmatter+body) or .yaml/.yml file.
func ParseFile(path string) (*Definition, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return nil, fmt.Errorf("agentdef: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agentdef: read %s: %w", path, err)
	}
	def, err := Parse(data, path)
	if err != nil {
		return nil, err
	}
	def.SourcePath = path
	return def, nil
}

// Parse parses agent definition bytes. pathHint is used only for format inference and errors.
func Parse(data []byte, pathHint string) (*Definition, error) {
	pathHint = strings.TrimSpace(pathHint)
	ext := strings.ToLower(filepath.Ext(pathHint))
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	var (
		frontmatter []byte
		body        string
		err         error
	)

	switch ext {
	case ".yaml", ".yml":
		frontmatter = []byte(strings.TrimSpace(content))
		body = ""
	case ".md", ".markdown":
		frontmatter, body, err = splitFrontmatter([]byte(content))
		if err != nil {
			return nil, fmt.Errorf("agentdef: %s: %w", pathHint, err)
		}
	default:
		// Auto-detect: prefer frontmatter when present, else pure YAML.
		if fm, b, splitErr := splitFrontmatter([]byte(content)); splitErr == nil {
			frontmatter, body = fm, b
		} else {
			frontmatter = []byte(strings.TrimSpace(content))
			body = ""
		}
	}

	if len(strings.TrimSpace(string(frontmatter))) == 0 {
		return nil, fmt.Errorf("agentdef: empty definition%v", formatPathSuffix(pathHint))
	}

	var def Definition
	if err := yaml.Unmarshal(frontmatter, &def); err != nil {
		return nil, fmt.Errorf("agentdef: unmarshal yaml%v: %w", formatPathSuffix(pathHint), err)
	}
	def.Body = strings.TrimSpace(body)
	if pathHint != "" {
		def.SourcePath = pathHint
	}
	// Derive name from filename when omitted.
	if strings.TrimSpace(def.Name) == "" && pathHint != "" {
		base := filepath.Base(pathHint)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		if base != "" && base != "." {
			def.Name = base
		}
	}
	def.Normalize()
	if err := Validate(&def); err != nil {
		return nil, err
	}
	return &def, nil
}

func splitFrontmatter(data []byte) ([]byte, string, error) {
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")

	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.TrimSpace(line) != "---" {
			return nil, "", fmt.Errorf("missing YAML frontmatter")
		}
		start = i
		break
	}
	if start < 0 {
		return nil, "", fmt.Errorf("missing YAML frontmatter")
	}

	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, "", fmt.Errorf("missing YAML frontmatter terminator")
	}

	frontmatter := strings.Join(lines[start+1:end], "\n")
	body := ""
	if end+1 < len(lines) {
		body = strings.Join(lines[end+1:], "\n")
	}
	return []byte(frontmatter), body, nil
}

func formatPathSuffix(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return " (" + path + ")"
}
