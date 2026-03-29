package profile

import (
	"fmt"
	"path/filepath"
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
