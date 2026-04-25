package prompt

import (
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	RoleSystem    = "system"
	RoleDeveloper = "developer"

	LayerBase      = "base"
	LayerDeveloper = "developer"
	LayerUser      = "user"
)

// Fragment describes one prompt section together with its intended instruction layer.
type Fragment struct {
	Layer  string `json:"layer,omitempty"`
	Role   string `json:"role,omitempty"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	Source string `json:"source,omitempty"`
}

// Layers stores structured prompt fragments before they are compiled into messages.
type Layers struct {
	Fragments []Fragment `json:"fragments,omitempty"`
}

// NewLayers creates an empty prompt layer collection.
func NewLayers() *Layers {
	return &Layers{Fragments: make([]Fragment, 0)}
}

// Add appends a fragment when it contains meaningful content.
func (l *Layers) Add(role, title, body, source string) {
	l.AddLayer(layerFromRole(role), title, body, source)
}

// AddLayer appends a fragment using the logical prompt layer model.
func (l *Layers) AddLayer(layer, title, body, source string) {
	if l == nil {
		return
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	layer = normalizeInstructionLayer(layer)
	if layer == "" {
		layer = LayerBase
	}
	l.Fragments = append(l.Fragments, Fragment{
		Layer:  layer,
		Role:   compiledRoleForLayer(layer, ""),
		Title:  strings.TrimSpace(title),
		Body:   body,
		Source: strings.TrimSpace(source),
	})
}

// Append merges another layer collection into the receiver.
func (l *Layers) Append(other *Layers) {
	if l == nil || other == nil || len(other.Fragments) == 0 {
		return
	}
	for _, fragment := range other.Fragments {
		l.AddLayer(firstNonEmptyLayer(fragment.Layer, layerFromRole(fragment.Role)), fragment.Title, fragment.Body, fragment.Source)
	}
}

// HasAny reports whether the layer collection contains visible fragments.
func (l *Layers) HasAny() bool {
	return l != nil && len(l.Fragments) > 0
}

// Clone copies the layer collection.
func (l *Layers) Clone() *Layers {
	if !l.HasAny() {
		return NewLayers()
	}
	cloned := NewLayers()
	cloned.Fragments = append(cloned.Fragments, l.Fragments...)
	return cloned
}

// CompileInstructionMessages converts structured fragments into runtime messages.
// Codex/OpenAI can preserve developer instructions; other providers collapse
// all instruction layers into system messages for compatibility.
func (l *Layers) CompileInstructionMessages(provider string) []types.Message {
	if !l.HasAny() {
		return nil
	}

	messages := make([]types.Message, 0, len(l.Fragments))
	var currentRole string
	var currentLayer string
	var currentParts []string
	var currentFragments []Fragment

	flush := func() {
		if strings.TrimSpace(currentRole) == "" || len(currentParts) == 0 {
			currentRole = ""
			currentLayer = ""
			currentParts = nil
			currentFragments = nil
			return
		}
		content := strings.TrimSpace(strings.Join(currentParts, "\n\n"))
		if content == "" {
			currentRole = ""
			currentLayer = ""
			currentParts = nil
			currentFragments = nil
			return
		}
		var message *types.Message
		if currentRole == RoleDeveloper {
			message = types.NewDeveloperMessage(content)
		} else {
			message = types.NewSystemMessage(content)
		}
		attachCompiledInstructionMetadata(message, currentLayer, currentFragments)
		messages = append(messages, *message)
		currentRole = ""
		currentLayer = ""
		currentParts = nil
		currentFragments = nil
	}

	for _, fragment := range l.Fragments {
		layer := firstNonEmptyLayer(fragment.Layer, layerFromRole(fragment.Role))
		role := compiledRoleForLayer(layer, provider)
		if currentRole != "" && role != currentRole {
			flush()
		}
		if currentLayer != "" && layer != currentLayer {
			flush()
		}
		currentRole = role
		currentLayer = layer
		currentParts = append(currentParts, renderFragment(fragment))
		currentFragments = append(currentFragments, fragment)
	}
	flush()

	return messages
}

// RenderModelVisibleLayout renders a stable debug view of compiled instruction messages.
func (l *Layers) RenderModelVisibleLayout(provider string) string {
	messages := l.CompileInstructionMessages(provider)
	return RenderInstructionMessagesLayout(messages)
}

// RenderInstructionMessagesLayout renders a stable view for the leading
// instruction messages that are visible to the model.
func RenderInstructionMessagesLayout(messages []types.Message) string {
	if len(messages) == 0 {
		return ""
	}
	leading := instructionMessagePrefix(messages)
	if len(leading) == 0 {
		return ""
	}
	parts := make([]string, 0, len(leading))
	for _, message := range leading {
		layer := strings.TrimSpace(metadataStringValue(message.Metadata, "prompt_layer"))
		if layer == "" {
			layer = "unknown"
		}
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "system"
		}
		parts = append(parts, "["+layer+"/"+role+"]\n"+strings.TrimSpace(message.Content))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func instructionMessagePrefix(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	parts := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case RoleSystem, RoleDeveloper:
			parts = append(parts, *message.Clone())
		default:
			return parts
		}
	}
	return parts
}

func normalizeInstructionLayer(layer string) string {
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case LayerBase:
		return LayerBase
	case LayerDeveloper:
		return LayerDeveloper
	case LayerUser:
		return LayerUser
	default:
		return ""
	}
}

func layerFromRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RoleDeveloper:
		return LayerDeveloper
	case RoleSystem:
		return LayerBase
	default:
		return ""
	}
}

func compiledRoleForLayer(layer string, provider string) string {
	switch normalizeInstructionLayer(layer) {
	case LayerDeveloper, LayerUser:
		if providerSupportsDeveloperRole(provider) {
			return RoleDeveloper
		}
		return RoleSystem
	default:
		return RoleSystem
	}
}

func providerSupportsDeveloperRole(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "openai", "codex":
		return true
	default:
		return false
	}
}

func attachCompiledInstructionMetadata(message *types.Message, layer string, fragments []Fragment) {
	if message == nil {
		return
	}
	if message.Metadata == nil {
		message.Metadata = types.NewMetadata()
	}
	layer = normalizeInstructionLayer(layer)
	if layer == "" {
		layer = LayerBase
	}
	message.Metadata["prompt_layer"] = layer
	if len(fragments) == 1 {
		if title := strings.TrimSpace(fragments[0].Title); title != "" {
			message.Metadata["prompt_title"] = title
		}
		if source := strings.TrimSpace(fragments[0].Source); source != "" {
			message.Metadata["prompt_source"] = filepath.Clean(source)
		}
		return
	}
	titles := make([]string, 0, len(fragments))
	sources := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		if title := strings.TrimSpace(fragment.Title); title != "" {
			titles = append(titles, title)
		}
		if source := strings.TrimSpace(fragment.Source); source != "" {
			sources = append(sources, filepath.Clean(source))
		}
	}
	if len(titles) > 0 {
		message.Metadata["prompt_titles"] = titles
	}
	if len(sources) > 0 {
		message.Metadata["prompt_sources"] = sources
	}
}

func renderFragment(fragment Fragment) string {
	lines := make([]string, 0, 3)
	if title := strings.TrimSpace(fragment.Title); title != "" {
		lines = append(lines, "# "+title)
	}
	if source := strings.TrimSpace(fragment.Source); source != "" {
		lines = append(lines, "Source: "+filepath.Clean(source))
	}
	if body := strings.TrimSpace(fragment.Body); body != "" {
		lines = append(lines, body)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func metadataStringValue(metadata types.Metadata, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return value
}

func firstNonEmptyLayer(values ...string) string {
	for _, value := range values {
		if normalized := normalizeInstructionLayer(value); normalized != "" {
			return normalized
		}
	}
	return ""
}
