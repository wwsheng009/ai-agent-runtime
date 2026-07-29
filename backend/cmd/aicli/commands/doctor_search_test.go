package commands

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	runtimeripgrep "github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
)

func TestInspectDoctorSearchReportsRipgrepBackend(t *testing.T) {
	report := inspectDoctorSearch(context.Background(), doctorSearchDeps{
		resolve: func() (runtimeripgrep.Resolution, error) {
			return runtimeripgrep.Resolution{Path: `C:\aicli\codex-path\rg.exe`, Source: runtimeripgrep.SourceBundled}, nil
		},
		probeVersion: func(context.Context, string) (string, error) {
			return "ripgrep 15.1.0", nil
		},
	})

	if !report.ToolkitSearchAvailable || !report.RipgrepAvailable || report.SelectedBackend != "rg" {
		t.Fatalf("unexpected search report: %#v", report)
	}
	if report.Source != runtimeripgrep.SourceBundled || report.Version != "ripgrep 15.1.0" {
		t.Fatalf("unexpected rg provenance: %#v", report)
	}
}

func TestInspectDoctorSearchReportsBuiltinFallback(t *testing.T) {
	report := inspectDoctorSearch(context.Background(), doctorSearchDeps{
		resolve: func() (runtimeripgrep.Resolution, error) {
			return runtimeripgrep.Resolution{}, errors.New("rg missing")
		},
		probeVersion: func(context.Context, string) (string, error) {
			t.Fatal("version probe must not run without a resolved executable")
			return "", nil
		},
	})

	if !report.ToolkitSearchAvailable || report.RipgrepAvailable || report.SelectedBackend != "builtin" {
		t.Fatalf("unexpected fallback report: %#v", report)
	}
	if !strings.Contains(report.Error, "rg missing") {
		t.Fatalf("expected resolver error, got %#v", report)
	}
}

func TestRenderDoctorSearchReportJSON(t *testing.T) {
	var output bytes.Buffer
	err := renderDoctorSearchReport(&output, doctorSearchReport{
		ToolkitSearchAvailable: true,
		RipgrepAvailable:       true,
		SelectedBackend:        "rg",
		Path:                   "/opt/aicli/codex-path/rg",
		Source:                 runtimeripgrep.SourceBundled,
		Version:                "ripgrep 15.1.0",
	}, "json")
	if err != nil {
		t.Fatalf("render JSON: %v", err)
	}
	for _, expected := range []string{`"selected_backend":"rg"`, `"source":"bundled"`, `"version":"ripgrep 15.1.0"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("expected JSON to contain %q, got %s", expected, output.String())
		}
	}
}
