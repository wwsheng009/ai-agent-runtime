package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestPrintSessionInfo_AlignsLabelsIntoColumns(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	SetTheme(ThemeAuto)

	output := captureUIStdout(t, func() {
		PrintSessionInfo(SessionInfo{
			ProviderName: "codex_ee",
			Protocol:     "codex",
			ModelName:    "gpt-5.2-codex",
			EndpointURL:  "https://ai.last.ee/v1/responses",
			Host:         "ai.last.ee",
			KeyCount:     1,
			Timeout:      "5m0s",
			IsStream:     true,
			SupportsFast: true,
			IsFast:       true,
		})
	})

	theme := GetTheme(ThemeAuto)
	childPrefix := sessionInfoChildPrefix(theme)
	for _, expected := range []string{
		fmt.Sprintf("%s%-*s %s", theme.SystemIcon+" ", sessionInfoLabelWidth, "Provider:", "( codex_ee )"),
		fmt.Sprintf("%s%-*s %s", childPrefix, sessionInfoLabelWidth, "Protocol:", "codex"),
		fmt.Sprintf("%s%-*s %s", childPrefix, sessionInfoLabelWidth, "Host:", "ai.last.ee"),
		fmt.Sprintf("%s%-*s %s", theme.SystemIcon+" ", sessionInfoLabelWidth, "Model:", "gpt-5.2-codex"),
		fmt.Sprintf("%s%-*s %s", theme.SystemIcon+" ", sessionInfoLabelWidth, "Stream:", "on"),
		fmt.Sprintf("%s%-*s %s", theme.SystemIcon+" ", sessionInfoLabelWidth, "Fast:", "on"),
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestPrintSessionInfo_OmitsFastWhenUnsupported(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	SetTheme(ThemeAuto)

	output := captureUIStdout(t, func() {
		PrintSessionInfo(SessionInfo{
			ProviderName: "openai",
			Protocol:     "openai",
			ModelName:    "gpt-4.1",
			IsStream:     false,
			SupportsFast: false,
			IsFast:       true, // must still omit when unsupported
		})
	})
	if strings.Contains(output, "Fast:") {
		t.Fatalf("expected Fast row omitted when unsupported, got:\n%s", output)
	}
}

func captureUIStdout(t *testing.T, fn func()) string {
	t.Helper()

	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
	}()

	fn()

	_ = writer.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	_ = reader.Close()
	return string(data)
}
