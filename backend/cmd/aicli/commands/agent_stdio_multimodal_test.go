package commands

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestACPPromptTextForTurn(t *testing.T) {
	if got := acpPromptTextForTurn(acp.PromptContent{Text: "  review this  "}); got != "review this" {
		t.Fatalf("text = %q, want trimmed prompt text", got)
	}
	imageOnly := acp.PromptContent{Images: []acp.PromptImage{{Data: []byte{1}, MIMEType: "image/png"}}}
	if got := acpPromptTextForTurn(imageOnly); got != acpImageOnlyPromptText {
		t.Fatalf("text = %q, want the image-only carrier %q", got, acpImageOnlyPromptText)
	}
	if got := acpPromptTextForTurn(acp.PromptContent{}); got != "" {
		t.Fatalf("text = %q, want empty for an empty payload", got)
	}
}

func TestStageACPPromptImages_WritesFilesAndCleansUpOnSessionClose(t *testing.T) {
	sess := &acpHostSession{id: "sess-img"}
	first, err := stageACPPromptImages(sess, []acp.PromptImage{
		{Data: []byte("png-bytes"), MIMEType: "image/png"},
	})
	if err != nil {
		t.Fatalf("stageACPPromptImages failed: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("paths = %v, want one staged file", first)
	}
	if filepath.Ext(first[0]) != ".png" {
		t.Fatalf("extension = %q, want .png", filepath.Ext(first[0]))
	}
	raw, err := os.ReadFile(first[0])
	if err != nil {
		t.Fatalf("staged file unreadable: %v", err)
	}
	if string(raw) != "png-bytes" {
		t.Fatalf("staged bytes = %q, want %q", raw, "png-bytes")
	}
	stagingDir := sess.imageDir
	if stagingDir == "" || !strings.HasPrefix(first[0], stagingDir) {
		t.Fatalf("staged path %q is not inside the session staging dir %q", first[0], stagingDir)
	}

	second, err := stageACPPromptImages(sess, []acp.PromptImage{
		{Data: []byte("jpg-bytes"), MIMEType: "image/jpeg"},
	})
	if err != nil {
		t.Fatalf("second stageACPPromptImages failed: %v", err)
	}
	if second[0] == first[0] {
		t.Fatalf("staged files collide across turns: %q", second[0])
	}
	if filepath.Ext(second[0]) != ".jpg" {
		t.Fatalf("extension = %q, want .jpg", filepath.Ext(second[0]))
	}

	host := &acpSessionHost{sess: map[string]*acpHostSession{sess.id: sess}}
	host.closeSessionLocked(sess)
	if _, err := os.Stat(stagingDir); !os.IsNotExist(err) {
		t.Fatalf("staging dir %q still present after session close (err=%v)", stagingDir, err)
	}
}

func TestStageACPPromptImages_RejectsEmptyAndOversizedPayloads(t *testing.T) {
	sess := &acpHostSession{id: "sess-img"}
	if _, err := stageACPPromptImages(sess, []acp.PromptImage{{MIMEType: "image/png"}}); err == nil {
		t.Fatal("expected an error for an image block without data")
	}
	oversized := acp.PromptImage{Data: make([]byte, acpPromptImageMaxBytes+1), MIMEType: "image/png"}
	_, err := stageACPPromptImages(sess, []acp.PromptImage{oversized})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want an oversize rejection", err)
	}
}

func TestACPModelAcceptsImageInput_RespectsDeclaredModalities(t *testing.T) {
	visionSpec := config.ModelCapabilitySpec{InputModalities: []string{"text", "image"}}
	textSpec := config.ModelCapabilitySpec{InputModalities: []string{"text"}}

	cases := []struct {
		name string
		chat *ChatSession
		want bool
	}{
		{
			name: "declared vision model",
			chat: &ChatSession{
				Model:    "m-vision",
				Provider: config.Provider{ModelCapabilities: map[string]config.ModelCapabilitySpec{"m-vision": visionSpec}},
			},
			want: true,
		},
		{
			name: "declared text-only model",
			chat: &ChatSession{
				Model:    "m-text",
				Provider: config.Provider{ModelCapabilities: map[string]config.ModelCapabilitySpec{"m-text": textSpec}},
			},
			want: false,
		},
		{
			name: "undeclared model stays permissive",
			chat: &ChatSession{Model: "m-plain", Provider: config.Provider{}},
			want: true,
		},
		{
			name: "effective model wins over the requested one",
			chat: &ChatSession{
				Model:          "m-vision",
				EffectiveModel: "m-text",
				Provider: config.Provider{ModelCapabilities: map[string]config.ModelCapabilitySpec{
					"m-vision": visionSpec,
					"m-text":   textSpec,
				}},
			},
			want: false,
		},
		{
			name: "provider config fallback by provider name",
			chat: &ChatSession{
				Model:        "m-text",
				ProviderName: "p1",
				Config: &config.Config{Providers: config.ProvidersConfig{Items: map[string]config.Provider{
					"p1": {ModelCapabilities: map[string]config.ModelCapabilitySpec{"m-text": textSpec}},
				}}},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := acpModelAcceptsImageInput(tc.chat); got != tc.want {
				t.Fatalf("acpModelAcceptsImageInput = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestACPPromptRejectsImageForTextOnlyModel(t *testing.T) {
	host, chat := newThoughtLevelTestHost("sess-img", "m-text", nil)
	chat.Provider.ModelCapabilities = map[string]config.ModelCapabilitySpec{
		"m-text": {InputModalities: []string{"text"}},
	}

	_, err := host.Prompt(context.Background(), acp.PromptRequest{
		SessionID: "sess-img",
		Prompt: []acp.ContentBlock{{
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString([]byte("png-bytes")),
			MIMEType: "image/png",
		}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not accept image input") {
		t.Fatalf("error = %v, want an explicit text-only model rejection", err)
	}
	if chat.ImagePaths != nil {
		t.Fatalf("rejected prompt must not leave attachments behind: %#v", chat.ImagePaths)
	}
}

func TestACPPromptRejectsUnsupportedAudioBlock(t *testing.T) {
	host, _ := newThoughtLevelTestHost("sess-audio", "m1", nil)

	_, err := host.Prompt(context.Background(), acp.PromptRequest{
		SessionID: "sess-audio",
		Prompt:    []acp.ContentBlock{{Type: "audio", Data: "AAAA", MIMEType: "audio/wav"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "audio prompts are not supported") {
		t.Fatalf("error = %v, want an explicit audio rejection", err)
	}
}
