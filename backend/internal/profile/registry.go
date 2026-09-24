package profile

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Registry resolves named profile references to absolute roots.
type Registry struct {
	mu          sync.RWMutex
	defaultRoot string
	items       map[string]string
}

// NewRegistry creates a new profile registry.
func NewRegistry(defaultRoot string) *Registry {
	return &Registry{
		defaultRoot: strings.TrimSpace(defaultRoot),
		items:       make(map[string]string),
	}
}

// Register adds or replaces a named profile root.
func (r *Registry) Register(name, root string) error {
	if r == nil {
		return fmt.Errorf("profile registry is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	root, err := normalizeRoot(root)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[name] = root
	return nil
}

// Resolve returns the absolute root for a profile reference.
func (r *Registry) Resolve(ref string) (string, error) {
	if r == nil {
		return normalizeRoot(ref)
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("profile reference is required")
	}

	r.mu.RLock()
	if root, ok := r.items[ref]; ok {
		r.mu.RUnlock()
		return root, nil
	}
	defaultRoot := r.defaultRoot
	r.mu.RUnlock()

	if looksLikePath(ref) {
		return normalizeRoot(ref)
	}
	if strings.TrimSpace(defaultRoot) == "" {
		return "", fmt.Errorf("profile %q is not registered", ref)
	}
	return normalizeRoot(filepath.Join(defaultRoot, ref))
}

func looksLikePath(ref string) bool {
	if ref == "" {
		return false
	}
	if strings.ContainsRune(ref, filepath.Separator) {
		return true
	}
	if filepath.VolumeName(ref) != "" {
		return true
	}
	return false
}

// Entry is one registered profile name/root pair.
type Entry struct {
	Name string `json:"name"`
	Root string `json:"root"`
}

// Entries returns the registered names with their absolute roots, sorted by
// name (deterministic order for API listing).
func (r *Registry) Entries() []Entry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.items) == 0 {
		return nil
	}
	entries := make([]Entry, 0, len(r.items))
	for name, root := range r.items {
		entries = append(entries, Entry{Name: name, Root: root})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// DefaultRoot returns the registry's default root (empty when unset).
func (r *Registry) DefaultRoot() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultRoot
}
