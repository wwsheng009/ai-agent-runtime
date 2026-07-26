package toolprotocol

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Kind is the capability-oriented tool taxonomy kind.
// Values mirror types.ToolKind* so policy/taxonomy stay single-sourced.
type Kind string

const (
	KindRead    Kind = types.ToolKindRead
	KindSearch  Kind = types.ToolKindSearch
	KindEdit    Kind = types.ToolKindEdit
	KindExec    Kind = types.ToolKindExec
	KindNetwork Kind = types.ToolKindNetwork
	KindControl Kind = types.ToolKindControl
)

// NormalizeKind returns a known Kind or empty string.
func NormalizeKind(value string) Kind {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(KindRead):
		return KindRead
	case string(KindSearch):
		return KindSearch
	case string(KindEdit):
		return KindEdit
	case string(KindExec):
		return KindExec
	case string(KindNetwork):
		return KindNetwork
	case string(KindControl):
		return KindControl
	default:
		return ""
	}
}

// Capabilities describes static tool capability flags for permission/routing.
type Capabilities struct {
	Kind        Kind `json:"kind,omitempty"`
	ReadOnly    bool `json:"read_only,omitempty"`
	MutatesFS   bool `json:"mutates_fs,omitempty"`
	RequiresNet bool `json:"requires_net,omitempty"`
}

// FromDefinitionMetadata builds Capabilities from tool definition metadata.
func FromDefinitionMetadata(metadata map[string]interface{}) (Capabilities, bool) {
	if len(metadata) == 0 {
		return Capabilities{}, false
	}
	caps := Capabilities{}
	found := false
	if raw, ok := metadata[types.ToolMetadataKindKey]; ok {
		if kind := NormalizeKind(asString(raw)); kind != "" {
			caps.Kind = kind
			found = true
		}
	}
	if v, ok := types.BoolMetadataValue(metadata, types.ToolMetadataReadOnlyKey); ok {
		caps.ReadOnly = v
		found = true
	}
	if v, ok := types.BoolMetadataValue(metadata, types.ToolMetadataMutatesFSKey); ok {
		caps.MutatesFS = v
		found = true
	}
	if v, ok := types.BoolMetadataValue(metadata, types.ToolMetadataRequiresNetKey); ok {
		caps.RequiresNet = v
		found = true
	}
	return caps, found
}

func asString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case Kind:
		return string(typed)
	default:
		return ""
	}
}
