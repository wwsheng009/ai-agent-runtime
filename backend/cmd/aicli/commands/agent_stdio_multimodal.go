package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// acpPromptImageMaxBytes bounds one decoded inbound image. ACP image blocks are
// base64 in-band, so an unbounded payload would be copied several times
// (JSON-RPC buffer -> base64 decode -> staging file) before the turn starts.
const acpPromptImageMaxBytes = 20 << 20

// acpImageOnlyPromptText is substituted when a client sends image blocks with
// no text (pasting a screenshot without typing). The runtime only attaches
// images to a non-empty user message, so an image-only turn needs a carrier.
const acpImageOnlyPromptText = "Please look at the attached image."

// acpPromptTextForTurn returns the prompt text for a decoded ACP payload. An
// image-only paste (no text) is carried by a placeholder prompt because the
// runtime attaches images to a non-empty user message.
func acpPromptTextForTurn(content acp.PromptContent) string {
	text := strings.TrimSpace(content.Text)
	if text == "" && len(content.Images) > 0 {
		return acpImageOnlyPromptText
	}
	return text
}

// stageACPPromptImages writes inbound ACP image blocks into the session's
// staging directory and returns their local paths. The files live until the
// session closes so that multi-turn history (which references them by path)
// keeps resolving; non-ephemeral sessions additionally persist copies into the
// session artifact directory during the turn.
func stageACPPromptImages(hostSess *acpHostSession, images []acp.PromptImage) ([]string, error) {
	if hostSess == nil || len(images) == 0 {
		return nil, nil
	}
	dir, err := hostSess.ensureImageDir()
	if err != nil {
		return nil, fmt.Errorf("prepare ACP image staging dir: %w", err)
	}
	seq := hostSess.nextImageSeq()
	paths := make([]string, 0, len(images))
	for index, image := range images {
		if len(image.Data) == 0 {
			return nil, fmt.Errorf("image %d has no data", index+1)
		}
		if len(image.Data) > acpPromptImageMaxBytes {
			return nil, fmt.Errorf("image %d exceeds the %d MiB prompt limit", index+1, acpPromptImageMaxBytes>>20)
		}
		name := fmt.Sprintf("acp-image-%d-%d%s", seq, index+1, acpPromptImageExtension(image.MIMEType))
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, image.Data, 0o600); err != nil {
			return nil, fmt.Errorf("write ACP image %d: %w", index+1, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func acpPromptImageExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".img"
	}
}

// ensureImageDir lazily creates the per-session staging directory used for
// inbound ACP image blocks. closeSessionLocked removes it.
func (s *acpHostSession) ensureImageDir() (string, error) {
	if s == nil {
		return "", fmt.Errorf("acp session is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.imageDir != "" {
		return s.imageDir, nil
	}
	dir, err := os.MkdirTemp("", "aicli-acp-images-")
	if err != nil {
		return "", err
	}
	s.imageDir = dir
	return dir, nil
}

// nextImageSeq returns a per-session sequence number so staged files from
// different turns never collide.
func (s *acpHostSession) nextImageSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.imageSeq++
	return s.imageSeq
}

// acpModelAcceptsImageInput reports whether the session's currently selected
// model accepts image input. Only a positive declaration that the model is
// text-only blocks the turn: when no capability is declared the runtime stays
// permissive and lets the provider answer, so providers added without modality
// metadata keep working.
func acpModelAcceptsImageInput(chat *ChatSession) bool {
	if chat == nil {
		return true
	}
	provider := chat.Provider
	if len(provider.ModelCapabilities) == 0 && chat.Config != nil {
		if named, ok := chat.Config.Providers.Items[strings.TrimSpace(chat.ProviderName)]; ok {
			provider = named
		}
	}
	model := acpChatResolvedModel(chat)
	spec, ok := config.ResolveModelCapabilitySpec(model, provider.ModelCapabilities)
	if !ok || len(spec.InputModalities) == 0 {
		return true
	}
	for _, modality := range spec.InputModalities {
		if strings.EqualFold(strings.TrimSpace(modality), "image") {
			return true
		}
	}
	return false
}

// acpChatResolvedModel returns the model the next turn will actually run on.
func acpChatResolvedModel(chat *ChatSession) string {
	if chat == nil {
		return ""
	}
	return strings.TrimSpace(firstNonEmptyChatValue(chat.EffectiveModel, chat.RequestedModel, chat.Model))
}
