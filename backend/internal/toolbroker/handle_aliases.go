package toolbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	sessionHandleAliasesContextKey = "tool_handle_aliases"
	backgroundJobAliasPrefix       = "job_ref_"
	agentSessionAliasPrefix        = "session_ref_"
)

// SessionContextStore persists broker-scoped session context across broker instances.
type SessionContextStore interface {
	LoadContextValue(ctx context.Context, sessionID, key string) (interface{}, error)
	SaveContextValue(ctx context.Context, sessionID, key string, value interface{}) error
}

type handleAliasRegistry struct {
	Jobs     handleAliasSet `json:"jobs,omitempty"`
	Sessions handleAliasSet `json:"sessions,omitempty"`
}

type handleAliasSet struct {
	AliasToActual map[string]string `json:"alias_to_actual,omitempty"`
	ActualToAlias map[string]string `json:"actual_to_alias,omitempty"`
}

func loadHandleAliasRegistry(value interface{}) *handleAliasRegistry {
	registry := &handleAliasRegistry{}
	if value == nil {
		registry.normalize()
		return registry
	}

	switch typed := value.(type) {
	case *handleAliasRegistry:
		if typed != nil {
			registry = typed.clone()
			registry.normalize()
			return registry
		}
	case handleAliasRegistry:
		cloned := typed
		registry = (&cloned).clone()
		registry.normalize()
		return registry
	}

	payload, err := json.Marshal(value)
	if err != nil || len(payload) == 0 || string(payload) == "null" {
		registry.normalize()
		return registry
	}
	if err := json.Unmarshal(payload, registry); err != nil {
		registry = &handleAliasRegistry{}
	}
	registry.normalize()
	return registry
}

func (r *handleAliasRegistry) clone() *handleAliasRegistry {
	if r == nil {
		return &handleAliasRegistry{}
	}
	return &handleAliasRegistry{
		Jobs:     r.Jobs.clone(),
		Sessions: r.Sessions.clone(),
	}
}

func (r *handleAliasRegistry) normalize() {
	if r == nil {
		return
	}
	r.Jobs.normalize()
	r.Sessions.normalize()
}

func (r *handleAliasRegistry) contextValue() map[string]interface{} {
	normalized := r.clone()
	normalized.normalize()
	payload, err := json.Marshal(normalized)
	if err != nil || len(payload) == 0 || string(payload) == "null" {
		return map[string]interface{}{}
	}
	var value map[string]interface{}
	if err := json.Unmarshal(payload, &value); err != nil || value == nil {
		return map[string]interface{}{}
	}
	return value
}

func (s *handleAliasSet) clone() handleAliasSet {
	if s == nil {
		return handleAliasSet{
			AliasToActual: map[string]string{},
			ActualToAlias: map[string]string{},
		}
	}
	cloned := handleAliasSet{
		AliasToActual: make(map[string]string, len(s.AliasToActual)),
		ActualToAlias: make(map[string]string, len(s.ActualToAlias)),
	}
	for alias, actual := range s.AliasToActual {
		cloned.AliasToActual[alias] = actual
	}
	for actual, alias := range s.ActualToAlias {
		cloned.ActualToAlias[actual] = alias
	}
	return cloned
}

func (s *handleAliasSet) normalize() {
	if s.AliasToActual == nil {
		s.AliasToActual = make(map[string]string)
	}
	if s.ActualToAlias == nil {
		s.ActualToAlias = make(map[string]string)
	}
	for alias, actual := range s.AliasToActual {
		trimmedAlias := strings.TrimSpace(alias)
		trimmedActual := strings.TrimSpace(actual)
		delete(s.AliasToActual, alias)
		if trimmedAlias == "" || trimmedActual == "" {
			continue
		}
		s.AliasToActual[trimmedAlias] = trimmedActual
	}
	for actual, alias := range s.ActualToAlias {
		trimmedActual := strings.TrimSpace(actual)
		trimmedAlias := strings.TrimSpace(alias)
		delete(s.ActualToAlias, actual)
		if trimmedActual == "" || trimmedAlias == "" {
			continue
		}
		s.ActualToAlias[trimmedActual] = trimmedAlias
	}
	for alias, actual := range s.AliasToActual {
		if existingAlias := strings.TrimSpace(s.ActualToAlias[actual]); existingAlias == "" {
			s.ActualToAlias[actual] = alias
		}
	}
}

func (s *handleAliasSet) register(actual, preferredAlias string) string {
	if s == nil {
		return strings.TrimSpace(actual)
	}
	s.normalize()
	actual = strings.TrimSpace(actual)
	if actual == "" {
		return ""
	}
	if existing := strings.TrimSpace(s.ActualToAlias[actual]); existing != "" {
		s.AliasToActual[existing] = actual
		return existing
	}

	alias := strings.TrimSpace(preferredAlias)
	if alias == "" {
		return actual
	}
	if boundActual := strings.TrimSpace(s.AliasToActual[alias]); boundActual != "" && boundActual != actual {
		base := alias
		for index := 2; ; index++ {
			candidate := fmt.Sprintf("%s_%d", base, index)
			boundActual = strings.TrimSpace(s.AliasToActual[candidate])
			if boundActual == "" || boundActual == actual {
				alias = candidate
				break
			}
		}
	}

	s.AliasToActual[alias] = actual
	s.ActualToAlias[actual] = alias
	return alias
}

func (s *handleAliasSet) resolve(reference, prefix, label string) (actual string, alias string, err error) {
	if s == nil {
		return strings.TrimSpace(reference), "", nil
	}
	s.normalize()
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", "", nil
	}
	if actual = strings.TrimSpace(s.AliasToActual[reference]); actual != "" {
		return actual, reference, nil
	}
	if strings.HasPrefix(reference, prefix) {
		return "", "", fmt.Errorf("unknown %s reference: %s", label, reference)
	}
	return reference, strings.TrimSpace(s.ActualToAlias[reference]), nil
}

func (s *handleAliasSet) aliasFor(actual string) string {
	if s == nil {
		return strings.TrimSpace(actual)
	}
	s.normalize()
	actual = strings.TrimSpace(actual)
	if actual == "" {
		return ""
	}
	if alias := strings.TrimSpace(s.ActualToAlias[actual]); alias != "" {
		return alias
	}
	return actual
}

func deterministicHandleAlias(prefix, toolCallID, toolName string, args map[string]interface{}) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "ref_"
	}

	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID != "" {
		sum := sha256.Sum256([]byte(toolCallID))
		return prefix + hex.EncodeToString(sum[:6])
	}

	argsJSON, err := json.Marshal(args)
	if err != nil || len(argsJSON) == 0 || string(argsJSON) == "null" {
		argsJSON = []byte("{}")
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(toolName) + "\n" + string(argsJSON)))
	return prefix + hex.EncodeToString(sum[:6])
}
