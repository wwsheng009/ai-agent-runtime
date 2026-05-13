package aiclitools

import (
	"fmt"
	"strings"
)

type Registry struct {
	caps  map[string]Capability
	order []string
}

func NewRegistry(capabilities ...Capability) *Registry {
	registry := &Registry{
		caps: make(map[string]Capability),
	}
	for _, cap := range capabilities {
		registry.Register(cap)
	}
	return registry
}

func (r *Registry) Register(cap Capability) error {
	if r == nil {
		return fmt.Errorf("capability registry is nil")
	}
	name := strings.TrimSpace(cap.Name)
	if name == "" {
		return fmt.Errorf("capability name is required")
	}
	cap.Name = name
	if r.caps == nil {
		r.caps = make(map[string]Capability)
	}
	if _, exists := r.caps[name]; !exists {
		r.order = append(r.order, name)
	}
	r.caps[name] = cap
	return nil
}

func (r *Registry) Get(name string) (Capability, bool) {
	if r == nil {
		return Capability{}, false
	}
	cap, ok := r.caps[strings.TrimSpace(name)]
	return cap, ok
}

func (r *Registry) List() []Capability {
	if r == nil {
		return nil
	}
	result := make([]Capability, 0, len(r.order))
	for _, name := range r.order {
		if cap, ok := r.caps[name]; ok {
			result = append(result, cap)
		}
	}
	return result
}

func (r *Registry) ListForPath(path ExposurePath) []Capability {
	if r == nil {
		return nil
	}
	result := []Capability{}
	for _, cap := range r.List() {
		if cap.SupportsPath(path) {
			result = append(result, cap)
		}
	}
	return result
}
