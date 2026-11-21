package memoryruntime

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// captureDigestText projects only user/assistant messages into the durable
// capture-backed digest. The canonical range has already been validated by the
// caller; this helper still redacts each message before applying the configured
// character bounds so no raw transcript text reaches the memory store.
func captureDigestText(transcript Transcript, cfg Config) (memorystore.TurnCaptureDigestText, error) {
	if !cfg.TurnDigestStoreText {
		return memorystore.TurnCaptureDigestText{}, nil
	}

	userText, userTruncated, err := captureDigestRoleText(transcript.Messages, "user", cfg.TurnDigestMaxUserChars)
	if err != nil {
		return memorystore.TurnCaptureDigestText{}, err
	}
	assistantText, assistantTruncated, err := captureDigestRoleText(
		transcript.Messages, "assistant", cfg.TurnDigestMaxAssistantChars,
	)
	if err != nil {
		return memorystore.TurnCaptureDigestText{}, err
	}
	return memorystore.TurnCaptureDigestText{
		UserText:      userText,
		AssistantText: assistantText,
		TextTruncated: userTruncated || assistantTruncated,
	}, nil
}

func captureDigestRoleText(messages []TranscriptMessage, role string, maxRunes int) (string, bool, error) {
	var b strings.Builder
	runeCount := 0
	truncated := false

	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), role) {
			continue
		}
		redacted, _ := RedactForPrompt(strings.TrimSpace(message.Content))
		if !utf8.ValidString(redacted) {
			return "", false, fmt.Errorf("memoryruntime: canonical message is not valid UTF-8")
		}
		if redacted == "" {
			continue
		}

		if maxRunes <= 0 {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(redacted)
			continue
		}

		if runeCount > 0 {
			if runeCount == maxRunes {
				truncated = true
				break
			}
			b.WriteByte('\n')
			runeCount++
		}
		remaining := maxRunes - runeCount
		prefix, partTruncated := prefixUTF8Runes(redacted, remaining)
		b.WriteString(prefix)
		runeCount += utf8.RuneCountInString(prefix)
		if partTruncated {
			truncated = true
			break
		}
	}

	if truncated {
		b.WriteString("…")
	}
	return b.String(), truncated, nil
}

func prefixUTF8Runes(text string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return "", text != ""
	}
	count := 0
	for index := range text {
		if count == maxRunes {
			return text[:index], true
		}
		count++
	}
	return text, false
}
