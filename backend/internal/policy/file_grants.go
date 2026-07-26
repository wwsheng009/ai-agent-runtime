package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultGrantsFileName is the project-scoped durable grants file under .aicli/.
const DefaultGrantsFileName = "grants.json"

// FileGrantStore persists remembered grants under <project>/.aicli/grants.json.
// It implements GrantStore, GrantLister, and GrantRevoker.
type FileGrantStore struct {
	mu   sync.Mutex
	path string
}

type grantsFile struct {
	Version int     `json:"version"`
	Grants  []Grant `json:"grants"`
}

// ResolveProjectGrantsPath returns <projectRoot>/.aicli/grants.json.
// Empty projectRoot returns "".
func ResolveProjectGrantsPath(projectRoot string) string {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(projectRoot), ".aicli", DefaultGrantsFileName)
}

// NewFileGrantStore opens (or will create) a durable grant store at path.
// Empty path is invalid.
func NewFileGrantStore(path string) (*FileGrantStore, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return nil, fmt.Errorf("grants store path is required")
	}
	return &FileGrantStore{path: path}, nil
}

// OpenProjectGrantStore resolves and opens the project grants file store.
func OpenProjectGrantStore(projectRoot string) (*FileGrantStore, error) {
	path := ResolveProjectGrantsPath(projectRoot)
	if path == "" {
		return nil, fmt.Errorf("project root is required for grants store")
	}
	return NewFileGrantStore(path)
}

// Path returns the grants file path.
func (s *FileGrantStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Find returns the first matching grant (same semantics as MemoryGrantStore).
func (s *FileGrantStore) Find(toolName string, args map[string]interface{}) (Grant, bool) {
	if s == nil {
		return Grant{}, false
	}
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil || file == nil {
		return Grant{}, false
	}
	for _, grant := range file.Grants {
		if !strings.EqualFold(strings.TrimSpace(grant.Tool), toolName) {
			continue
		}
		pattern := strings.TrimSpace(grant.Pattern)
		if pattern == "" {
			return grant, true
		}
		if argsMatchGrantPattern(args, pattern) {
			return grant, true
		}
	}
	return Grant{}, false
}

// Remember stores a grant unless the tool is dangerous.
func (s *FileGrantStore) Remember(grant Grant) error {
	if s == nil {
		return fmt.Errorf("grants store is nil")
	}
	grant.Tool = strings.TrimSpace(grant.Tool)
	if grant.Tool == "" {
		return nil
	}
	if IsDangerousTool(grant.Tool) {
		return errDangerousGrant
	}
	grant.Pattern = strings.TrimSpace(grant.Pattern)
	grant.Scope = strings.TrimSpace(grant.Scope)
	if grant.Scope == "" {
		grant.Scope = "project"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return err
	}
	// de-dupe identical tool+pattern+scope
	for _, existing := range file.Grants {
		if strings.EqualFold(strings.TrimSpace(existing.Tool), grant.Tool) &&
			strings.EqualFold(strings.TrimSpace(existing.Pattern), grant.Pattern) &&
			strings.EqualFold(strings.TrimSpace(existing.Scope), grant.Scope) {
			return nil
		}
	}
	file.Grants = append(file.Grants, grant)
	return s.saveLocked(file)
}

// List returns a copy of remembered grants.
func (s *FileGrantStore) List() []Grant {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil || file == nil || len(file.Grants) == 0 {
		return nil
	}
	out := make([]Grant, len(file.Grants))
	copy(out, file.Grants)
	return out
}

// Revoke removes matching grants and returns how many were removed.
func (s *FileGrantStore) Revoke(toolName, pattern string, matchEmptyPattern bool) int {
	if s == nil {
		return 0
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return 0
	}
	pattern = strings.TrimSpace(pattern)

	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil || file == nil || len(file.Grants) == 0 {
		return 0
	}
	kept := make([]Grant, 0, len(file.Grants))
	removed := 0
	for _, grant := range file.Grants {
		if !strings.EqualFold(strings.TrimSpace(grant.Tool), toolName) {
			kept = append(kept, grant)
			continue
		}
		grantPattern := strings.TrimSpace(grant.Pattern)
		if pattern == "" {
			if matchEmptyPattern && grantPattern != "" {
				kept = append(kept, grant)
				continue
			}
			removed++
			continue
		}
		if !strings.EqualFold(grantPattern, pattern) {
			kept = append(kept, grant)
			continue
		}
		removed++
	}
	if removed == 0 {
		return 0
	}
	file.Grants = kept
	if err := s.saveLocked(file); err != nil {
		return 0
	}
	return removed
}

func (s *FileGrantStore) loadLocked() (*grantsFile, error) {
	if s == nil {
		return nil, fmt.Errorf("grants store is nil")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &grantsFile{Version: 1, Grants: nil}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return &grantsFile{Version: 1, Grants: nil}, nil
	}
	var file grantsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse grants file %s: %w", s.path, err)
	}
	if file.Version == 0 {
		file.Version = 1
	}
	if file.Grants == nil {
		file.Grants = nil
	}
	return &file, nil
}

func (s *FileGrantStore) saveLocked(file *grantsFile) error {
	if s == nil {
		return fmt.Errorf("grants store is nil")
	}
	if file == nil {
		file = &grantsFile{Version: 1}
	}
	if file.Version == 0 {
		file.Version = 1
	}
	if file.Grants == nil {
		file.Grants = []Grant{}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(s.path)
		if err2 := os.Rename(tmp, s.path); err2 != nil {
			_ = os.Remove(tmp)
			return err2
		}
	}
	return nil
}
