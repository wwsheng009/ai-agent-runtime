// Package workspaceregistry maintains the durable registry of workspace
// directories surfaced in the workspace UI.
//
// The registry only records directories that already exist on the server:
// Add never creates directories and Remove never deletes files. The backing
// document lives next to the other aicli stores
// (~/.aicli/workspace_directories.yaml) and follows the same fail-closed
// rules as foldertrust: when no user home resolves, the store lists nothing
// and persists nothing (never a cwd-relative file a clone could ship).
package workspaceregistry

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"gopkg.in/yaml.v3"
)

// StoreFileName is the durable workspace-directory document under the aicli home.
const StoreFileName = "workspace_directories.yaml"

// DirectoryRecord is one registered workspace directory.
type DirectoryRecord struct {
	ID         string `yaml:"id" json:"id"`                        // sha1(pathKey(path))[:12]
	Path       string `yaml:"path" json:"path"`                    // cleaned absolute path (native separators)
	Name       string `yaml:"name,omitempty" json:"name,omitempty"` // optional alias; UI falls back to basename
	CreatedAt  int64  `yaml:"created_at" json:"created_at"`         // unix seconds
	LastUsedAt int64  `yaml:"last_used_at,omitempty" json:"last_used_at,omitempty"`
}

// Sentinel errors surfaced to the HTTP layer.
var (
	// ErrValidationFailed covers empty/relative paths and unreadable paths.
	ErrValidationFailed = errors.New("workspace directory path is invalid")
	// ErrDirectoryNotFound: the path does not exist on the server.
	ErrDirectoryNotFound = errors.New("workspace directory does not exist on the server")
	// ErrNotDirectory: the path exists but is a file (or other non-directory).
	ErrNotDirectory = errors.New("path exists but is not a directory")
	// ErrNotFound: unknown registry id.
	ErrNotFound = errors.New("workspace directory not found in registry")
	// ErrStoreUnavailable: no-home environment, registry is fail-closed.
	ErrStoreUnavailable = errors.New("workspace directory registry is unavailable")
)

type storeDocument struct {
	Version     int               `yaml:"version,omitempty" json:"version,omitempty"`
	Directories []DirectoryRecord `yaml:"directories" json:"directories"`
}

// Store is the durable workspace-directory registry.
//
// path is empty only when no user home resolves: the store then lists
// nothing and persists nothing (fail closed — never write a cwd-relative
// store a clone could ship).
type Store struct {
	mu   sync.Mutex
	doc  storeDocument
	path string // empty => no-home, persist-nothing
}

// DefaultStorePath returns ~/.aicli/workspace_directories.yaml, or "" when
// no home resolves. Honors AICLI_HOME when set (same home root as
// plugins/sessions/foldertrust).
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

// Load opens the default store path (or an empty no-home store).
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
	return &Store{doc: storeDocument{Version: 1}}
}

// Path returns the backing file path (may be empty for no-home stores).
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Len returns the number of registered directories.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.doc.Directories)
}

// List returns a copy of the registered directories.
// A no-home store lists nothing (fail closed).
func (s *Store) List() []DirectoryRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DirectoryRecord, len(s.doc.Directories))
	copy(out, s.doc.Directories)
	return out
}

