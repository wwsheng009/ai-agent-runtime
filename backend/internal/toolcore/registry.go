package toolcore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolschema"
)

// Filter narrows Registry.List. Surface filtering (profile/tools/trust) is not
// part of core and remains the surface's responsibility.
type Filter struct {
	Capabilities []Capability
	Names        []string
	Namespace    string
}

// Registry resolves canonical names and aliases to one Handler per tool.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	aliases  map[string]string
	schemas  map[string]json.RawMessage
	defs     map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]Handler),
		aliases:  make(map[string]string),
		schemas:  make(map[string]json.RawMessage),
		defs:     make(map[string]Definition),
	}
}

// Register canonicalizes and validates the schema once. Canonical names and
// aliases share one namespace so an alias can never shadow another tool.
func (r *Registry) Register(handler Handler) error {
	if handler == nil {
		return fmt.Errorf("tool handler is nil")
	}
	def := handler.Definition()
	if err := ValidateDefinition(def); err != nil {
		return err
	}
	name := strings.TrimSpace(def.Name)
	var schemaMap map[string]any
	if err := json.Unmarshal(def.InputSchema, &schemaMap); err != nil {
		return fmt.Errorf("tool %q schema must be a JSON object: %w", name, err)
	}
	canonical, _, err := toolschema.CanonicalizeAndValidate(schemaMap)
	if err != nil {
		return fmt.Errorf("tool %q has invalid schema: %w", name, err)
	}
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		return fmt.Errorf("tool %q schema marshal failed: %w", name, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[name]; exists {
		return fmt.Errorf("tool %q already registered", name)
	}
	if existing, exists := r.aliases[name]; exists {
		return fmt.Errorf("tool name %q collides with alias of %q", name, existing)
	}
	for _, alias := range def.Aliases {
		alias = strings.TrimSpace(alias)
		if _, exists := r.handlers[alias]; exists {
			return fmt.Errorf("alias %q collides with tool %q", alias, alias)
		}
		if owner, exists := r.aliases[alias]; exists {
			return fmt.Errorf("alias %q already belongs to %q", alias, owner)
		}
	}

	def.Name = name
	def.InputSchema = canonicalJSON
	r.handlers[name] = handler
	r.schemas[name] = canonicalJSON
	r.defs[name] = def
	for _, alias := range def.Aliases {
		r.aliases[strings.TrimSpace(alias)] = name
	}
	return nil
}

func (r *Registry) Unregister(name string) error {
	name = strings.TrimSpace(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	def, exists := r.defs[name]
	if !exists {
		return fmt.Errorf("tool %q not found", name)
	}
	for _, alias := range def.Aliases {
		delete(r.aliases, strings.TrimSpace(alias))
	}
	delete(r.handlers, name)
	delete(r.schemas, name)
	delete(r.defs, name)
	return nil
}

func (r *Registry) Resolve(name string) (Handler, bool) {
	name = strings.TrimSpace(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if handler, ok := r.handlers[name]; ok {
		return handler, true
	}
	canonical, ok := r.aliases[name]
	if !ok {
		return nil, false
	}
	handler, ok := r.handlers[canonical]
	return handler, ok
}

func (r *Registry) Schema(name string) (json.RawMessage, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	name = strings.TrimSpace(name)
	if _, ok := r.schemas[name]; !ok {
		canonical, aliasOK := r.aliases[name]
		if !aliasOK {
			return nil, false
		}
		name = canonical
	}
	schema, ok := r.schemas[name]
	if !ok {
		return nil, false
	}
	return append(json.RawMessage(nil), schema...), true
}

func (r *Registry) List(filter Filter) []Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names)

	nameSet := make(map[string]struct{}, len(filter.Names))
	for _, name := range filter.Names {
		nameSet[strings.TrimSpace(name)] = struct{}{}
	}
	capSet := make(map[Capability]struct{}, len(filter.Capabilities))
	for _, capability := range filter.Capabilities {
		capSet[capability] = struct{}{}
	}

	out := make([]Handler, 0, len(names))
	for _, name := range names {
		def := r.defs[name]
		if len(nameSet) > 0 {
			if _, ok := nameSet[name]; !ok {
				continue
			}
		}
		if namespace := strings.TrimSpace(filter.Namespace); namespace != "" {
			marker, ok := def.Metadata["namespace"].(string)
			if !ok || marker != namespace {
				continue
			}
		}
		if len(capSet) > 0 {
			matched := false
			for _, capability := range def.Capabilities {
				if _, ok := capSet[capability]; ok {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, r.handlers[name])
	}
	return out
}

// Definitions returns a deterministic snapshot for list/describe surfaces.
func (r *Registry) Definitions() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.defs))
	for name := range r.defs {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Definition, 0, len(names))
	for _, name := range names {
		out = append(out, cloneDefinition(r.defs[name]))
	}
	return out
}

func cloneDefinition(def Definition) Definition {
	def.Aliases = append([]string(nil), def.Aliases...)
	def.Capabilities = append([]Capability(nil), def.Capabilities...)
	def.InputSchema = append(json.RawMessage(nil), def.InputSchema...)
	if len(def.Metadata) > 0 {
		metadata := make(map[string]any, len(def.Metadata))
		for key, value := range def.Metadata {
			metadata[key] = value
		}
		def.Metadata = metadata
	}
	return def
}
