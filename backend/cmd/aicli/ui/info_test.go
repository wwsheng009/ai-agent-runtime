package ui

import (
	"fmt"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"io"
	"os"
	"strings"
	"testing"
)

func TestPrintSessionInfo_AlignsLabelsIntoColumns(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
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
	t.Setenv("NO_COLOR", "1")
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

func TestSessionInfoDocumentUsesRolesAndWidth(t *testing.T) {
	doc := SessionInfoDocument(SessionInfo{
		ProviderName: "codex",
		EndpointURL:  "https://example.com/a/very/long/responses/endpoint",
		ModelName:    "gpt-5.6-codex",
		IsStream:     true,
	}, 40)
	if doc.LineCount() < 6 {
		t.Fatalf("session info lines=%d: %+v", doc.LineCount(), doc)
	}
	foundSuccess := false
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			if render.LineWidth(line) > 40 {
				t.Fatalf("session line overflow: width=%d line=%+v", render.LineWidth(line), line)
			}
			for _, span := range line.Spans {
				if span.Style.Role == string(style.RoleSuccess) {
					foundSuccess = true
				}
				if strings.ContainsRune(span.Text, '\x1b') {
					t.Fatalf("session IR contains ESC: %q", span.Text)
				}
			}
		}
	}
	if !foundSuccess {
		t.Fatal("session document lost success role")
	}
	plain := render.PlainBackend{}.Render(doc)
	if !strings.Contains(plain, "═") || !strings.Contains(plain, "Provider:") {
		t.Fatalf("unexpected session projection: %q", plain)
	}
}

func TestTableDocumentUsesCellWidthAndBoundsHugeCells(t *testing.T) {
	doc := TableDocument(
		[]string{"名称", "状态", "说明"},
		[][]string{{"渲染器", "完成", strings.Repeat("structured-", 10000)}},
		24,
	)
	if doc.LineCount() != 3 {
		t.Fatalf("table lines=%d", doc.LineCount())
	}
	for _, line := range doc.Blocks[0].Lines {
		if width := render.LineWidth(line); width > 24 {
			t.Fatalf("table overflow width=%d line=%+v", width, line)
		}
	}
	plain := render.PlainBackend{}.Render(doc)
	if !strings.Contains(plain, "名称") || !strings.Contains(plain, "…") {
		t.Fatalf("table projection lost content/truncation: %q", plain)
	}
}

func TestPrintKeyValuesSortsKeysDeterministically(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	output := captureUIStdout(t, func() {
		PrintKeyValues(map[string]string{
			"zeta":  "3",
			"alpha": "1",
			"名称":    "2",
		})
	})
	alpha := strings.Index(output, "alpha:")
	zeta := strings.Index(output, "zeta:")
	name := strings.Index(output, "名称:")
	if alpha < 0 || zeta < 0 || name < 0 || !(alpha < zeta && zeta < name) {
		t.Fatalf("key order is not deterministic: %q", output)
	}
}

func TestRenderInfoDocumentHonorsANSI16Profile(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("AICLI_COLOR_DEPTH", "ansi16")

	out := renderInfoDocument(infoKeyValueDocument("Mode", "structured", 0))
	if !strings.ContainsRune(out, '\x1b') {
		t.Fatalf("forced ANSI-16 info has no SGR: %q", out)
	}
	if strings.Contains(out, "\x1b[38;2;") || strings.Contains(out, "\x1b[38;5;") {
		t.Fatalf("ANSI-16 info contains higher-depth color: %q", out)
	}
}

func TestSessionInfoDocument_PlainProjection(t *testing.T) {
	doc := SessionInfoDocument(SessionInfo{
		ProviderName: "p",
		Protocol:     "proto",
		ModelName:    "m",
		IsStream:     true,
		SupportsFast: true,
		IsFast:       false,
	}, 40)
	plain := RenderDocumentPlain(doc)
	for _, want := range []string{"Provider:", "( p )", "Protocol:", "proto", "Model:", "m", "Stream:", "on", "Fast:", "off"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain missing %q:\n%s", want, plain)
		}
	}
}

func TestTableDocument_FitsAndAligns(t *testing.T) {
	doc := TableDocument(
		[]string{"Name", "Value"},
		[][]string{
			{"alpha", "1"},
			{"beta-long", "22"},
		},
		20,
	)
	plain := RenderDocumentPlain(doc)
	if !strings.Contains(plain, "Name") || !strings.Contains(plain, "Value") {
		t.Fatalf("headers missing:\n%s", plain)
	}
	if !strings.Contains(plain, "alpha") || !strings.Contains(plain, "beta") {
		t.Fatalf("rows missing:\n%s", plain)
	}
	// Separator row uses dashes.
	if !strings.Contains(plain, "--") {
		t.Fatalf("expected separator dashes:\n%s", plain)
	}
}

func TestFitInfoColumnWidths(t *testing.T) {
	got := fitInfoColumnWidths([]int{10, 10, 10}, 20, 2)
	sum := 0
	for _, w := range got {
		if w < 1 {
			t.Fatalf("column too small: %v", got)
		}
		sum += w
	}
	// 20 total with 2 gaps of 2 => content budget 16
	if sum+2*2 > 20 {
		t.Fatalf("did not fit: widths=%v sum=%d", got, sum)
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

	var stdoutData []byte
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		stdoutData, _ = io.ReadAll(reader)
	}()

	fn()

	_ = writer.Close()
	<-readDone
	return string(stdoutData)
}