// Get returns the record for id.
func (s *Store) Get(id string) (DirectoryRecord, bool) {
	if s == nil {
		return DirectoryRecord{}, false
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.doc.Directories {
		if rec.ID == id {
			return rec, true
		}
	}
	return DirectoryRecord{}, false
}

// FindByPath returns the record registered for path (dedup semantics:
// case-insensitive on Windows, exact elsewhere).
func (s *Store) FindByPath(path string) (DirectoryRecord, bool) {
	if s == nil {
		return DirectoryRecord{}, false
	}
	key := pathKey(filepath.Clean(strings.TrimSpace(path)))
	if key == "" || key == "." {
		return DirectoryRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.doc.Directories {
		if pathKey(rec.Path) == key {
			return rec, true
		}
	}
	return DirectoryRecord{}, false
}

// Add validates path and registers it. The directory must already exist on
// the server (registration never creates directories). Re-adding an already
// registered path is idempotent: the existing record is returned with
// existing=true and the document is left untouched.
func (s *Store) Add(path, name string) (DirectoryRecord, bool, error) {
	normalized, err := NormalizePath(path)
	if err != nil {
		return DirectoryRecord{}, false, err
	}
	if s == nil {
		return DirectoryRecord{}, false, ErrStoreUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.path) == "" {
		return DirectoryRecord{}, false, ErrStoreUnavailable
	}

	// Re-read so concurrent writers are merged rather than clobbered.
	s.doc = s.readDocLocked()
	key := pathKey(normalized)
	for _, rec := range s.doc.Directories {
		if pathKey(rec.Path) == key {
			return rec, true, nil
		}
	}
	rec := DirectoryRecord{
		ID:        directoryID(normalized),
		Path:      normalized,
		Name:      strings.TrimSpace(name),
		CreatedAt: time.Now().Unix(),
	}
	s.doc.Directories = append(s.doc.Directories, rec)
	if s.doc.Version == 0 {
		s.doc.Version = 1
	}
	if err := s.persistDocLocked(); err != nil {
		return DirectoryRecord{}, false, err
	}
	return rec, false, nil
}

// Rename updates the display alias of a registered directory. The path is
// immutable: changing a path equals remove + add and would drift sessions
// already bound to the old path.
func (s *Store) Rename(id, name string) (DirectoryRecord, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return DirectoryRecord{}, fmt.Errorf("%w: missing directory id", ErrNotFound)
	}
	if s == nil {
		return DirectoryRecord{}, ErrStoreUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.path) == "" {
		return DirectoryRecord{}, ErrStoreUnavailable
	}

	s.doc = s.readDocLocked()
	for i := range s.doc.Directories {
		if s.doc.Directories[i].ID == id {
			s.doc.Directories[i].Name = strings.TrimSpace(name)
			if err := s.persistDocLocked(); err != nil {
				return DirectoryRecord{}, err
			}
			return s.doc.Directories[i], nil
		}
	}
	return DirectoryRecord{}, fmt.Errorf("%w: id %s", ErrNotFound, id)
}

// Remove unregisters a directory by id. It only drops the registry entry:
// files on the server are never touched. Returns the removed record and
// whether it existed.
func (s *Store) Remove(id string) (DirectoryRecord, bool, error) {
	id = strings.TrimSpace(id)
	if s == nil {
		return DirectoryRecord{}, false, ErrStoreUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.path) == "" {
		return DirectoryRecord{}, false, ErrStoreUnavailable
	}

	s.doc = s.readDocLocked()
	for i := range s.doc.Directories {
		if s.doc.Directories[i].ID == id {
			removed := s.doc.Directories[i]
			s.doc.Directories = append(s.doc.Directories[:i], s.doc.Directories[i+1:]...)
			if err := s.persistDocLocked(); err != nil {
				return DirectoryRecord{}, false, err
			}
			return removed, true, nil
		}
	}
	return DirectoryRecord{}, false, nil
}

// Touch refreshes LastUsedAt for a registered directory (best effort at the
// call site: session creation refreshes it, failures are non-fatal).
func (s *Store) Touch(id string) error {
	id = strings.TrimSpace(id)
	if s == nil {
		return ErrStoreUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.path) == "" {
		return ErrStoreUnavailable
	}

	s.doc = s.readDocLocked()
	for i := range s.doc.Directories {
		if s.doc.Directories[i].ID == id {
			s.doc.Directories[i].LastUsedAt = time.Now().Unix()
			return s.persistDocLocked()
		}
	}
	return fmt.Errorf("%w: id %s", ErrNotFound, id)
}

// NormalizePath validates a workspace directory path and returns the cleaned
// absolute path. The directory must already exist: registration never
// creates directories on the server.
func NormalizePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("%w: path is empty", ErrValidationFailed)
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("%w: path must be absolute: %q", ErrValidationFailed, trimmed)
	}
	cleaned := filepath.Clean(trimmed)
	info, err := os.Stat(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrDirectoryNotFound, cleaned)
		}
		return "", fmt.Errorf("%w: cannot stat path: %v", ErrValidationFailed, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrNotDirectory, cleaned)
	}
	return cleaned, nil
}

// pathKey returns the comparison key for a cleaned path. Windows paths are
// case-insensitive (drive letters, NTFS default), so comparisons fold case
// there; other platforms compare as-is.
func pathKey(cleanedPath string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleanedPath)
	}
	return cleanedPath
}

// directoryID derives the stable registry id from the canonical path key so
// case variants on Windows map to the same identity.
func directoryID(normalizedPath string) string {
	sum := sha1.Sum([]byte(pathKey(normalizedPath)))
	return hex.EncodeToString(sum[:])[:12]
}

// PathMatches reports whether two raw path strings refer to the same
// directory for grouping/counting purposes (cleaned compare, case-folded on
// Windows). Empty or relative-only fragments never match.
func PathMatches(a, b string) bool {
	ka := pathKey(filepath.Clean(strings.TrimSpace(a)))
	kb := pathKey(filepath.Clean(strings.TrimSpace(b)))
	if ka == "" || kb == "" || ka == "." || kb == "." {
		return false
	}
	return ka == kb
}

func (s *Store) readDoc() storeDocument {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readDocLocked()
}

func (s *Store) readDocLocked() storeDocument {
	if strings.TrimSpace(s.path) == "" {
		return storeDocument{Version: 1}
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return storeDocument{Version: 1}
	}
	var doc storeDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// Corrupted document: fail open to an empty registry and let the
		// next persist rewrite it, rather than blocking session features.
		return storeDocument{Version: 1}
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
		// Windows rename-over-existing can fail on some filesystems; retry
		// once after removing the target (same strategy as foldertrust).
		_ = os.Remove(s.path)
		if err2 := os.Rename(tmp, s.path); err2 != nil {
			return err2
		}
	}
	return nil
}
