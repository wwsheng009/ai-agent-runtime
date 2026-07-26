package foldertrust

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"gopkg.in/yaml.v3"
)

// StoreFileName is the durable folder-trust document under the aicli home.
const StoreFileName = "trusted_folders.yaml"

// FolderRecord is one folder's trust decision.
type FolderRecord struct {
	Trusted   bool  `yaml:"trusted" json:"trusted"`
	DecidedAt int64 `yaml:"decided_at,omitempty" json:"decided_at,omitempty"`
}

type storeDocument struct {
	Version int                     `yaml:"version,omitempty" json:"version,omitempty"`
	Folders map[string]FolderRecord `yaml:"folders" json:"folders"`
}

// Store is the durable trusted-folders registry (~/.aicli/trusted_folders.yaml).
//
// Trust cascades to subdirectories: a trusted parent trusts all children.
// When both an ancestor and a nearer folder are recorded, the longest matching
// prefix wins (so an explicit child untrust overrides an ancestor trust).
//
// path is empty only when no user home resolves: the store then trusts nothing
// and persists nothing (fail closed — never write a cwd-relative store a clone
// could ship to self-trust).
type Store struct {
	mu   sync.Mutex
	doc  storeDocument
	path string // empty => no-home, persist-nothing
}

// DefaultStorePath returns ~/.aicli/trusted_folders.yaml, or "" when no home.
// Honors AICLI_HOME when set (same home root as plugins/sessions).
func DefaultStorePath() string {
	if home := strings.TrimSpace(os.Getenv("AICLI_HOME")); home != "" {
		expanded := aiclipaths.ExpandUserPath(home)
		if strings.TrimSpace(expanded) == "" {
			return ""
		}
		return filepath.Join(expanded, StoreFileName)
	}
	userHome, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(userHome) == "" {
		return ""
	}
	return filepath.Join(userHome, ".aicli", StoreFileName)
}

// Load opens the default store path (or empty no-home store).
func Load() *Store {
	path := DefaultStorePath()
	if path == "" {
		return emptyStore()
	}
	return LoadFrom(path)
}

// LoadFrom opens a custom store path (tests).
func LoadFrom(path string) *Store {
	path = strings.TrimSpace(path)
	if path == "" {
		return emptyStore()
	}
	s := &Store{path: filepath.Clean(path)}
	s.doc = s.readDoc()
	return s
}

func emptyStore() *Store {
	return &Store{
		doc: storeDocument{Version: 1, Folders: map[string]FolderRecord{}},
	}
}

// Path returns the backing file path (may be empty).
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Len returns the number of recorded folders.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.doc.Folders)
}

// IsTrusted reports whether workspaceKey is trusted via the most-specific record.
func (s *Store) IsTrusted(workspaceKey string) bool {
	if s == nil {
		return false
	}
	key := Canonicalize(workspaceKey)
	if key == "" || IsUnsafeTrustRoot(key) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bestDepth := -1
	trusted := false
	for folder, record := range s.doc.Folders {
		folder = Canonicalize(folder)
		if folder == "" || IsUnsafeTrustRoot(folder) {
			continue
		}
		if !pathIsUnder(key, folder) {
			continue
		}
		depth := pathDepth(folder)
		switch {
		case depth < bestDepth:
			// shorter ancestor — ignore
		case depth == bestDepth:
			// tie: require every tied record to be trusted (fail closed)
			trusted = trusted && record.Trusted
		default:
			bestDepth = depth
			trusted = record.Trusted
		}
	}
	return trusted
}

// SetTrusted records workspaceKey as trusted and persists.
// Over-broad roots are refused (no-op Ok). No-home store is a no-op.
func (s *Store) SetTrusted(workspaceKey string) error {
	return s.recordDecision(workspaceKey, true)
}

// SetUntrusted records workspaceKey as explicitly untrusted and persists.
func (s *Store) SetUntrusted(workspaceKey string) error {
	return s.recordDecision(workspaceKey, false)
}

func (s *Store) recordDecision(workspaceKey string, trusted bool) error {
	if s == nil {
		return fmt.Errorf("folder trust store is nil")
	}
	// Refuse non-absolute inputs before Abs/canonicalize (Abs would otherwise
	// promote "relative/path" under cwd into a recordable absolute key).
	raw := strings.TrimSpace(workspaceKey)
	if raw == "" || !filepath.IsAbs(raw) {
		return nil
	}
	key := Canonicalize(raw)
	if key == "" || IsUnsafeTrustRoot(key) {
		return nil
	}
	if strings.TrimSpace(s.path) == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-read so concurrent writers are merged rather than clobbered.
	s.doc = s.readDocLocked()
	if s.doc.Folders == nil {
		s.doc.Folders = map[string]FolderRecord{}
	}
	if s.doc.Version == 0 {
		s.doc.Version = 1
	}
	s.doc.Folders[key] = FolderRecord{
		Trusted:   trusted,
		DecidedAt: time.Now().Unix(),
	}
	if err := s.persistDocLocked(); err != nil {
		return err
	}
	return nil
}

func (s *Store) readDoc() storeDocument {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readDocLocked()
}

func (s *Store) readDocLocked() storeDocument {
	if strings.TrimSpace(s.path) == "" {
		return storeDocument{Version: 1, Folders: map[string]FolderRecord{}}
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return storeDocument{Version: 1, Folders: map[string]FolderRecord{}}
	}
	var doc storeDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return storeDocument{Version: 1, Folders: map[string]FolderRecord{}}
	}
	if doc.Folders == nil {
		doc.Folders = map[string]FolderRecord{}
	}
	if doc.Version == 0 {
		doc.Version = 1
	}
	return doc
}

func (s *Store) persistDocLocked() error {
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if s.doc.Folders == nil {
		s.doc.Folders = map[string]FolderRecord{}
	}
	if s.doc.Version == 0 {
		s.doc.Version = 1
	}
	data, err := yaml.Marshal(&s.doc)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(s.path)
		if err2 := os.Rename(tmp, s.path); err2 != nil {
			return err2
		}
	}
	return nil
}

func pathDepth(path string) int {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return 0
	}
	// Count non-empty components (works on Windows volumes too).
	n := 0
	for _, c := range strings.Split(path, string(filepath.Separator)) {
		if c == "" || c == "." {
			continue
		}
		n++
	}
	return n
}
